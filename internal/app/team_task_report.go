package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// ResultGate evaluates a reported task's checks before it moves from checking
// to review (Q-125). F11.5 wires the real gate; F11.3 fails closed unless a
// gate is injected.
type ResultGate interface {
	Evaluate(ctx context.Context, teamID, taskID string) (ResultGateVerdict, error)
}

// ResultGateVerdict is the outcome of a ResultGate check pass.
type ResultGateVerdict struct {
	Ready       bool
	Passed      bool
	FailureCode string
	Summary     string
}

type unconfiguredResultGate struct{}

func (unconfiguredResultGate) Evaluate(context.Context, string, string) (ResultGateVerdict, error) {
	return ResultGateVerdict{Ready: false, Summary: "no gate configured"}, nil
}

type taskReportPayload struct {
	Status           string          `json:"status"`
	Summary          string          `json:"summary"`
	SummaryTruncated bool            `json:"summary_truncated,omitempty"`
	Output           json.RawMessage `json:"output,omitempty"`
	SchemaValid      bool            `json:"schema_valid"`
	SchemaError      string          `json:"schema_error,omitempty"`
	CommitSHA        string          `json:"commit_sha,omitempty"`
}

const (
	taskStatusSucceeded = "succeeded"
	taskStatusFailed    = "failed"
	askTargetParent     = "parent"
	askTargetUser       = "user"
)

// Done records a task's report/output/diff and wakes its delegator once (Q-125, Q-127, Q-128).
func (s *teamTaskService) Done(ctx context.Context, sessionID, summary string, output json.RawMessage, status string) (string, error) {
	c, err := s.resolveReporter(ctx, sessionID, "fugue_task_done")
	if err != nil {
		return "", err
	}
	cutSummary, truncated, err := validateDoneArgs(summary, output, status)
	if err != nil {
		return "", err
	}
	if c.role == vendorstore.RoleTeamManager && hasNonTerminalChild(c.caller.TaskID, c.tasks) {
		return "", errcode.New(errcode.FailedPrecondition, "team manager children are not terminal")
	}
	if status == taskStatusFailed {
		return s.recordTaskFailedReport(ctx, c, cutSummary, truncated, output)
	}
	return s.recordTaskSucceededReport(ctx, c, cutSummary, truncated, output)
}

func (s *teamTaskService) resolveReporter(ctx context.Context, sessionID, toolName string) (*callerTeamContext, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if c.role != vendorstore.RoleTeamManager && c.role != vendorstore.RoleMember {
		return nil, errcode.New(errcode.PermissionDenied, "only a team manager or member may call "+toolName)
	}
	if c.caller == nil || c.caller.State != vendorstore.TaskStateRunning {
		return nil, errcode.New(errcode.FailedPrecondition, "task is not running")
	}
	return c, nil
}

func validateDoneArgs(summary string, output json.RawMessage, status string) (string, bool, error) {
	if status != taskStatusSucceeded && status != taskStatusFailed {
		return "", false, errcode.New(errcode.InvalidArgument, "status must be succeeded or failed")
	}
	if len(output) > teams.MaxReportOutputBytes {
		return "", false, errcode.New(errcode.InvalidArgument, "output exceeds 64 KiB limit")
	}
	cutSummary, truncated := truncateUTF8(strings.TrimSpace(summary), teams.MaxReportSummaryBytes)
	if cutSummary == "" {
		return "", false, errcode.New(errcode.InvalidArgument, "summary is required")
	}
	return cutSummary, truncated, nil
}

func hasNonTerminalChild(parentID string, tasks []vendorstore.TaskRecord) bool {
	for i := range tasks {
		if tasks[i].ParentTaskID == parentID && !teamTaskTerminal(tasks[i].State) {
			return true
		}
	}
	return false
}

func (s *teamTaskService) recordTaskFailedReport(ctx context.Context, c *callerTeamContext, summary string, truncated bool, output json.RawMessage) (string, error) {
	payload, err := json.Marshal(taskReportPayload{
		Status: "failed", Summary: summary, SummaryTruncated: truncated, Output: output, SchemaValid: true,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode failed report", err)
	}
	evID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate report event ID", err)
	}
	if _, err := s.store.AppendTeamEvent(ctx, c.team.TeamID, c.team.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: c.caller.TaskID, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateFailed), FailureCode: "task_failed", ProtectedPayload: payload,
		Mailbox: digestSummaryRows(c.caller.TaskID, c.caller.ParentTaskID, string(vendorstore.TaskStateFailed), "", summary, false),
	}); err != nil {
		return "", boundaryError("record failed task report", err)
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return fmt.Sprintf("task %s recorded as failed", c.caller.TaskID), nil
}

