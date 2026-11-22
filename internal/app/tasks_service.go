package app

// F11.10b tasks service (host-api v0.17 §5.3, Q-175 A): the Host API surface
// of the team channel. PageChannel serves the lines a local client may read —
// the content opens from the sealed source payloads, so the whole read fails
// closed while the vault is locked. PostChannelMessage records one §6.7 user
// post as a TeamUserPosted event whose transaction projects the channel line
// and enqueues the delegator's informational mention, then delivers the text
// to the routed agent as an immediate origin=user turn (never through the
// §6.1 pacing queue).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// TasksRequestMaxBytes bounds one tasks RPC request on the wire: the post
// text is capped at 4 KiB and the rest of the arguments are identifiers.
const TasksRequestMaxBytes = 16 * 1024

// maxChannelPostBytes is the §5.3 cap of one channel post.
const maxChannelPostBytes = 4 << 10

// ChannelLineView is one line of a PageChannel page (§5.3): the metadata of
// the team_channel_messages row plus the attempt of its source task and the
// content opened from the sealed source payload.
type ChannelLineView struct {
	ChannelSeq      int64  `json:"channel_seq"`
	Kind            string `json:"kind"`
	SenderKind      string `json:"sender_kind"`
	SenderTaskID    string `json:"sender_task_id,omitempty"`
	RecipientTaskID string `json:"recipient_task_id,omitempty"`
	ThreadRoot      string `json:"thread_root_task_id,omitempty"`
	Attempt         uint32 `json:"attempt"`
	SourceSeq       int64  `json:"source_seq"`
	CreatedAt       int64  `json:"created_at"`
	Content         string `json:"content,omitempty"`
}

// PostChannelCommand is one PostChannelMessage call (§5.3): command_id dedup
// guards replays; the routing falls back from to_task_id to the thread's task
// agent and then to the lead.
type PostChannelCommand struct {
	CommandID    string
	TeamID       string
	ThreadTaskID string
	ToTaskID     string
	Text         string
}

// PostChannelResult carries the `#n` of the new line.
type PostChannelResult struct {
	ChannelSeq int64 `json:"channel_seq"`
}

// teamUserPostedPayload is the sealed payload of EventTeamUserPosted (§6.7,
// Q-178 A): the routed target and the user's text. The message field name
// matches the other `send` payloads, so one content extractor serves both.
type teamUserPostedPayload struct {
	ToTaskID string `json:"to_task_id"`
	Message  string `json:"message"`
}

// taskApprovalRefPayload is the sealed payload of TaskApprovalRequested and
// TaskApprovalResolved (Q-178 A): only a reference — the interaction request,
// the tool, and, once resolved, the outcome. The request and answer content
// never leaves the approvals inbox (interaction §7, VR-05).
type taskApprovalRefPayload struct {
	RequestID string `json:"request_id"`
	Tool      string `json:"tool"`
	Outcome   string `json:"outcome,omitempty"`
}

// command dedup state, following the appstate fingerprint pattern.
type tasksDedupEntry struct {
	seq         int64
	fingerprint string
}

// TasksService serves the channel Host API and the §6.7 user posts.
type TasksService struct {
	store      *vendorstore.Store
	sessions   taskSessions
	vault      *vault.Service
	clk        clock.Clock
	ids        ids.Generator
	workspaces *workspace.Service
	git        *gitops.Manager
	dataDir    string

	dedupMu    sync.Mutex
	dedup      map[string]tasksDedupEntry
	dedupOrder []string
}

// NewTasksService builds the service; the host wires it with the team
// services so the port and the store are shared.
func NewTasksService(store *vendorstore.Store, sessions taskSessions, v *vault.Service, clk clock.Clock, gen ids.Generator) *TasksService {
	return &TasksService{
		store: store, sessions: sessions, vault: v, clk: clk, ids: gen,
		dedup: make(map[string]tasksDedupEntry, 16),
	}
}

