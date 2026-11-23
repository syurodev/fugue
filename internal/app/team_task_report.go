package app

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// ResultGate evaluates a reported task's checks before it moves from checking
// to review (Q-125). F11.5 wires the real gate; F11.3 fails closed unless a
// gate is injected.
type ResultGate interface {
	Evaluate(ctx context.Context, teamID, taskID string) (ResultGateVerdict, error)
}

// InitialResultGate extends ResultGate with step 5 (bundled cross review) on
// the initial checking -> review transition.
type InitialResultGate interface {
	EvaluateInitial(ctx context.Context, teamID, taskID string) (ResultGateVerdict, error)
}

// ResultGateVerdict is the outcome of a ResultGate check pass.
type ResultGateVerdict struct {
	Ready       bool
	Passed      bool
	Bounce      bool
	BounceFiles []string
	FailureCode string
	Summary     string
	CrossReview *CrossReviewReport
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
		Status: taskStatusFailed, Summary: summary, SummaryTruncated: truncated, Output: output, SchemaValid: true,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode failed report", err)
	}
	taskID := c.caller.TaskID
	if err := s.appendTeamWithRetry(ctx, c.sessionID, func(_ context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		if cur.caller == nil || cur.caller.State != vendorstore.TaskStateRunning {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not running")
		}
		if cur.role == vendorstore.RoleTeamManager && hasNonTerminalChild(cur.caller.TaskID, cur.tasks) {
			return nil, errcode.New(errcode.FailedPrecondition, "team manager children are not terminal")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate report event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.caller.TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TaskStateFailed), FailureCode: "task_failed", ProtectedPayload: payload,
			Mailbox: digestSummaryRows(cur.caller.TaskID, resolvedParentID(cur.caller.ParentTaskID, cur.tasks), string(vendorstore.TaskStateFailed), "", summary, false),
		}, nil
	}); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	s.maybeTriggerFinalReview(ctx, c.team.TeamID)
	return fmt.Sprintf("task %s recorded as failed", taskID), nil
}

func (s *teamTaskService) recordTaskSucceededReport(ctx context.Context, c *callerTeamContext, summary string, truncated bool, output json.RawMessage) (string, error) {
	commitSHA, err := s.maybeCommitWorktree(ctx, c)
	if err != nil {
		return "", err
	}
	// The task-branch commit is an external effect with no event yet: a host
	// death here leaves its outcome unknown to the store, so restart must
	// reconcile it without committing again.
	if commitSHA != "" {
		config.TriggerCrashPoint(s.crashPoint, config.CrashPointTeamTaskCommitted)
	}
	schemaValid, schemaErr := s.validateTaskOutput(ctx, c.team.TeamID, c.caller.TaskID, output)
	payload, err := json.Marshal(taskReportPayload{
		Status: taskStatusSucceeded, Summary: summary, SummaryTruncated: truncated, Output: output,
		SchemaValid: schemaValid, SchemaError: schemaErr, CommitSHA: commitSHA,
	})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode task report", err)
	}
	taskID, teamID, parentID := c.caller.TaskID, c.team.TeamID, c.caller.ParentTaskID
	if err := s.appendTeamWithRetry(ctx, c.sessionID, func(_ context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		return s.buildReportEvent(cur, summary, payload)
	}); err != nil {
		return "", err
	}
	if err := s.applyResultGate(ctx, teamID, taskID, parentID); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	s.maybeTriggerFinalReview(ctx, teamID)
	return fmt.Sprintf("task %s reported (schema_valid=%t)", taskID, schemaValid), nil
}

func (s *teamTaskService) maybeCommitWorktree(ctx context.Context, c *callerTeamContext) (string, error) {
	if c.caller.Deliverable != vendorstore.DeliverableDiff && c.caller.Deliverable != vendorstore.DeliverableBoth {
		return "", nil
	}
	return s.commitTaskWorktree(ctx, c)
}

