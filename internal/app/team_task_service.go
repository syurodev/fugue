package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	maxTeamNoteBytes     = 2 << 10
	maxTeamNotesCount    = 100
	maxTeamBriefBytes    = 16 << 10
	maxTaskSendBytes     = 16 << 10
	maxCancelReasonBytes = 512
	cancelFailureCode    = "cancelled_by_delegator"
	maxAppendRetries     = 3
	defaultResultLimit   = 16 << 10
	maxResultLimit       = 64 << 10
)

type teamNotePayload struct {
	AuthorTaskID string `json:"author_task_id"`
	Note         string `json:"note"`
}

type taskQuestionPayload struct {
	Question string `json:"question"`
	To       string `json:"to"`
}

type teamTaskService struct {
	store      *vendorstore.Store
	sessions   taskSessions
	roots      rootResolver
	git        *gitops.Manager
	dataDir    string
	ids        ids.Generator
	vault      *vault.Service
	resultGate ResultGate
	dispatch   *teamDispatcher
	wake       *teamWake
}

func newTeamTaskService(store *vendorstore.Store, sessions taskSessions, roots rootResolver,
	git *gitops.Manager, dataDir string, gen ids.Generator, v *vault.Service, gate ResultGate,
) *teamTaskService {
	if gate == nil {
		gate = unconfiguredResultGate{}
	}
	return &teamTaskService{
		store: store, sessions: sessions, roots: roots, git: git,
		dataDir: dataDir, ids: gen, vault: v, resultGate: gate,
	}
}

func (s *teamTaskService) SetRuntime(dispatch *teamDispatcher, wake *teamWake) {
	s.dispatch = dispatch
	s.wake = wake
}

type callerTeamContext struct {
	team      vendorstore.TeamRecord
	tasks     []vendorstore.TaskRecord
	caller    *vendorstore.TaskRecord
	role      vendorstore.TeamRole
	sessionID string
}

func (s *teamTaskService) resolveCaller(ctx context.Context, sessionID string) (*callerTeamContext, error) {
	if sessionID == "" {
		return nil, errcode.New(errcode.PermissionDenied, "session identity is required")
	}
	teamID, task, found, err := s.callerTeamID(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return nil, boundaryError("read the team", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return nil, errcode.New(errcode.FailedPrecondition, "the team is not active")
	}
	return buildCallerContext(&rec, tasks, sessionID, &task, found)
}

func (s *teamTaskService) callerTeamID(ctx context.Context, sessionID string) (string, vendorstore.TaskRecord, bool, error) {
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		return "", vendorstore.TaskRecord{}, false, boundaryError("read the caller task", err)
	}
	if found {
		return task.TeamID, task, true, nil
	}
	rec, active, teamErr := s.store.ActiveTeamForSession(ctx, sessionID)
	if teamErr != nil {
		return "", vendorstore.TaskRecord{}, false, boundaryError("read the caller team", teamErr)
	}
	if !active {
		return "", vendorstore.TaskRecord{}, false, errcode.New(errcode.PermissionDenied, "session does not belong to an active team")
	}
	return rec.TeamID, vendorstore.TaskRecord{}, false, nil
}

func buildCallerContext(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, sessionID string, task *vendorstore.TaskRecord, found bool) (*callerTeamContext, error) {
	res := &callerTeamContext{team: *rec, tasks: tasks, sessionID: sessionID}
	if found {
		t, ok := findTask(tasks, task.TaskID)
		if !ok {
			return nil, errcode.New(errcode.PermissionDenied, "caller task not found in team")
		}
		res.caller, res.role = t, t.HierarchyRole
		return res, nil
	}
	res.role = vendorstore.RoleLead
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			res.caller = &tasks[i]
			break
		}
	}
	return res, nil
}

func findTask(tasks []vendorstore.TaskRecord, taskID string) (*vendorstore.TaskRecord, bool) {
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return &tasks[i], true
		}
	}
	return nil, false
}