// RegisterTeamChannel registers one team's channel topic from outside the
// startup enumeration (F11.10b tests; a team created outside the decide path
// would otherwise have no topic to subscribe to).
func (rt *HostRuntime) RegisterTeamChannel(teamID string) {
	if rt.teamChannels != nil {
		rt.teamChannels.RegisterTeam(teamID)
	}
}

// TasksService accessor for the host API handlers; unavailable before the
// vendor runtime composes.
func (rt *HostRuntime) TasksService() (*TasksService, error) {
	if rt.tasksService == nil {
		return nil, errcode.New(errcode.Unavailable, "the tasks service is unavailable")
	}
	return rt.tasksService, nil
}

// PageChannel pages one team's channel (§5.3): without thread_task_id the
// general channel, with it that task's thread; newest first, before/since as
// exclusive cursors. Fails closed while the vault is locked.
func (s *TasksService) PageChannel(ctx context.Context, teamID, threadTaskID string, before, since int64, limit int) ([]ChannelLineView, error) {
	if err := s.pageChannelGate(); err != nil {
		return nil, err
	}
	q, err := s.pageChannelQuery(ctx, teamID, threadTaskID, before, since, limit)
	if err != nil {
		return nil, err
	}
	rows, err := s.store.TeamChannelMessages(ctx, teamID, q)
	if err != nil {
		return nil, boundaryError("read the team channel", err)
	}
	bySeq, err := s.sourceEvents(ctx, teamID)
	if err != nil {
		return nil, err
	}
	out := make([]ChannelLineView, 0, len(rows))
	for i := range rows {
		row := &rows[i]
		src := bySeq[row.SourceSeq]
		raw, err := s.store.OpenTeamEventPayload(ctx, teamID, row.SourceSeq)
		if err != nil {
			return nil, boundaryError("open the channel payload", err)
		}
		out = append(out, ChannelLineView{
			ChannelSeq:      row.ChannelSeq,
			Kind:            row.Kind,
			SenderKind:      row.SenderKind,
			SenderTaskID:    row.SenderTaskID,
			RecipientTaskID: row.RecipientTaskID,
			ThreadRoot:      row.ThreadRoot,
			Attempt:         channelAttemptOf(row, src),
			SourceSeq:       row.SourceSeq,
			CreatedAt:       row.CreatedAt,
			Content:         channelContentText(row, src, raw),
		})
	}
	return out, nil
}

// pageChannelGate fails the page closed while the vault is locked (§5.3).
func (s *TasksService) pageChannelGate() error {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.Unavailable, "the vault is locked; channel content is unavailable")
	}
	return nil
}

// pageChannelQuery validates the paging arguments and resolves the thread
// scope: without thread_task_id the general channel, with it that task's
// thread (§5.3).
func (s *TasksService) pageChannelQuery(ctx context.Context, teamID, threadTaskID string, before, since int64, limit int) (vendorstore.TeamChannelPageQuery, error) {
	if teamID == "" {
		return vendorstore.TeamChannelPageQuery{}, errcode.New(errcode.TasksTeamNotFound, "team_id is required")
	}
	if before < 0 || since < 0 {
		return vendorstore.TeamChannelPageQuery{}, errcode.New(errcode.InvalidArgument, "before and since must be non-negative")
	}
	if before > 0 && since > 0 && since >= before {
		return vendorstore.TeamChannelPageQuery{}, errcode.New(errcode.InvalidArgument, "since must be below before")
	}
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	q := vendorstore.TeamChannelPageQuery{Before: before, Since: since, Limit: limit}
	_, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return vendorstore.TeamChannelPageQuery{}, s.teamReadError(err)
	}
	if threadTaskID == "" {
		return q, nil
	}
	if _, ok := findTask(tasks, threadTaskID); !ok {
		return vendorstore.TeamChannelPageQuery{}, errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
	}
	q.ThreadRoots = []string{vendorstore.ThreadRootTaskID(threadTaskID)}
	return q, nil
}

