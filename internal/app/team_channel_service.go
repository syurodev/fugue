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
		byID, retryChild := resolveTaskChain(c.tasks)
		roots := map[string]struct{}{vendorstore.ThreadRootTaskID(c.caller.TaskID): {}}
		for i := range c.tasks {
			if sameAttemptChain(c.tasks[i].ParentTaskID, c.caller.TaskID, byID, retryChild) {
				roots[vendorstore.ThreadRootTaskID(c.tasks[i].TaskID)] = struct{}{}
			}
		}
		return channelScope{general: true, roots: roots}
	case vendorstore.RoleMember:
		byID, retryChild := resolveTaskChain(c.tasks)
		roots := map[string]struct{}{}
		for i := range c.tasks {
			if sameAttemptChain(c.tasks[i].ParentTaskID, c.caller.ParentTaskID, byID, retryChild) {
				roots[vendorstore.ThreadRootTaskID(c.tasks[i].TaskID)] = struct{}{}
			}
		}
		return channelScope{general: true, roots: roots}
	default:
		return channelScope{roots: map[string]struct{}{}}
	}
}

// inScope reports whether a thread root is readable.
func (sc channelScope) inScope(root string) bool {
	if sc.all {
		return true
	}
	_, ok := sc.roots[root]
	return ok
}

// TeamRead pages the channel lines of one scope (§6.7 fugue_team_read).
func (s *teamTaskService) TeamRead(ctx context.Context, sessionID, thread string, before, since int64, limit int) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	switch c.role {
	case vendorstore.RoleLead, vendorstore.RoleTeamManager, vendorstore.RoleMember:
	default:
		return "", errcode.New(errcode.PermissionDenied, "fugue_team_read is a team role tool (§6.6)")
	}
	if before < 0 || since < 0 {
		return "", errcode.New(errcode.InvalidArgument, "before and since must be non-negative")
	}
	if before > 0 && since > 0 && since >= before {
		return "", errcode.New(errcode.InvalidArgument, "since must be below before")
	}
	if limit <= 0 {
		limit = defaultTeamReadLines
	}
	if limit > maxTeamReadLines {
		limit = maxTeamReadLines
	}
	scope := c.channelScope()
	q := vendorstore.TeamChannelPageQuery{Before: before, Since: since, Limit: limit}
	switch {
	case thread == "":
		if c.caller == nil {
			return "", errcode.New(errcode.PermissionDenied, "the caller task is not in the team")
		}
		root := vendorstore.ThreadRootTaskID(c.caller.TaskID)
		if !scope.inScope(root) {
			return "", errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.ThreadRoots = []string{root}
	case thread == teamReadGeneralSelector:
		if !scope.general {
			return "", errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.GeneralOnly = true
	case thread == teamReadAllSelector:
		if !scope.all {
			return "", errcode.New(errcode.PermissionDenied, "only the lead may read the whole team channel")
		}
	default:
		if err := teams.ValidateDurableTaskID("thread", thread); err != nil {
			return "", errcode.New(errcode.InvalidArgument, err.Error())
		}
		if _, ok := findTask(c.tasks, thread); !ok {
			return "", errcode.New(errcode.NotFound, "team task not found")
		}
		root := vendorstore.ThreadRootTaskID(thread)
		if !scope.inScope(root) {
			return "", errcode.New(errcode.PermissionDenied, "thread is outside your read scope")
		}
		q.ThreadRoots = []string{root}
	}
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
	bySeq := make(map[int64]vendorstore.RecordedTeamEvent, len(events))
	for i := range events {
		bySeq[events[i].Sequence] = events[i]
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

// channelLineText renders one line: host metadata, then the payload content
// of its source event as data.
func (s *teamTaskService) channelLineText(ctx context.Context, c *callerTeamContext, line *vendorstore.TeamChannelMessage, src vendorstore.RecordedTeamEvent, safeNonce string) (string, error) {
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
	parts := []string{head.String()}
	// Metadata-only parts come first: the failure code and the task
	// lifecycle lines (delegation, attempt-open) are Fugue's own and render
	// even when their source event carries no payload.
	switch line.Kind {
	case vendorstore.ChannelKindFailed:
		if src.FailureCode != "" {
			parts = append(parts, "failed("+src.FailureCode+")")
		}
	case vendorstore.ChannelKindTask:
		parts = append(parts, channelTaskLineText(c, src)...)
	}

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
	switch line.Kind {
	case vendorstore.ChannelKindPlan:
		parts = append(parts, channelPlanText(raw, label, safeNonce)...)
	case vendorstore.ChannelKindDecision:
		parts = append(parts, channelDecisionText(raw, label)...)
	case vendorstore.ChannelKindNote:
		var p teamNotePayload
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, channelBlock(label, p.Note))
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindQuestion:
		var p taskQuestionPayload
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, "to "+p.To)
			parts = append(parts, channelBlock(label, p.Question))
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindAnswer:
		var p struct {
			Answer     string `json:"answer"`
			AnsweredBy string `json:"answered_by"`
		}
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, channelBlock(label, p.Answer))
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindResult:
		var p taskReportPayload
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, "status "+p.Status)
			if p.Summary != "" {
				parts = append(parts, channelBlock(label, p.Summary))
			}
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindSend:
		var p taskMessagePayload
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, "to task "+teams.EscapeDigestText(p.ToTaskID))
			parts = append(parts, channelBlock(label, p.Message))
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindStall:
		var p taskStalledPayload
		if json.Unmarshal(raw, &p) == nil {
			parts = append(parts, fmt.Sprintf("silent for %ds waiting_on=%s", p.SilentSeconds, p.WaitingOn))
			if p.TeamWide {
				parts = append(parts, "team-wide: every task is waiting")
			}
		} else {
			parts = append(parts, channelBlock(label, string(raw)))
		}
	case vendorstore.ChannelKindApproval:
		// F11.10b adds the approval events; the reference line renders from
		// its event metadata alone.
		parts = append(parts, channelBlock(label, string(raw)))
	}
	return strings.Join(parts, "\n"), nil
}