func (c *callerTeamContext) requireDelegator(taskID string) (*vendorstore.TaskRecord, error) {
	if c.role != vendorstore.RoleLead && c.role != vendorstore.RoleTeamManager {
		return nil, errcode.New(errcode.PermissionDenied, "only a lead or team manager may delegate tasks")
	}
	if err := teams.ValidateTaskIdentifier("task_id", taskID); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	target, ok := findTask(c.tasks, taskID)
	if !ok {
		return nil, errcode.New(errcode.NotFound, "team task not found")
	}
	if err := teams.ValidateRoleIdentifier("role", target.CatalogRole); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	if c.caller == nil || target.ParentTaskID != c.caller.TaskID {
		return nil, errcode.New(errcode.PermissionDenied, "cannot act on a task outside your delegation subtree")
	}
	return target, nil
}

func (c *callerTeamContext) requireReadableTask(taskID string) (*vendorstore.TaskRecord, error) {
	if err := teams.ValidateTaskIdentifier("task_id", taskID); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	target, ok := findTask(c.tasks, taskID)
	if !ok {
		return nil, errcode.New(errcode.NotFound, "team task not found")
	}
	if err := teams.ValidateRoleIdentifier("role", target.CatalogRole); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	switch c.role {
	case vendorstore.RoleLead, vendorstore.RoleTeamManager:
		if c.caller != nil && target.ParentTaskID == c.caller.TaskID {
			return target, nil
		}
	case vendorstore.RoleMember:
		if c.caller != nil && slices.Contains(c.caller.DependsOn, target.TaskID) {
			return target, nil
		}
	case vendorstore.RoleSolo:
	}
	return nil, errcode.New(errcode.PermissionDenied, "cannot inspect a task outside your scope")
}

// Start starts a queued child task immediately without waiting for completion.
func (s *teamTaskService) Start(ctx context.Context, sessionID, taskID string) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return "", err
	}
	if s.dispatch == nil {
		return "", errcode.New(errcode.FailedPrecondition, "team dispatcher is not configured")
	}
	if err := s.dispatch.StartTask(ctx, c.team.TeamID, target.TaskID); err != nil {
		return "", err
	}
	return fmt.Sprintf("task %s started", target.TaskID), nil
}

// Send delivers a follow-up message to a running/asking direct child task via the mailbox.
func (s *teamTaskService) Send(ctx context.Context, sessionID, taskID, message string) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return "", err
	}
	if target.State != vendorstore.TaskStateRunning && target.State != vendorstore.TaskStateAsking {
		return "", errcode.New(errcode.FailedPrecondition, "task is not accepting messages in its current state")
	}
	trimmed := strings.TrimSpace(message)
	if trimmed == "" || len(trimmed) > maxTaskSendBytes {
		return "", errcode.New(errcode.InvalidArgument, "message must be 1..16384 bytes")
	}
	payload, err := json.Marshal(mailboxDigestItem{TaskID: c.caller.TaskID, State: "message", Summary: trimmed})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode task send payload", err)
	}
	uid, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate message ID", err)
	}
	if err := s.store.EnqueueDirectMailbox(ctx, c.team.TeamID, target.TaskID, vendorstore.MailboxKindSend, uid, payload); err != nil {
		return "", boundaryError("enqueue task message", err)
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return "message queued for task " + target.TaskID, nil
}

// Cancel cancels a non-terminal direct child task (and any non-terminal
// descendants in its subtree) and closes their active sessions.
func (s *teamTaskService) Cancel(ctx context.Context, sessionID, taskID, reason string) (string, error) {
	cutReason, _ := truncateUTF8(strings.TrimSpace(reason), maxCancelReasonBytes)
	payload, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: cutReason})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode cancel reason", err)
	}
	sessions := make([]string, 0, 1)
	var rootID string
	if err := s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		ev, sid, buildErr := s.buildCancelEvent(c, taskID, payload)
		if buildErr != nil {
			return nil, buildErr
		}
		rootID = ev.TaskID
		if sid != "" {
			sessions = append(sessions, sid)
		}
		return ev, nil
	}); err != nil {
		return "", err
	}
	sessions = append(sessions, s.cancelSubtreeDescendants(ctx, sessionID, rootID, payload)...)
	s.closeCancelledSessions(ctx, sessions)
	return "task " + rootID + " cancelled", nil
}