// sourceEvents maps the team's event sequences to their metadata, so a page
// can name the attempt and the failure code of its lines.
func (s *TasksService) sourceEvents(ctx context.Context, teamID string) (map[int64]*vendorstore.RecordedTeamEvent, error) {
	events, err := s.store.TeamEventsSince(ctx, teamID, 0, teamEventScanLimit)
	if err != nil {
		return nil, boundaryError("read the team events", err)
	}
	bySeq := make(map[int64]*vendorstore.RecordedTeamEvent, len(events))
	for i := range events {
		bySeq[events[i].Sequence] = &events[i]
	}
	return bySeq, nil
}

// teamReadError maps store errors to the tasks codes.
func (s *TasksService) teamReadError(err error) error {
	if errors.Is(err, vendorstore.ErrTeamNotFound) {
		return errcode.New(errcode.TasksTeamNotFound, "the team does not exist")
	}
	return boundaryError("read the team", err)
}

// PostChannelMessage records one §6.7 user post and delivers it to the routed
// agent (§5.3). The channel line and the delegator's informational mention
// commit with the event; the delivery is an immediate origin=user turn.
func (s *TasksService) PostChannelMessage(ctx context.Context, cmd *PostChannelCommand) (PostChannelResult, error) {
	trimmed, fingerprint, err := s.validatePost(cmd)
	if err != nil {
		return PostChannelResult{}, err
	}
	if seq, cached := s.cachedPost(cmd.CommandID, fingerprint); cached {
		return PostChannelResult{ChannelSeq: seq}, nil
	}
	if s.commandKnown(cmd.CommandID) {
		// The command_id was already used for different arguments (appstate
		// pattern): the replay must not attach to the original operation.
		return PostChannelResult{}, errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
	}
	rec, tasks, err := s.postTeamGate(ctx, cmd.TeamID)
	if err != nil {
		return PostChannelResult{}, err
	}
	target, err := s.routeUserPost(tasks, cmd.ThreadTaskID, cmd.ToTaskID)
	if err != nil {
		return PostChannelResult{}, err
	}
	info, err := s.postSessionGate(ctx, target)
	if err != nil {
		return PostChannelResult{}, err
	}
	result, err := s.appendUserPost(ctx, cmd.CommandID, fingerprint, &rec, tasks, target, trimmed)
	if err != nil {
		return PostChannelResult{}, err
	}
	// §6.2: the delegator's mention rides the next digest (informational);
	// the routed agent gets the text at once, origin user, no pacing.
	s.deliverUserPost(ctx, target.SessionID, trimmed, info.State == vendorstore.StateRunning)
	return result, nil
}

// validatePost checks the command shape and the §5.3 text cap, and computes
// the command fingerprint for the dedup.
func (s *TasksService) validatePost(cmd *PostChannelCommand) (string, string, error) {
	if cmd.CommandID == "" {
		return "", "", errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if cmd.TeamID == "" {
		return "", "", errcode.New(errcode.TasksTeamNotFound, "team_id is required")
	}
	trimmed := strings.TrimSpace(cmd.Text)
	if trimmed == "" {
		return "", "", errcode.New(errcode.InvalidArgument, "text must not be empty")
	}
	if len(trimmed) > maxChannelPostBytes {
		return "", "", errcode.New(errcode.TasksPostTooLarge, "text exceeds the 4 KiB limit")
	}
	return trimmed, channelPostFingerprint(cmd.TeamID, cmd.ThreadTaskID, cmd.ToTaskID, trimmed), nil
}

// postTeamGate reads the team and rejects posting once it ended (§5.3).
func (s *TasksService) postTeamGate(ctx context.Context, teamID string) (vendorstore.TeamRecord, []vendorstore.TaskRecord, error) {
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		if errors.Is(err, vendorstore.ErrTeamNotFound) {
			return vendorstore.TeamRecord{}, nil, errcode.New(errcode.TasksTeamNotFound, "the team does not exist")
		}
		return vendorstore.TeamRecord{}, nil, boundaryError("read the team", err)
	}
	if isHostTeamTerminal(rec.State) {
		return vendorstore.TeamRecord{}, nil, errcode.New(errcode.TasksTeamEnded, "the team has ended; its channel is read-only")
	}
	return rec, tasks, nil
}