func (s *teamTaskService) recordTaskSucceededReport(ctx context.Context, c *callerTeamContext, summary string, truncated bool, output json.RawMessage) (string, error) {
	var commitSHA string
	if c.caller.Deliverable == vendorstore.DeliverableDiff || c.caller.Deliverable == vendorstore.DeliverableBoth {
		sha, commitErr := s.commitTaskWorktree(ctx, c)
		if commitErr != nil {
			return "", commitErr
		}
		commitSHA = sha
	}
	schemaValid, schemaErr := s.validateTaskOutput(ctx, c.team.TeamID, c.caller.TaskID, output)
	payload, err := json.Marshal(taskReportPayload{
		Status: "succeeded", Summary: summary, SummaryTruncated: truncated, Output: output,
		SchemaValid: schemaValid, SchemaError: schemaErr, CommitSHA: commitSHA,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode task report", err)
	}
	evID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate report event ID", err)
	}
	seq, err := s.store.AppendTeamEvent(ctx, c.team.TeamID, c.team.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: c.caller.TaskID, EventType: vendorstore.EventTaskReported, ProtectedPayload: payload,
		Mailbox: digestSummaryRows(c.caller.TaskID, c.caller.ParentTaskID, string(vendorstore.TaskStateChecking), "", summary, false),
	})
	if err != nil {
		return "", boundaryError("record task report", err)
	}
	if err := s.applyResultGate(ctx, c.team.TeamID, c.caller.TaskID, c.caller.ParentTaskID, seq); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return fmt.Sprintf("task %s reported (schema_valid=%t)", c.caller.TaskID, schemaValid), nil
}

func (s *teamTaskService) commitTaskWorktree(ctx context.Context, c *callerTeamContext) (string, error) {
	if s.git == nil || s.roots == nil {
		return "", errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	lead, err := s.store.Snapshot(ctx, c.team.SessionID)
	if err != nil {
		return "", boundaryError("read lead session", err)
	}
	root, err := s.roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return "", boundaryError("resolve workspace root", err)
	}
	wt, err := gitops.TaskWorktreePath(s.dataDir, repoIDFor(root), c.caller.TaskID)
	if err != nil {
		return "", boundaryError("derive task worktree", err)
	}
	res, err := s.git.CommitWorktree(ctx, wt, "fugue: "+c.caller.TaskID)
	if err != nil {
		return "", boundaryError("commit task worktree", err)
	}
	return res.SHA, nil
}

func (s *teamTaskService) validateTaskOutput(ctx context.Context, teamID, taskID string, output json.RawMessage) (bool, string) {
	raw, err := s.store.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil || !json.Valid(raw) {
		return true, ""
	}
	var spec teams.ProtectedTaskContent
	if json.Unmarshal(raw, &spec) != nil {
		return true, ""
	}
	return teams.ValidateOutputAgainstSchema(spec.OutputSchema, output)
}

func (s *teamTaskService) applyResultGate(ctx context.Context, teamID, taskID, parentTaskID string, seq int64) error {
	verdict, err := s.resultGate.Evaluate(ctx, teamID, taskID)
	if err != nil {
		return boundaryError("evaluate result gate", err)
	}
	if !verdict.Ready {
		return nil
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return errcode.Wrap(errcode.Internal, "generate check event ID", idErr)
	}
	ev := &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskCheckCompleted, Outcome: "passed",
	}
	if !verdict.Passed {
		ev.Outcome = string(vendorstore.TaskStateFailed)
		ev.FailureCode = verdict.FailureCode
		if ev.FailureCode == "" {
			ev.FailureCode = "checks_failed"
		}
		ev.Mailbox = digestSummaryRows(taskID, parentTaskID, string(vendorstore.TaskStateFailed), ev.FailureCode, "", false)
	}
	if _, err := s.store.AppendTeamEvent(ctx, teamID, seq, ev); err != nil {
		return boundaryError("record check result", err)
	}
	return nil
}