func (s *teamTaskService) buildCancelEvent(c *callerTeamContext, taskID string, payload []byte) (*vendorstore.AppendTeamEventInput, string, error) {
	target, delErr := c.requireDelegator(taskID)
	if delErr != nil {
		return nil, "", delErr
	}
	if teamTaskTerminal(target.State) {
		return nil, "", errcode.New(errcode.FailedPrecondition, "task is already terminal")
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "generate cancel event ID", idErr)
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: target.TaskID, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateCancelled), FailureCode: cancelFailureCode,
		ProtectedPayload: payload,
	}, target.SessionID, nil
}

func (s *teamTaskService) closeCancelledSessions(ctx context.Context, sessions []string) {
	if s.sessions == nil {
		return
	}
	for _, sid := range sessions {
		if closeErr := s.sessions.Close(ctx, sid, vendor.CloseHostStop); closeErr != nil {
			slog.WarnContext(ctx, "closing cancelled task session failed", "session_id", sid, "error_code", string(errcode.CodeOf(closeErr)))
		}
	}
}

func (s *teamTaskService) cancelSubtreeDescendants(ctx context.Context, sessionID, rootID string, payload []byte) []string {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return nil
	}
	desc := collectSubtreeTasks(c.tasks, rootID)
	sessions := make([]string, 0, len(desc))
	for i := range desc {
		if teamTaskTerminal(desc[i].State) {
			continue
		}
		if sid := s.cancelDescendant(ctx, c.team.TeamID, desc[i].TaskID, payload); sid != "" {
			sessions = append(sessions, sid)
		}
	}
	return sessions
}

func (s *teamTaskService) cancelDescendant(ctx context.Context, teamID, taskID string, payload []byte) string {
	var sid string
	if err := s.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || teamTaskTerminal(cur.State) {
			return nil, errcode.New(errcode.FailedPrecondition, "descendant already terminal")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate cancel event ID", idErr)
		}
		sid = cur.SessionID
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TaskStateCancelled), FailureCode: cancelFailureCode,
			ProtectedPayload: payload,
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "cancelling subtree descendant failed", "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return ""
	}
	return sid
}

func collectSubtreeTasks(tasks []vendorstore.TaskRecord, rootID string) []vendorstore.TaskRecord {
	queue := []string{rootID}
	var out []vendorstore.TaskRecord
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for i := range tasks {
			if tasks[i].ParentTaskID == parent {
				out = append(out, tasks[i])
				queue = append(queue, tasks[i].TaskID)
			}
		}
	}
	return out
}

// Status returns metadata status for a readable task (§6.3).
func (s *teamTaskService) Status(ctx context.Context, sessionID, taskID string) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireReadableTask(taskID)
	if err != nil {
		return "", err
	}
	var children []string
	for i := range c.tasks {
		if c.tasks[i].ParentTaskID == target.TaskID {
			children = append(children, fmt.Sprintf("%s:%s", c.tasks[i].TaskID, c.tasks[i].State))
		}
	}
	var held []HeldMailboxEntry
	if s.wake != nil {
		for _, h := range s.wake.HeldRecipients() {
			if h.TeamID == c.team.TeamID && h.TaskID == target.TaskID {
				held = append(held, h)
			}
		}
	}
	out, err := json.Marshal(struct {
		TaskID      string             `json:"task_id"`
		State       string             `json:"state"`
		FailureCode string             `json:"failure_code,omitempty"`
		Role        string             `json:"role"`
		Deliverable string             `json:"deliverable"`
		Children    []string           `json:"children,omitempty"`
		MailboxHeld []HeldMailboxEntry `json:"mailbox_held,omitempty"`
	}{
		TaskID: target.TaskID, State: string(target.State), FailureCode: target.FailureCode,
		Role: target.CatalogRole, Deliverable: string(target.Deliverable), Children: children, MailboxHeld: held,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode task status", err)
	}
	return string(out), nil
}

