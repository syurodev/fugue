package app

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

const (
	maxTeamNoteBytes             = 2 << 10
	maxTeamNotesCount            = 100
	maxTeamBriefBytes            = 16 << 10
	maxTaskSendBytes             = 4 << 10
	maxEscapedDigestMessageBytes = 6 << 10
	maxAppendRetries             = 3
	defaultResultLimit           = 16 << 10
	maxResultLimit               = 64 << 10
)

func validateDigestMessage(field, text string) (string, error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) > maxTaskSendBytes {
		return "", errcode.New(errcode.InvalidArgument, fmt.Sprintf("%s must be 1..%d bytes", field, maxTaskSendBytes))
	}
	if len(teams.EscapeDigestText(trimmed)) > maxEscapedDigestMessageBytes {
		return "", errcode.New(errcode.InvalidArgument, field+" exceeds escaped digest budget")
	}
	return trimmed, nil
}

type teamNotePayload struct {
	AuthorTaskID string `json:"author_task_id"`
	Note         string `json:"note"`
}

type taskQuestionPayload struct {
	Question            string `json:"question"`
	To                  string `json:"to"`
	ForwardedFromTaskID string `json:"forwarded_from_task_id,omitempty"`
}

type teamTaskService struct {
	store        *vendorstore.Store
	sessions     taskSessions
	roots        rootResolver
	git          *gitops.Manager
	dataDir      string
	ids          ids.Generator
	vault        *vault.Service
	resultGate   ResultGate
	dispatch     *teamDispatcher
	wake         *teamWake
	interactions *InteractionService
	teamsCfg     func(context.Context, string) (workspace.TeamsConfig, error)
	budgets      *teamBudgets
	clk          clock.Clock
	unlockMu     sync.Mutex
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

func (s *teamTaskService) SetSessions(sess taskSessions) {
	s.sessions = sess
}

func (s *teamTaskService) SetInteractions(inter *InteractionService) {
	s.interactions = inter
}

func (s *teamTaskService) SetTeamsConfig(fn func(context.Context, string) (workspace.TeamsConfig, error)) {
	s.teamsCfg = fn
}

func (s *teamTaskService) SetBudgets(b *teamBudgets) {
	s.budgets = b
}

func (s *teamTaskService) SetClock(clk clock.Clock) {
	s.clk = clk
}

func (s *teamTaskService) now() time.Time {
	if s.clk != nil {
		return s.clk.Now()
	}
	return (clock.Real{}).Now()
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
	if c.caller != nil && c.caller.State != vendorstore.TaskStateRunning {
		return nil, errcode.New(errcode.FailedPrecondition, "caller task is not running")
	}
	if err := teams.ValidateDurableTaskID("task_id", taskID); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	target, ok := findTask(c.tasks, taskID)
	if !ok {
		return nil, errcode.New(errcode.NotFound, "team task not found")
	}
	if err := teams.ValidateRoleIdentifier("role", target.CatalogRole); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	byID, retryChild := resolveTaskChain(c.tasks)
	if c.caller == nil || !sameAttemptChain(target.ParentTaskID, c.caller.TaskID, byID, retryChild) {
		return nil, errcode.New(errcode.PermissionDenied, "cannot act on a task outside your delegation subtree")
	}
	return target, nil
}

func (c *callerTeamContext) requireReadableTask(taskID string) (*vendorstore.TaskRecord, error) {
	if err := teams.ValidateDurableTaskID("task_id", taskID); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	target, ok := findTask(c.tasks, taskID)
	if !ok {
		return nil, errcode.New(errcode.NotFound, "team task not found")
	}
	if err := teams.ValidateRoleIdentifier("role", target.CatalogRole); err != nil {
		return nil, errcode.New(errcode.InvalidArgument, err.Error())
	}
	if c.canInspectTask(target) {
		return target, nil
	}
	return nil, errcode.New(errcode.PermissionDenied, "cannot inspect a task outside your scope")
}

func (c *callerTeamContext) canInspectTask(target *vendorstore.TaskRecord) bool {
	if c.caller == nil {
		return false
	}
	byID, retryChild := resolveTaskChain(c.tasks)
	switch c.role {
	case vendorstore.RoleLead, vendorstore.RoleTeamManager:
		return sameAttemptChain(target.ParentTaskID, c.caller.TaskID, byID, retryChild) || dependsOnTask(c.caller, c.tasks, target.TaskID)
	case vendorstore.RoleMember:
		return dependsOnTask(c.caller, c.tasks, target.TaskID)
	case vendorstore.RoleSolo:
		return false
	default:
		return false
	}
}

// dependsOnTask reports whether targetID is either listed directly in
// caller.DependsOn or is an attempt in the retry chain of a listed dependency
// (Q-126, OR-E18).
func dependsOnTask(caller *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord, targetID string) bool {
	if slices.Contains(caller.DependsOn, targetID) {
		return true
	}
	byID, retryChild := resolveTaskChain(tasks)
	for _, dep := range caller.DependsOn {
		if latest := latestAttempt(dep, byID, retryChild); latest != nil && latest.TaskID == targetID {
			return true
		}
	}
	return false
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
	trimmed, err := validateDigestMessage("message", message)
	if err != nil {
		return "", err
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
	byID, retryChild := resolveTaskChain(c.tasks)
	var children []string
	for i := range c.tasks {
		if sameAttemptChain(c.tasks[i].ParentTaskID, target.TaskID, byID, retryChild) {
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
