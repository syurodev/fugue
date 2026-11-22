package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func sliceUTF8(s string, offset, limit int) string {
	if offset >= len(s) {
		return ""
	}
	for offset > 0 && offset < len(s) && !utf8.RuneStart(s[offset]) {
		offset++
	}
	if offset >= len(s) {
		return ""
	}
	cut, _ := truncateUTF8(s[offset:], limit)
	return cut
}

func (s *teamTaskService) appendTeamWithRetry(
	ctx context.Context,
	sessionID string,
	build func(ctx context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error),
) error {
	_, err := s.appendTeamSeqWithRetry(ctx, sessionID, build)
	return err
}

func (s *teamTaskService) appendTeamSeqWithRetry(
	ctx context.Context,
	sessionID string,
	build func(ctx context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error),
) (int64, error) {
	for attempt := range maxAppendRetries {
		c, err := s.resolveCaller(ctx, sessionID)
		if err != nil {
			return 0, err
		}
		ev, err := build(ctx, c)
		if err != nil {
			return 0, err
		}
		seq, err := s.store.AppendTeamEvent(ctx, c.team.TeamID, c.team.LastSeq, ev)
		if err == nil {
			return seq, nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return 0, boundaryError("record team event", err)
	}
	return 0, errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) appendTaskEventWithRetry(
	ctx context.Context,
	teamID string,
	build func(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error),
) error {
	for attempt := range maxAppendRetries {
		rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
		if err != nil {
			return boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive {
			return errcode.New(errcode.FailedPrecondition, "the team is not active")
		}
		ev, err := build(&rec, tasks)
		if err != nil {
			return err
		}
		if _, err = s.store.AppendTeamEvent(ctx, teamID, rec.LastSeq, ev); err == nil {
			return nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return boundaryError("record team event", err)
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

// OnTurnCompleted enforces the Q-131 no-report rule when a task session
// completes a turn while still in TaskStateRunning. A team_manager with
// non-terminal children is never nudged or failed until its last child ends.
func (s *teamTaskService) OnTurnCompleted(ctx context.Context, sessionID string) {
	if s.mergeQueue != nil {
		s.mergeQueue.Notify()
	}
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "no-report check failed to read task", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if !found || task.State != vendorstore.TaskStateRunning ||
		task.HierarchyRole == vendorstore.RoleLead || task.HierarchyRole == vendorstore.RoleSolo {
		return
	}
	if task.HierarchyRole == vendorstore.RoleTeamManager {
		_, tasks, snapErr := s.store.TeamSnapshot(ctx, task.TeamID)
		if snapErr != nil {
			slog.WarnContext(ctx, "no-report check failed to read team", "team_id", task.TeamID, "error_code", string(errcode.CodeOf(snapErr)))
			return
		}
		if hasNonTerminalChild(task.TaskID, tasks) {
			return
		}
	}
	firstMiss, err := s.store.MarkTaskNoReportNudged(ctx, task.TeamID, task.TaskID)
	if err != nil {
		slog.WarnContext(ctx, "marking no_report_nudged failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if firstMiss {
		s.sendNoReportNudge(ctx, sessionID)
		return
	}
	s.failTaskNoReport(ctx, &task, sessionID)
}

func (s *teamTaskService) sendNoReportNudge(ctx context.Context, sessionID string) {
	opCtx := context.WithoutCancel(ctx)
	err := s.sessions.SendAsync(ctx, sessionID, vendor.Input{
		Text:   "Your turn ended without calling fugue_task_done. Call fugue_task_done now with your summary, status, and any output.",
		Origin: vendor.OriginFugue,
	}, func(sendErr error) {
		if sendErr != nil && !errors.Is(sendErr, ErrHostStopping) {
			slog.WarnContext(opCtx, "sending no-report nudge failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(sendErr)))
		}
	})
	if err != nil {
		slog.WarnContext(ctx, "sending no-report nudge failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

// OnTurnFailed handles quota and auth turn failures on a task session (§8,
// Q-149): it moves the task to failed(quota)/failed(auth) and notifies the
// direct delegator in the same transaction.
func (s *teamTaskService) OnTurnFailed(ctx context.Context, sessionID, failureCode string) {
	if s.mergeQueue != nil {
		s.mergeQueue.Notify()
	}
	if failureCode != string(vendor.FailureQuota) && failureCode != string(vendor.FailureAuth) {
		return
	}
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "turn-failure check failed to read task", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if !found || !canFailTaskOnTurn(&task) {
		return
	}
	if err := s.recordTaskQuotaAuthFailure(ctx, task.TeamID, task.TaskID, failureCode); err != nil {
		slog.WarnContext(ctx, "recording task quota/auth failure failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	s.closeFailedTaskSession(ctx, sessionID)
	if s.wake != nil {
		s.wake.Notify()
	}
	if failureCode == string(vendor.FailureQuota) && s.tryReassignOnQuota(ctx, &task, sessionID) {
		s.dispatchReassignedTeam(ctx, task.TeamID, task.TaskID)
	}
}

func (s *teamTaskService) dispatchReassignedTeam(ctx context.Context, teamID, taskID string) {
	if s.dispatch == nil {
		return
	}
	if dispErr := s.dispatch.Dispatch(ctx, teamID); dispErr != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "dispatching quota-reassigned task failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(dispErr)))
	}
}

func (s *teamTaskService) tryReassignOnQuota(ctx context.Context, task *vendorstore.TaskRecord, sessionID string) bool {
	if s.accounts == nil || (s.vault != nil && s.vault.Snapshot().State != vault.StateUnlocked) {
		return false
	}
	fromAccount, toAccount, ok := s.pickQuotaAlternateAccount(ctx, task, sessionID)
	if !ok {
		return false
	}
	payload, ok := s.buildQuotaReassignPayload(ctx, task, sessionID, fromAccount, toAccount)
	if !ok {
		return false
	}
	return s.commitQuotaReassignRetry(ctx, task, fromAccount, toAccount, payload)
}

func (s *teamTaskService) pickQuotaAlternateAccount(ctx context.Context, task *vendorstore.TaskRecord, sessionID string) (fromAccount, toAccount string, ok bool) {
	sess, err := s.store.Snapshot(ctx, sessionID)
	if err != nil {
		return "", "", false
	}
	fromAccount = sess.AccountID
	if fromAccount == "" {
		fromAccount = vendorDefaultAccount
	}
	_, role, code, err := resolveTaskProfile(ctx, s.store, task.TeamID, task.TaskID, task.CatalogRole)
	if err != nil || code != "" {
		return "", "", false
	}
	toAccount, allLimited, _, _, pickErr := s.accountPicker().PickForTask(ctx, sess.Vendor, role.Model, fromAccount)
	if pickErr != nil || allLimited || toAccount == "" || toAccount == fromAccount {
		return "", "", false
	}
	return fromAccount, toAccount, true
}

func (s *teamTaskService) buildQuotaReassignPayload(ctx context.Context, task *vendorstore.TaskRecord, sessionID, fromAccount, toAccount string) ([]byte, bool) {
	progressSummary, _ := truncateUTF8(s.lastAssistantText(ctx, sessionID), teams.MaxReportSummaryBytes)
	worktreeDiff := s.captureQuotaWorktreeDiff(ctx, task)
	payload, mErr := json.Marshal(quotaReassignPayload{
		FromAccount:     fromAccount,
		ToAccount:       toAccount,
		ProgressSummary: progressSummary,
		WorktreeDiff:    worktreeDiff,
	})
	return payload, mErr == nil
}

func (s *teamTaskService) commitQuotaReassignRetry(ctx context.Context, task *vendorstore.TaskRecord, fromAccount, toAccount string, payload []byte) bool {
	for attempt := range maxAppendRetries {
		rec, _, snapErr := s.store.TeamSnapshot(ctx, task.TeamID)
		if snapErr != nil || rec.State != vendorstore.TeamStateActive {
			return false
		}
		supID, id1 := s.ids.NewID()
		crtID, id2 := s.ids.NewID()
		if id1 != nil || id2 != nil {
			return false
		}
		_, retryErr := s.store.RetryTask(ctx, task.TeamID, task.TaskID, &vendorstore.TaskRetryInput{
			ExpectedSeq:       rec.LastSeq,
			SupersededEventID: supID,
			CreatedEventID:    crtID,
			SupersededPayload: payload,
			QuotaReassign:     true,
		})
		if retryErr == nil {
			slog.InfoContext(ctx, "reassigned quota-exhausted task to alternate account",
				"team_id", task.TeamID, "task_id", task.TaskID, "from_account", fromAccount, "to_account", toAccount)
			return true
		}
		if !errors.Is(retryErr, vendorstore.ErrSequenceConflict) || attempt+1 == maxAppendRetries {
			return false
		}
	}
	return false
}

func (s *teamTaskService) accountPicker() *accountPicker {
	if s.dispatch != nil && s.dispatch.picker != nil {
		return s.dispatch.picker
	}
	return newAccountPicker(s.accounts, s.hostCfg, s.clk)
}

func (s *teamTaskService) captureQuotaWorktreeDiff(ctx context.Context, task *vendorstore.TaskRecord) string {
	if s.git == nil || s.roots == nil || s.dataDir == "" {
		return ""
	}
	rec, tasks, err := s.store.TeamSnapshot(ctx, task.TeamID)
	if err != nil {
		return ""
	}
	lead, err := s.store.Snapshot(ctx, rec.SessionID)
	if err != nil {
		return ""
	}
	repoDir, err := s.roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return ""
	}
	repoID := repoIDFor(repoDir)
	wtID := gitTaskID(task.TaskID)
	wtDir, wtErr := gitops.TaskWorktreePath(s.dataDir, repoID, wtID)
	if wtErr != nil {
		return ""
	}
	if _, cErr := s.git.CommitWorktree(ctx, wtDir, "fugue: checkpoint before quota reassignment"); cErr != nil {
		slog.DebugContext(ctx, "quota reassignment worktree checkpoint skipped", "task_id", task.TaskID, "error_code", string(errcode.CodeOf(cErr)))
	}
	parentRef, pErr := parentBranch(task, tasks)
	if pErr != nil {
		return ""
	}
	diffRes, dErr := s.git.DiffAgainstParent(ctx, s.dataDir, repoID, wtID, parentRef, 0, defaultResultLimit)
	if dErr != nil {
		return ""
	}
	return diffRes.Text
}

func canFailTaskOnTurn(task *vendorstore.TaskRecord) bool {
	if task.HierarchyRole == vendorstore.RoleLead || task.HierarchyRole == vendorstore.RoleSolo {
		return false
	}
	return task.State == vendorstore.TaskStateRunning || task.State == vendorstore.TaskStateAsking
}

func (s *teamTaskService) recordTaskQuotaAuthFailure(ctx context.Context, teamID, taskID, failureCode string) error {
	return s.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || !canFailTaskOnTurn(cur) {
			return nil, errcode.New(errcode.FailedPrecondition, "task is no longer running or asking")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate turn failure event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: vendorstore.TaskOutcomeFailed, FailureCode: failureCode,
			Mailbox: digestSummaryRows(cur.TaskID, resolvedParentID(cur.ParentTaskID, tasks), string(vendorstore.TaskStateFailed), failureCode, "", false),
		}, nil
	})
}

func (s *teamTaskService) closeFailedTaskSession(ctx context.Context, sessionID string) {
	if s.sessions == nil {
		return
	}
	err := s.sessions.Close(ctx, sessionID, vendor.CloseHostStop)
	if err != nil && !errors.Is(err, vendorstore.ErrSessionNotFound) && !errors.Is(err, vendor.ErrSessionClosed) {
		slog.WarnContext(ctx, "closing task session after quota/auth failure failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

func (s *teamTaskService) failTaskNoReport(ctx context.Context, task *vendorstore.TaskRecord, sessionID string) {
	summary, _ := truncateUTF8(s.lastAssistantText(ctx, sessionID), teams.MaxReportSummaryBytes)
	if err := s.appendTaskEventWithRetry(ctx, task.TeamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, task.TaskID)
		if !ok || cur.State != vendorstore.TaskStateRunning {
			return nil, errcode.New(errcode.FailedPrecondition, "task is no longer running")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate no-report event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TaskStateFailed), FailureCode: "no_report",
			Mailbox: digestSummaryRows(cur.TaskID, resolvedParentID(cur.ParentTaskID, tasks), string(vendorstore.TaskStateFailed), "no_report", summary, false),
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "recording no_report failure failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if s.wake != nil {
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
