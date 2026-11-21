package app

// F11.10a team channel read path (orchestration v0.3 §6.7, Q-176 A, Q-178 A):
// fugue_team_read serves the lines an agent may see for its role (§6.6),
// newest first, with backward and forward paging and a 16 KiB page budget.
// Model-authored content opens from the sealed source payloads and renders as
// escaped untrusted blocks (OR-13); reads never wake anyone and never touch
// the channel stream — agents have no subscription to it.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// The §6.7 read selectors: no thread reads the caller's own thread,
// "general" the general channel, "all" (lead only) the whole team.
const (
	teamReadGeneralSelector = "general"
	teamReadAllSelector     = "all"
	maxTeamReadBytes        = 16 << 10
	defaultTeamReadLines    = 50
	maxTeamReadLines        = 200
)

// channelScope is the §6.6 read scope of one caller: the lead sees the whole
// team; a team manager its own thread and its part; a member its thread, the
// sibling threads of the same direct delegator, and the general channel.
type channelScope struct {
	all     bool
	general bool
	roots   map[string]struct{}
}

func (c *callerTeamContext) channelScope() channelScope {
	switch c.role {
	case vendorstore.RoleLead:
		return channelScope{all: true, general: true, roots: map[string]struct{}{}}
	case vendorstore.RoleTeamManager:
		return channelScope{general: true, roots: delegationRoots(c)}
	case vendorstore.RoleMember:
		return channelScope{general: true, roots: siblingRoots(c)}
	case vendorstore.RoleSolo:
		// A solo team has one task and no channel partners (§6.6 lists lead,
		// manager and member scopes only).
		return channelScope{roots: map[string]struct{}{}}
	default:
		return channelScope{roots: map[string]struct{}{}}
	}
}

// delegationRoots resolves the manager's part: its own thread and its direct
// children's threads across their retry chains.
func delegationRoots(c *callerTeamContext) map[string]struct{} {
	byID, retryChild := resolveTaskChain(c.tasks)
	roots := map[string]struct{}{vendorstore.ThreadRootTaskID(c.caller.TaskID): {}}
	for i := range c.tasks {
		if sameAttemptChain(c.tasks[i].ParentTaskID, c.caller.TaskID, byID, retryChild) {
			roots[vendorstore.ThreadRootTaskID(c.tasks[i].TaskID)] = struct{}{}
		}
	}
	return roots
}

// siblingRoots resolves the member's scope: every task sharing its direct
// delegator, itself included (§6.6).
func siblingRoots(c *callerTeamContext) map[string]struct{} {
	byID, retryChild := resolveTaskChain(c.tasks)
	roots := map[string]struct{}{}
	for i := range c.tasks {
		if sameAttemptChain(c.tasks[i].ParentTaskID, c.caller.ParentTaskID, byID, retryChild) {
			roots[vendorstore.ThreadRootTaskID(c.tasks[i].TaskID)] = struct{}{}
		}
	}
	return roots
}

// inScope reports whether a thread root is readable.
func (sc channelScope) inScope(root string) bool {
	if sc.all {
		return true
	}
	_, ok := sc.roots[root]
	return ok
}

// validateTeamReadArgs checks the paging arguments against the §6.7 read.
func validateTeamReadArgs(role vendorstore.TeamRole, before, since int64, limit int) error {
	switch role {
	case vendorstore.RoleLead, vendorstore.RoleTeamManager, vendorstore.RoleMember:
	case vendorstore.RoleSolo:
		return errcode.New(errcode.PermissionDenied, "fugue_team_read is a team role tool (§6.6)")
	default:
		return errcode.New(errcode.PermissionDenied, "fugue_team_read is a team role tool (§6.6)")
	}
	if before < 0 || since < 0 {
		return errcode.New(errcode.InvalidArgument, "before and since must be non-negative")
	}
	if before > 0 && since > 0 && since >= before {
		return errcode.New(errcode.InvalidArgument, "since must be below before")
	}
	if limit > maxTeamReadLines {
		return errcode.New(errcode.InvalidArgument, "limit exceeds 200")
	}
	return nil
}