func (s *teamTaskService) buildReportEvent(cur *callerTeamContext, summary string, payload []byte) (*vendorstore.AppendTeamEventInput, error) {
	if cur.caller == nil || cur.caller.State != vendorstore.TaskStateRunning {
		return nil, errcode.New(errcode.FailedPrecondition, "task is not running")
	}
	if cur.role == vendorstore.RoleTeamManager && hasNonTerminalChild(cur.caller.TaskID, cur.tasks) {
		return nil, errcode.New(errcode.FailedPrecondition, "team manager children are not terminal")
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate report event ID", idErr)
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: cur.caller.TaskID, EventType: vendorstore.EventTaskReported, ProtectedPayload: payload,
		Mailbox: digestSummaryRows(cur.caller.TaskID, resolvedParentID(cur.caller.ParentTaskID, cur.tasks), string(vendorstore.TaskStateChecking), "", summary, false),
	}, nil
}

func (s *teamTaskService) commitTaskWorktree(ctx context.Context, c *callerTeamContext) (string, error) {
	if s.git == nil {
		return "", errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	wt, err := s.taskWorktreeDir(ctx, &c.team, c.caller.TaskID)
	if err != nil {
		return "", err
	}
	res, err := s.git.CommitWorktree(ctx, wt, "fugue: "+c.caller.TaskID)
	if err != nil {
		return "", boundaryError("commit task worktree", err)
	}
	if res.SHA != "" {
		return res.SHA, nil
	}
	sha, err := s.git.WorktreeHead(ctx, wt)
	if err != nil {
		return "", boundaryError("read worktree head", err)
	}
	return sha, nil
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

func (s *teamTaskService) buildCheckEvent(tasks []vendorstore.TaskRecord, taskID, parentTaskID string, verdict ResultGateVerdict) (*vendorstore.AppendTeamEventInput, error) {
	cur, ok := findTask(tasks, taskID)
	if !ok || cur.State != vendorstore.TaskStateChecking {
		return nil, errcode.New(errcode.FailedPrecondition, "task is not in checking")
	}
	return s.buildCheckEventForState(cur, resolvedParentID(parentTaskID, tasks), verdict)
}

func (s *teamTaskService) buildCheckEventForState(cur *vendorstore.TaskRecord, resolvedParent string, verdict ResultGateVerdict) (*vendorstore.AppendTeamEventInput, error) {
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, errcode.Wrap(errcode.Internal, "generate check event ID", idErr)
	}
	ev := &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskCheckCompleted, Outcome: "passed",
	}
	if verdict.Bounce {
		ev.Outcome = "scope_bounce"
		msgPayload, mErr := json.Marshal(mailboxDigestItem{
			TaskID:  cur.TaskID,
			State:   "scope_bounce",
			Summary: "out-of-scope files: " + strings.Join(verdict.BounceFiles, ", "),
		})
		if mErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "encode scope bounce payload", mErr)
		}
		ev.Mailbox = []vendorstore.MailboxEnqueueInput{{
			RecipientTaskID: cur.TaskID, Kind: vendorstore.MailboxKindSend, Payload: msgPayload,
		}}
		return ev, nil
	}
	if !verdict.Passed {
		ev.Outcome = string(vendorstore.TaskStateFailed)
		ev.FailureCode = verdict.FailureCode
		if ev.FailureCode == "" {
			ev.FailureCode = "checks_failed"
		}
		ev.Mailbox = digestSummaryRows(cur.TaskID, resolvedParent, string(vendorstore.TaskStateFailed), ev.FailureCode, "", false)
		return ev, nil
	}
	if verdict.CrossReview != nil {
		raw, mErr := json.Marshal(verdict.CrossReview)
		if mErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "encode cross review report", mErr)
		}
		ev.ProtectedPayload = raw
		ev.Mailbox = digestSummaryRows(cur.TaskID, resolvedParent, string(vendorstore.TaskStateReview), "", formatCrossReviewDigest(verdict.CrossReview), false)
	}
	return ev, nil
}