// Ask transitions a running task to asking and enqueues a question digest (Q-129).
func (s *teamTaskService) Ask(ctx context.Context, sessionID, question, to string) (string, error) {
	c, err := s.resolveReporter(ctx, sessionID, "fugue_task_ask")
	if err != nil {
		return "", err
	}
	q, target, err := validateAskArgs(question, to)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(taskQuestionPayload{Question: q, To: target})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode question payload", err)
	}
	evID, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate ask event ID", err)
	}
	var mail []vendorstore.MailboxEnqueueInput
	if target == askTargetParent {
		mail = digestSummaryRows(c.caller.TaskID, c.caller.ParentTaskID, string(vendorstore.TaskStateAsking), "", q, true)
	}
	if _, err := s.store.AppendTeamEvent(ctx, c.team.TeamID, c.team.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: c.caller.TaskID, EventType: vendorstore.EventTaskQuestionAsked,
		ProtectedPayload: payload, Mailbox: mail,
	}); err != nil {
		return "", boundaryError("record task question", err)
	}
	if s.wake != nil && len(mail) > 0 {
		s.wake.Notify()
	}
	return fmt.Sprintf("task %s is waiting for an answer from %s", c.caller.TaskID, target), nil
}

func validateAskArgs(question, to string) (string, string, error) {
	if to == "" {
		to = askTargetParent
	}
	if to != askTargetParent && to != askTargetUser {
		return "", "", errcode.New(errcode.InvalidArgument, "to must be parent or user")
	}
	q, _ := truncateUTF8(strings.TrimSpace(question), teams.MaxReportSummaryBytes)
	if q == "" {
		return "", "", errcode.New(errcode.InvalidArgument, "question is required")
	}
	return q, to, nil
}

// Result returns one paged part (summary, output, report, checks, diff) of a readable task (§6.3, Q-127).
func (s *teamTaskService) Result(ctx context.Context, sessionID, taskID, part string, offset, limit int) (string, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireReadableTask(taskID)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		return "", errcode.New(errcode.InvalidArgument, "offset must be non-negative")
	}
	if limit <= 0 || limit > maxResultLimit {
		limit = defaultResultLimit
	}
	switch part {
	case "checks":
		verdict, gateErr := s.resultGate.Evaluate(ctx, c.team.TeamID, target.TaskID)
		if gateErr != nil {
			return "", boundaryError("evaluate checks", gateErr)
		}
		if !verdict.Ready {
			return "not_run", nil
		}
		return fmt.Sprintf("passed=%t summary=%s", verdict.Passed, verdict.Summary), nil
	case "diff":
		return s.readTaskDiff(ctx, c, target, offset, limit)
	case "summary", "output", "report":
		return s.readTaskReportPart(ctx, c.team.TeamID, target.TaskID, part, offset, limit)
	default:
		return "", errcode.New(errcode.InvalidArgument, "part must be summary, output, report, checks, or diff")
	}
}

func (s *teamTaskService) readTaskDiff(ctx context.Context, c *callerTeamContext, target *vendorstore.TaskRecord, offset, limit int) (string, error) {
	if target.Deliverable == vendorstore.DeliverableReport {
		return "", errcode.New(errcode.FailedPrecondition, "report-only task has no diff")
	}
	if s.git == nil || s.roots == nil {
		return "", errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	lead, err := s.store.Snapshot(ctx, c.team.SessionID)
	if err != nil {
		return "", boundaryError("read lead session", err)
	}
	root, err := s.roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return "", boundaryError("resolve workspace root", err)
	}
	parentRef, err := parentBranch(target, c.tasks)
	if err != nil {
		return "", err
	}
	res, err := s.git.DiffAgainstParent(ctx, s.dataDir, repoIDFor(root), target.TaskID, parentRef, offset, limit)
	if err != nil {
		return "", boundaryError("read task diff", err)
	}
	nonce, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate diff nonce", err)
	}
	return teams.BuildDigest(fmt.Sprintf("diff offset=%d total=%d has_more=%t", res.Offset, res.TotalBytes, res.HasMore),
		[]teams.DigestEntry{{TaskID: target.TaskID, State: "diff", Summary: res.Text}}, nonce, maxResultLimit), nil
}