// teamReadQuery scopes the query to the requested selector: the caller's own
// thread by default, the general channel, the whole team (lead only), or one
// in-scope task thread.
func teamReadQuery(c *callerTeamContext, thread string) (vendorstore.TeamChannelPageQuery, error) {
	scope := c.channelScope()
	q := vendorstore.TeamChannelPageQuery{}
	switch thread {
	case "":
		if c.caller == nil {
			return q, errcode.New(errcode.PermissionDenied, "the caller task is not in the team")
		}
		root := vendorstore.ThreadRootTaskID(c.caller.TaskID)
		if !scope.inScope(root) {
			return q, errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.ThreadRoots = []string{root}
	case teamReadGeneralSelector:
		if !scope.general {
			return q, errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.GeneralOnly = true
	case teamReadAllSelector:
		if !scope.all {
			return q, errcode.New(errcode.PermissionDenied, "only the lead may read the whole team channel")
		}
	default:
		if err := teams.ValidateDurableTaskID("thread", thread); err != nil {
			return q, errcode.New(errcode.InvalidArgument, err.Error())
		}
		if _, ok := findTask(c.tasks, thread); !ok {
			return q, errcode.New(errcode.NotFound, "team task not found")
		}
		root := vendorstore.ThreadRootTaskID(thread)
		if !scope.inScope(root) {
			return q, errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.ThreadRoots = []string{root}
	}
	return q, nil
}

// TeamRead pages the channel lines of one scope (§6.7 fugue_team_read).
func (s *teamTaskService) TeamRead(ctx context.Context, sessionID, thread string, before, since int64, limit int) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if argErr := validateTeamReadArgs(c.role, before, since, limit); argErr != nil {
		return "", argErr
	}
	if limit <= 0 {
		limit = defaultTeamReadLines
	}
	q, err := teamReadQuery(c, thread)
	if err != nil {
		return "", err
	}
	q.Before, q.Since, q.Limit = before, since, limit
	lines, err := s.store.TeamChannelMessages(ctx, c.team.TeamID, q)
	if err != nil {
		return "", boundaryError("read the team channel", err)
	}
	nonce, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate the channel read nonce", err)
	}
	return s.renderChannelLines(ctx, c, lines, nonce)
}

// renderChannelLines renders one page under the 16 KiB budget: the host
// metadata first, every model-authored piece inside an escaped untrusted
// block bound by the nonce (OR-13).
func (s *teamTaskService) renderChannelLines(ctx context.Context, c *callerTeamContext, lines []vendorstore.TeamChannelMessage, nonce string) (string, error) {
	events, err := s.store.TeamEventsSince(ctx, c.team.TeamID, 0, teamEventScanLimit)
	if err != nil {
		return "", boundaryError("read the team events", err)
	}
	bySeq := make(map[int64]*vendorstore.RecordedTeamEvent, len(events))
	for i := range events {
		bySeq[events[i].Sequence] = &events[i]
	}
	safeNonce := teams.EscapeDigestText(nonce)
	var b strings.Builder
	budget := maxTeamReadBytes
	shown := 0
	for i := range lines {
		text, textErr := s.channelLineText(ctx, c, &lines[i], bySeq[lines[i].SourceSeq], safeNonce)
		if textErr != nil {
			return "", textErr
		}
		if len(text)+2 > budget {
			break
		}
		budget -= len(text) + 2
		b.WriteString(text)
		b.WriteString("\n")
		shown++
	}
	if shown == 0 {
		return "No channel lines in this scope.", nil
	}
	return b.String(), nil
}

// channelLineText renders one line: the host metadata parts, then the payload
// content of its source event as data.
func (s *teamTaskService) channelLineText(ctx context.Context, c *callerTeamContext, line *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent, safeNonce string) (string, error) {
	parts := channelHeadParts(line, src)
	raw, err := s.store.OpenTeamEventPayload(ctx, c.team.TeamID, line.SourceSeq)
	if err != nil {
		// §6.7 content is sealed; a locked vault fails closed (Q-175 A).
		return "", boundaryError("open the channel payload", err)
	}
	if len(raw) == 0 {
		// The source event carries no payload: the line renders from its
		// metadata alone (assignment, failure, attempt-open lines).
		return strings.Join(parts, "\n"), nil
	}
	label := fmt.Sprintf("kind=%s nonce=%s", line.Kind, safeNonce)
	parts = append(parts, channelPayloadParts(line.Kind, raw, label)...)
	return strings.Join(parts, "\n"), nil
}

// channelHeadParts renders the metadata of one line: the number, kind, sender
// and recipient header, the failure code, and the task lifecycle text
// (delegation, attempt-open). All of it is Fugue's own.
func channelHeadParts(line *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent) []string {
	parts := []string{channelHeadText(line)}
	switch line.Kind {
	case vendorstore.ChannelKindFailed:
		if src != nil && src.FailureCode != "" {
			parts = append(parts, "failed("+src.FailureCode+")")
		}
	case vendorstore.ChannelKindTask:
		parts = append(parts, channelTaskLineParts(line, src)...)
	}
	return parts
}

// channelHeadText renders the header of one line.
func channelHeadText(line *vendorstore.TeamChannelMessage) string {
	var head strings.Builder
	fmt.Fprintf(&head, "#%d %s", line.ChannelSeq, line.Kind)
	switch line.SenderKind {
	case vendorstore.ChannelSenderAgent:
		fmt.Fprintf(&head, " from task %s", teams.EscapeDigestText(line.SenderTaskID))
	case vendorstore.ChannelSenderUser:
		head.WriteString(" from the user")
	case vendorstore.ChannelSenderFugue:
		head.WriteString(" from fugue")
	case vendorstore.ChannelSenderWatchdog:
		head.WriteString(" from the watchdog")
	}
	if line.RecipientTaskID != "" {
		fmt.Fprintf(&head, " to task %s", teams.EscapeDigestText(line.RecipientTaskID))
	}
	return head.String()
}

// channelTaskLineParts renders the `task` kind: a delegation line for
// TaskAssigned, the attempt-open line for TaskSuperseded (§5.4, §6.7: every
// new attempt opens its shared thread with the attempt number and the
// previous failure code).
func channelTaskLineParts(line *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent) []string {
	if src == nil {
		return []string{"task " + teams.EscapeDigestText(line.ThreadRoot) + " delegated"}
	}
	if src.EventType == vendorstore.EventTaskSuperseded {
		text := fmt.Sprintf("attempt %d begins after failed(%s)", retryAttemptAfter(src.TaskID), src.FailureCode)
		return []string{"task " + teams.EscapeDigestText(src.TaskID) + ":", text}
	}
	id := src.TaskID
	if id == "" {
		id = line.ThreadRoot
	}
	return []string{"task " + teams.EscapeDigestText(id) + " delegated"}
}

// retryAttemptAfter derives the attempt number that opens when taskID's
// chain supersedes: `<root>` is attempt 1, `<root>~r<k>` attempt k+1 (Q-126).
func retryAttemptAfter(taskID string) int {
	root := vendorstore.ThreadRootTaskID(taskID)
	if rest := taskID[len(root):]; strings.HasPrefix(rest, "~r") {
		if n, err := strconv.Atoi(rest[2:]); err == nil {
			return n + 2
		}
	}
	return 2
}

// channelBlock renders one piece of untrusted content as an escaped block
// bound by the read's nonce (OR-13, CS-06).
func channelBlock(label, text string) string {
	var b strings.Builder
	b.WriteString("⟪untrusted source=team-channel " + label + "⟫\n")
	b.WriteString(teams.EscapeDigestText(text))
	b.WriteString("\n⟪/untrusted " + label + "⟫")
	return b.String()
}

// channelAttemptOf derives the attempt a line belongs to: the attempt of the
// task its source event names (Q-126: <root> is attempt 1, <root>~r<k> k+1).
func channelAttemptOf(row *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent) int {
	taskID := row.ThreadRoot
	if src != nil && src.TaskID != "" {
		taskID = src.TaskID
	}
	root := vendorstore.ThreadRootTaskID(taskID)
	if rest := taskID[len(root):]; strings.HasPrefix(rest, "~r") {
		if n, err := strconv.Atoi(rest[2:]); err == nil {
			return n + 1
		}
	}
	return 1
}

// channelContentText extracts one line's content from its sealed source
// payload: the primary text of the kind, host metadata for the lifecycle
// kinds, and nothing for a payload-less event.
func channelContentText(row *vendorstore.TeamChannelMessage, src *vendorstore.RecordedTeamEvent, raw []byte) string {
	if len(raw) == 0 {
		if row.Kind == vendorstore.ChannelKindFailed && src != nil && src.FailureCode != "" {
			return "failed(" + src.FailureCode + ")"
		}
		if row.Kind == vendorstore.ChannelKindTask && src != nil && src.EventType == vendorstore.EventTaskSuperseded {
			return fmt.Sprintf("attempt %d begins after failed(%s)", retryAttemptAfter(src.TaskID), src.FailureCode)
		}
		return ""
	}
	switch row.Kind {
	case vendorstore.ChannelKindPlan:
		var p teams.ProposalInput
		if json.Unmarshal(raw, &p) == nil {
			return p.Brief
		}
	case vendorstore.ChannelKindDecision:
		var d teamDecisionPayload
		if json.Unmarshal(raw, &d) == nil && d.Decision != "" {
			return d.Decision
		}
		var a teamAmendPayload
		if json.Unmarshal(raw, &a) == nil {
			return "the team was amended"
		}
	case vendorstore.ChannelKindNote:
		var p teamNotePayload
		if json.Unmarshal(raw, &p) == nil {
			return p.Note
		}
	case vendorstore.ChannelKindQuestion:
		var p taskQuestionPayload
		if json.Unmarshal(raw, &p) == nil {
			return p.Question
		}
	case vendorstore.ChannelKindAnswer:
		var p struct {
			Answer string `json:"answer"`
		}
		if json.Unmarshal(raw, &p) == nil {
			return p.Answer
		}
	case vendorstore.ChannelKindResult:
		var p taskReportPayload
		if json.Unmarshal(raw, &p) == nil {
			return p.Summary
		}
	case vendorstore.ChannelKindSend:
		var m taskMessagePayload
		if json.Unmarshal(raw, &m) == nil {
			return m.Message
		}
		var u teamUserPostedPayload
		if json.Unmarshal(raw, &u) == nil {
			return u.Message
		}
	case vendorstore.ChannelKindStall:
		var p taskStalledPayload
		if json.Unmarshal(raw, &p) == nil {
			return fmt.Sprintf("silent for %ds waiting_on=%s", p.SilentSeconds, p.WaitingOn)
		}
	case vendorstore.ChannelKindApproval:
		var p taskApprovalRefPayload
		if json.Unmarshal(raw, &p) == nil {
			ref := "approval request " + p.RequestID + " (" + p.Tool + ")"
			if p.Outcome != "" {
				ref += ": " + p.Outcome
			}
			return ref
		}
	}
	return string(raw)
}