// Note appends a sealed team note (Q-130: lead or team_manager only, <= 2 KiB, <= 100 notes).
func (s *teamTaskService) Note(ctx context.Context, sessionID, note string) (string, error) {
	trimmed := strings.TrimSpace(note)
	if trimmed == "" || len(trimmed) > maxTeamNoteBytes {
		return "", errcode.New(errcode.InvalidArgument, "note must be 1..2048 bytes")
	}
	if err := s.appendTeamWithRetry(ctx, sessionID, func(ctx context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		return s.buildNoteEvent(ctx, c, trimmed)
	}); err != nil {
		return "", err
	}
	return "team note recorded", nil
}

func (s *teamTaskService) buildNoteEvent(ctx context.Context, c *callerTeamContext, trimmed string) (*vendorstore.AppendTeamEventInput, error) {
	if c.role != vendorstore.RoleLead && c.role != vendorstore.RoleTeamManager {
		return nil, errcode.New(errcode.PermissionDenied, "only a lead or team manager may write team notes")
	}
	count, countErr := s.store.CountTeamNotes(ctx, c.team.TeamID)
	if countErr != nil {
		return nil, boundaryError("count team notes", countErr)
	}
	if count >= maxTeamNotesCount {
		return nil, errcode.New(errcode.FailedPrecondition, "team note limit reached")
	}
	authorID := ""
	if c.caller != nil {
		authorID = c.caller.TaskID
	}
	payload, encErr := json.Marshal(teamNotePayload{AuthorTaskID: authorID, Note: trimmed})
	if encErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode team note", encErr)
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate note event ID", idErr)
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, EventType: vendorstore.EventTeamNoted, ProtectedPayload: payload,
	}, nil
}

// Brief reads the team brief and notes in order, rendered via nonce-bound untrusted blocks (Q-130).
func (s *teamTaskService) Brief(ctx context.Context, sessionID string, offset, limit int) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		return "", errcode.New(errcode.InvalidArgument, "offset must be non-negative")
	}
	if limit <= 0 || limit > maxTeamBriefBytes {
		limit = maxTeamBriefBytes
	}
	entries, err := s.briefEntries(ctx, c.team.TeamID, offset)
	if err != nil {
		return "", err
	}
	nonce, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate brief nonce", err)
	}
	return teams.BuildDigest("Team brief and notes.", entries, nonce, limit), nil
}

func (s *teamTaskService) briefEntries(ctx context.Context, teamID string, offset int) ([]teams.DigestEntry, error) {
	var entries []teams.DigestEntry
	if offset == 0 {
		if brief := s.readProposalBrief(ctx, teamID); brief != "" {
			entries = append(entries, teams.DigestEntry{TaskID: "team-brief", State: "brief", Summary: brief})
		}
	}
	notePayloads, _, err := s.store.OpenTeamNotePayloads(ctx, teamID, max(offset-1, 0), maxTeamNotesCount)
	if err != nil {
		return nil, boundaryError("open team notes", err)
	}
	for _, raw := range notePayloads {
		var np teamNotePayload
		if json.Unmarshal(raw, &np) == nil {
			entries = append(entries, teams.DigestEntry{TaskID: np.AuthorTaskID, State: "note", Summary: np.Note})
		}
	}
	return entries, nil
}

func (s *teamTaskService) readProposalBrief(ctx context.Context, teamID string) string {
	raw, err := s.store.OpenTeamEventPayload(ctx, teamID, teamProposedSequence)
	if err != nil {
		return ""
	}
	var p teams.ProposalInput
	if json.Unmarshal(raw, &p) != nil {
		return ""
	}
	return p.Brief
}