// Ask transitions a running task to asking and routes the question either to
// the direct parent mailbox or to the user question card with a one-line
// notice to the direct parent (§6.2, Q-129).
func (s *teamTaskService) Ask(ctx context.Context, sessionID, question, to string) (string, error) {
	q, target, err := validateAskArgs(question, to)
	if err != nil {
		return "", err
	}
	if target == askTargetUser && (s.interactions == nil || s.interactions.owner == nil) {
		return "", errcode.New(errcode.FailedPrecondition, "interaction service is not configured")
	}
	payload, err := json.Marshal(taskQuestionPayload{Question: q, To: target})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode question payload", err)
	}
	teamID, workspaceID, askingTask, seq, hasMail, err := s.appendAskEvent(ctx, sessionID, q, target, payload)
	if err != nil {
		return "", err
	}
	if s.wake != nil && hasMail {
		s.wake.Notify()
	}
	if target == askTargetUser {
		if cardErr := s.ensureUserQuestionCard(teamID, workspaceID, &askingTask, seq, q); cardErr != nil {
			return "", cardErr
		}
	}
	return fmt.Sprintf("task %s is waiting for an answer from %s", askingTask.TaskID, target), nil
}

func (s *teamTaskService) appendAskEvent(
	ctx context.Context,
	sessionID, q, target string,
	payload []byte,
) (teamID, workspaceID string, asking vendorstore.TaskRecord, seq int64, hasMail bool, err error) {
	seq, err = s.appendTeamSeqWithRetry(ctx, sessionID, func(_ context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		if cur.role != vendorstore.RoleTeamManager && cur.role != vendorstore.RoleMember {
			return nil, errcode.New(errcode.PermissionDenied, "only a team manager or member may call fugue_task_ask")
		}
		if cur.caller == nil || cur.caller.State != vendorstore.TaskStateRunning {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not running")
		}
		teamID, workspaceID, asking = cur.team.TeamID, cur.team.WorkspaceID, *cur.caller
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate ask event ID", idErr)
		}
		parentID := resolvedParentID(cur.caller.ParentTaskID, cur.tasks)
		mail := askMailboxRows(cur.caller, parentID, q, target)
		hasMail = len(mail) > 0
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.caller.TaskID, EventType: vendorstore.EventTaskQuestionAsked,
			ProtectedPayload: payload, Mailbox: mail,
		}, nil
	})
	return teamID, workspaceID, asking, seq, hasMail, err
}

func askMailboxRows(caller *vendorstore.TaskRecord, parentID, q, target string) []vendorstore.MailboxEnqueueInput {
	if target == askTargetParent {
		return digestSummaryRows(caller.TaskID, parentID, string(vendorstore.TaskStateAsking), "", q, true)
	}
	return digestSummaryRows(caller.TaskID, parentID, string(vendorstore.TaskStateAsking), "", "question sent to user (needs you)", false)
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
	if offset < 0 {
		return "", errcode.New(errcode.InvalidArgument, "offset must be non-negative")
	}
	if limit <= 0 || limit > maxResultLimit {
		limit = defaultResultLimit
	}
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireReadableTask(taskID)
	if err != nil {
		return "", err
	}
	target = resolveLatestTarget(target, c.tasks)
	switch part {
	case "checks":
		return s.readTaskChecks(ctx, c.team.TeamID, target.TaskID)
	case "diff":
		return s.readTaskDiff(ctx, c, target, offset, limit)
	case "summary", "output", "report":
		return s.readTaskReportPart(ctx, c.team.TeamID, target.TaskID, part, offset, limit)
	default:
		return "", errcode.New(errcode.InvalidArgument, "part must be summary, output, report, checks, or diff")
	}
}

func resolveLatestTarget(target *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) *vendorstore.TaskRecord {
	if target.State != vendorstore.TaskStateStopped {
		return target
	}
	byID, retryChild := resolveTaskChain(tasks)
	if latest := latestAttempt(target.TaskID, byID, retryChild); latest != nil {
		return latest
	}
	return target
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
	res, err := s.git.DiffAgainstParent(ctx, s.dataDir, repoIDFor(root), gitTaskID(target.TaskID), parentRef, offset, limit)
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
	text = sliceUTF8(text, offset, limit)
	nonce, err := s.ids.NewID()
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "generate report nonce", err)
	}
	header := fmt.Sprintf("part=%s schema_valid=%t", part, rep.SchemaValid)
	if rep.SchemaError != "" {
		header += " schema_error=" + teams.EscapeDigestText(rep.SchemaError)
	}
	return teams.BuildDigest(header, []teams.DigestEntry{{TaskID: taskID, State: rep.Status, Summary: text}}, nonce, maxResultLimit), nil
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