// postSessionGate refuses a post whose target cannot take input yet, and
// reports whether the session is mid-turn (steer instead of send).
func (s *TasksService) postSessionGate(ctx context.Context, target *vendorstore.TaskRecord) (vendorstore.Session, error) {
	if target.SessionID == "" {
		return vendorstore.Session{}, errcode.New(errcode.FailedPrecondition, "the task has no live session yet")
	}
	info, err := s.store.Snapshot(ctx, target.SessionID)
	if err != nil || (info.State != vendorstore.StateReady && info.State != vendorstore.StateRunning) {
		return vendorstore.Session{}, errcode.New(errcode.FailedPrecondition, "the task session is not taking input")
	}
	return info, nil
}

// appendUserPost records the TeamUserPosted event with its delegator mention,
// caches the command result and resolves the new line's `#n`.
func (s *TasksService) appendUserPost(ctx context.Context, commandID, fingerprint string, rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord, target *vendorstore.TaskRecord, trimmed string) (PostChannelResult, error) {
	payload, encErr := json.Marshal(teamUserPostedPayload{ToTaskID: target.TaskID, Message: trimmed})
	if encErr != nil {
		return PostChannelResult{}, errcode.Wrap(errcode.Internal, "encode the user post", encErr)
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return PostChannelResult{}, errcode.Wrap(errcode.Internal, "generate the user post event ID", idErr)
	}
	evSeq, appendErr := s.store.AppendTeamEvent(ctx, rec.TeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, EventType: vendorstore.EventTeamUserPosted, ProtectedPayload: payload,
		Mailbox: userPostMentionRows(tasks, target, trimmed),
	})
	if appendErr != nil {
		if errors.Is(appendErr, vendorstore.ErrTeamNotFound) {
			return PostChannelResult{}, errcode.New(errcode.TasksTeamNotFound, "the team does not exist")
		}
		return PostChannelResult{}, boundaryError("record the user post", appendErr)
	}
	s.rememberPost(commandID, fingerprint, 0)
	lines, err := s.store.TeamChannelLinesForSources(ctx, rec.TeamID, []int64{evSeq})
	if err != nil {
		return PostChannelResult{}, boundaryError("read the posted line", err)
	}
	seq := int64(0)
	for _, sourceLines := range lines {
		for i := range sourceLines {
			if sourceLines[i].SenderKind == vendorstore.ChannelSenderUser && sourceLines[i].ChannelSeq > seq {
				seq = sourceLines[i].ChannelSeq
			}
		}
	}
	s.rememberPost(commandID, fingerprint, seq)
	return PostChannelResult{ChannelSeq: seq}, nil
}

// routeUserPost resolves the §5.3 routing: to_task_id when given, else the
// thread's task agent, else the lead.
func (s *TasksService) routeUserPost(tasks []vendorstore.TaskRecord, threadTaskID, toTaskID string) (*vendorstore.TaskRecord, error) {
	switch {
	case toTaskID != "":
		target, ok := findTask(tasks, toTaskID)
		if !ok {
			return nil, errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
		}
		return target, nil
	case threadTaskID != "":
		target, ok := findTask(tasks, threadTaskID)
		if !ok {
			return nil, errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
		}
		return target, nil
	default:
		for i := range tasks {
			if tasks[i].HierarchyRole == vendorstore.RoleLead {
				return &tasks[i], nil
			}
		}
		return nil, errcode.New(errcode.TasksTaskNotFound, "the team has no lead task")
	}
}