func (s *teamTaskService) readTaskReportPart(ctx context.Context, teamID, taskID, part string, offset, limit int) (string, error) {
	raw, found, err := s.store.OpenLatestTaskEventPayload(ctx, teamID, taskID, vendorstore.EventTaskReported)
	if err != nil {
		return "", boundaryError("open task report", err)
	}
	if !found {
		return "", errcode.New(errcode.NotFound, "task report not found")
	}
	var rep taskReportPayload
	if decErr := json.Unmarshal(raw, &rep); decErr != nil {
		return "", errcode.Wrap(errcode.Internal, "decode task report", decErr)
	}
	var text string
	switch part {
	case "summary", "report":
		text = rep.Summary
	case "output":
		text = string(rep.Output)
	}
	if offset < len(text) {
		text = text[offset:min(offset+limit, len(text))]
	} else {
		text = ""
	}
	nonce, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate report nonce", err)
	}
	header := fmt.Sprintf("part=%s schema_valid=%t", part, rep.SchemaValid)
	if rep.SchemaError != "" {
		header += " schema_error=" + rep.SchemaError
	}
	return teams.BuildDigest(header, []teams.DigestEntry{{TaskID: taskID, State: rep.Status, Summary: text}}, nonce, maxResultLimit), nil
}

// OnTurnCompleted enforces the Q-131 no-report rule when a task session
// completes a turn while still in TaskStateRunning.
func (s *teamTaskService) OnTurnCompleted(ctx context.Context, sessionID string) {
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil || !found || task.State != vendorstore.TaskStateRunning ||
		task.HierarchyRole == vendorstore.RoleLead || task.HierarchyRole == vendorstore.RoleSolo {
		return
	}
	firstMiss, err := s.store.MarkTaskNoReportNudged(ctx, task.TeamID, task.TaskID)
	if err != nil {
		return
	}
	if firstMiss {
		if sendErr := s.sessions.Send(ctx, sessionID, vendor.Input{
			Text: "Your turn ended without calling fugue_task_done. Call fugue_task_done now with your summary, status, and any output.",
		}); sendErr != nil {
			slog.WarnContext(ctx, "sending no-report nudge failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(sendErr)))
		}
		return
	}
	s.failTaskNoReport(ctx, &task, sessionID)
}

func (s *teamTaskService) failTaskNoReport(ctx context.Context, task *vendorstore.TaskRecord, sessionID string) {
	rec, _, err := s.store.TeamSnapshot(ctx, task.TeamID)
	if err != nil || rec.State != vendorstore.TeamStateActive {
		return
	}
	summary, _ := truncateUTF8(s.lastAssistantText(ctx, sessionID), teams.MaxReportSummaryBytes)
	evID, err := s.ids.NewID()
	if err != nil {
		return
	}
	if _, err := s.store.AppendTeamEvent(ctx, task.TeamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: task.TaskID, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateFailed), FailureCode: "no_report",
		Mailbox: digestSummaryRows(task.TaskID, task.ParentTaskID, string(vendorstore.TaskStateFailed), "no_report", summary, false),
	}); err == nil && s.wake != nil {
		s.wake.Notify()
	}
}

func (s *teamTaskService) lastAssistantText(ctx context.Context, sessionID string) string {
	items, err := s.store.Transcript(ctx, sessionID, 0, 100)
	if err != nil {
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != string(transcript.AssistantMessage) {
			continue
		}
		_, content, readErr := s.store.ReadItem(ctx, sessionID, items[i].ID)
		if readErr != nil || len(content) == 0 {
			continue
		}
		var msg struct {
			Text string `json:"Text"`
		}
		if json.Unmarshal(content, &msg) == nil && msg.Text != "" {
			return msg.Text
		}
		return string(content)
	}
	return ""
}

func digestSummaryRows(taskID, recipientTaskID, state, failureCode, summary string, question bool) []vendorstore.MailboxEnqueueInput {
	if recipientTaskID == "" {
		return nil
	}
	payload, err := json.Marshal(mailboxDigestItem{
		TaskID: taskID, State: state, FailureCode: failureCode, Summary: summary, Question: question,
	})
	if err != nil {
		return nil
	}
	return []vendorstore.MailboxEnqueueInput{{RecipientTaskID: recipientTaskID, Kind: vendorstore.MailboxKindDigest, Payload: payload}}
}