// channelPlanText renders the proposal plan: the title is host metadata, the
// brief and every task goal are model output in blocks.
func channelPlanText(raw []byte, label, safeNonce string) []string {
	var p teams.ProposalInput
	if json.Unmarshal(raw, &p) != nil {
		return []string{channelBlock(label, string(raw))}
	}
	parts := []string{"Plan proposal: " + p.Title}
	if p.Brief != "" {
		parts = append(parts, channelBlock(label, p.Brief))
	}
	for i := range p.Tasks {
		t := &p.Tasks[i]
		parts = append(parts, "task "+teams.EscapeDigestText(t.ID)+":")
		parts = append(parts, channelBlock(label, t.Goal))
	}
	return parts
}

// channelDecisionText renders a decision line: the decision text is the
// user's own, role edits are host-applied, and unknown payloads render as one
// escaped block.
func channelDecisionText(raw []byte, label string) []string {
	var d teamDecisionPayload
	if err := json.Unmarshal(raw, &d); err == nil && d.Decision != "" {
		parts := []string{"The user decided \"" + d.Decision + "\"."}
		if d.DecidedBy != "" {
			parts = append(parts, "Decided by: "+d.DecidedBy+".")
		}
		return parts
	}
	var a teamAmendPayload
	if err := json.Unmarshal(raw, &a); err == nil && (len(a.RoleEdits) > 0 || len(a.AddedTaskIDs) > 0 || a.Budget != nil) {
		return []string{"The user amended the team."}
	}
	return []string{channelBlock(label, string(raw))}
}

// channelTaskLineText renders the `task` kind: a delegation line for
// TaskAssigned, the attempt-open line for TaskSuperseded (§5.4, §6.7: every
// new attempt opens its shared thread with the attempt number and the
// previous failure code).
func channelTaskLineText(c *callerTeamContext, src vendorstore.RecordedTeamEvent) []string {
	if src.EventType == vendorstore.EventTaskSuperseded {
		next := 2
		if row, ok := findTask(c.tasks, src.TaskID); ok {
			next = row.Attempt + 1
		}
		text := fmt.Sprintf("attempt %d begins after failed(%s)", next, src.FailureCode)
		return []string{"task " + teams.EscapeDigestText(src.TaskID) + ":", text}
	}
	return []string{"task " + teams.EscapeDigestText(src.TaskID) + " delegated"}
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