// userPostMentionRows seals the delegator's informational mention (§6.2): it
// rides the delegator's next digest without a separate wake (Q-177 A).
func userPostMentionRows(tasks []vendorstore.TaskRecord, target *vendorstore.TaskRecord, text string) []vendorstore.MailboxEnqueueInput {
	delegator := resolvedParentID(target.ParentTaskID, tasks)
	if delegator == "" || delegator == target.TaskID {
		return nil
	}
	payload, err := json.Marshal(mailboxDigestItem{TaskID: vendorstore.ChannelSenderUser, State: mailboxStateMessage, Summary: text})
	if err != nil {
		return nil
	}
	return []vendorstore.MailboxEnqueueInput{
		{RecipientTaskID: delegator, Kind: vendorstore.MailboxKindSend, Payload: payload, Informational: true},
	}
}

// deliverUserPost sends the text as one origin=user turn: the pacer only
// queues fugue-origin prompts, so this goes immediately (§6.1, §5.3). A busy
// session takes the prompt into its running turn (steer, §6.2).
func (s *TasksService) deliverUserPost(ctx context.Context, sessionID, text string, running bool) {
	if s.sessions == nil {
		return
	}
	input := vendor.Input{Text: text, Origin: vendor.OriginUser}
	var err error
	if running {
		err = s.sessions.Steer(ctx, sessionID, input)
	} else {
		err = s.sessions.Send(ctx, sessionID, input)
	}
	if err != nil {
		// The post is committed; the delivery failure surfaces to the caller
		// through the channel only. Never log the text.
		slog.WarnContext(ctx, "delivering the user post failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

// channelPostFingerprint binds a command_id to its arguments, so a replayed
// command_id with different arguments fails instead of returning the cached
// result (appstate pattern).
func channelPostFingerprint(teamID, threadTaskID, toTaskID, text string) string {
	h := sha256.New()
	var lenBuf [20]byte
	for _, part := range []string{teamID, threadTaskID, toTaskID, text} {
		// sha256.Hash.Write never fails; the length prefix keeps the parts
		// distinct, so no separator can collide with the text.
		_, _ = h.Write(strconv.AppendInt(lenBuf[:0], int64(len(part)), 10))
		_, _ = h.Write([]byte(":"))
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

const tasksDedupCapacity = 1024

// cachedPost returns the cached result of a command_id, failing typed when
// the same command_id was used for different arguments.
func (s *TasksService) cachedPost(commandID, fingerprint string) (int64, bool) {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	entry, ok := s.dedup[commandID]
	if !ok {
		return 0, false
	}
	if entry.fingerprint != fingerprint {
		return 0, false
	}
	return entry.seq, entry.seq > 0
}

// commandKnown reports a command_id the service already recorded, whatever
// its fingerprint was.
func (s *TasksService) commandKnown(commandID string) bool {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	_, ok := s.dedup[commandID]
	return ok
}

// rememberPost records a command_id with its fingerprint and result, evicting
// the oldest entry beyond the capacity.
func (s *TasksService) rememberPost(commandID, fingerprint string, seq int64) {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	if _, ok := s.dedup[commandID]; !ok {
		s.dedupOrder = append(s.dedupOrder, commandID)
		for len(s.dedupOrder) > tasksDedupCapacity {
			oldest := s.dedupOrder[0]
			s.dedupOrder = s.dedupOrder[1:]
			delete(s.dedup, oldest)
		}
	}
	s.dedup[commandID] = tasksDedupEntry{seq: seq, fingerprint: fingerprint}
}

// isHostTeamTerminal reports a terminal team state for the post gate.
func isHostTeamTerminal(state vendorstore.TeamState) bool {
	return state == vendorstore.TeamStateDone || state == vendorstore.TeamStateCancelled ||
		state == vendorstore.TeamStateFailed
}
