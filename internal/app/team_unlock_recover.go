package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// OnSessionResumed moves a detached lead session's unknown lead task back to
// running via EventTaskRecoveryDecided("resume") -> EventTaskStarted (Q-151).
func (s *teamTaskService) OnSessionResumed(ctx context.Context, sessionID string) error {
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		return boundaryError("read resumed session task", err)
	}
	if !found || task.HierarchyRole != vendorstore.RoleLead || task.State != vendorstore.TaskStateUnknown {
		return nil
	}
	for attempt := range maxAppendRetries {
		done, retry, appErr := s.tryAppendLeadRecoveryStart(ctx, task.TeamID, task.TaskID, sessionID)
		if appErr != nil {
			return appErr
		}
		if done || !retry {
			return nil
		}
		if attempt+1 == maxAppendRetries {
			return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
		}
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) tryAppendLeadRecoveryStart(ctx context.Context, teamID, taskID, sessionID string) (done, retry bool, err error) {
	rec, tasks, snapErr := s.store.TeamSnapshot(ctx, teamID)
	if snapErr != nil {
		return false, false, boundaryError("read lead team", snapErr)
	}
	cur, ok := findTask(tasks, taskID)
	if rec.State != vendorstore.TeamStateActive || !ok || cur.State != vendorstore.TaskStateUnknown {
		return true, false, nil
	}
	evRecID, idErr := s.ids.NewID()
	if idErr != nil {
		return false, false, errcode.Wrap(errcode.Internal, "generate lead recovery event ID", idErr)
	}
	evStartID, idErr := s.ids.NewID()
	if idErr != nil {
		return false, false, errcode.Wrap(errcode.Internal, "generate lead start event ID", idErr)
	}
	evs := []*vendorstore.AppendTeamEventInput{
		{EventID: evRecID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskRecoveryDecided, Outcome: recoveryDecisionResume},
		{EventID: evStartID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskStarted},
	}
	if _, appErr := s.store.AppendTeamEvents(ctx, teamID, rec.LastSeq, evs); appErr != nil {
		if errors.Is(appErr, vendorstore.ErrSequenceConflict) {
			return false, true, nil
		}
		return false, false, boundaryError("recover resumed lead task", appErr)
	}
	if s.budgets != nil {
		s.budgets.trackTaskWatch(teamID, cur, vendorstore.TaskStateRunning, sessionID, rec.LastSeq+2, s.now().UnixMilli())
		s.budgets.Notify()
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return true, false, nil
}

// RecoverOnUnlock runs once the vault unlocks after host startup: it rebuilds
// user question cards for unknown tasks whose TaskMarkedUnknown came from
// asking (Q-142), and inspects each recovered non-lead task's worktree to send
// its direct delegator one idempotent mailbox notice (Q-151, OR-E09).
func (s *teamTaskService) RecoverOnUnlock(ctx context.Context) error {
	s.unlockMu.Lock()
	defer s.unlockMu.Unlock()

	teamIDs, err := s.store.ActiveTeamIDs(ctx)
	if err != nil {
		return boundaryError("list active teams for unlock recovery", err)
	}
	var enqueued bool
	for _, teamID := range teamIDs {
		sent, recErr := s.recoverTeamOnUnlock(ctx, teamID)
		if recErr != nil {
			slog.WarnContext(ctx, "unlock recovery failed for team", "team_id", teamID, "error_code", string(errcode.CodeOf(recErr)))
		}
		enqueued = enqueued || sent
	}
	if enqueued && s.wake != nil {
		s.wake.Notify()
	}
	return nil
}

func (s *teamTaskService) recoverTeamOnUnlock(ctx context.Context, teamID string) (bool, error) {
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return false, boundaryError("read active team for unlock recovery", err)
	}
	if rec.State != vendorstore.TeamStateActive {
		return false, nil
	}
	origins, err := s.unknownTaskOrigins(ctx, teamID)
	if err != nil {
		return false, err
	}
	var enqueued bool
	for i := range tasks {
		t := &tasks[i]
		if t.State != vendorstore.TaskStateUnknown {
			continue
		}
		orig := origins[t.TaskID]
		if orig.fromAsking {
			s.rebuildUserQuestionCard(ctx, &rec, t)
		}
		if t.HierarchyRole != vendorstore.RoleLead {
			if parentID := resolvedParentID(t.ParentTaskID, tasks); parentID != "" && s.notifyDelegatorUnknown(ctx, &rec, t, parentID, orig.seq) {
				enqueued = true
			}
		}
	}
	return enqueued, nil
}

type taskUnknownOrigin struct {
	fromAsking bool
	seq        int64
}

func (s *teamTaskService) unknownTaskOrigins(ctx context.Context, teamID string) (map[string]taskUnknownOrigin, error) {
	origins := make(map[string]taskUnknownOrigin)
	var afterSeq int64
	for {
		evs, err := s.store.TeamEventsSince(ctx, teamID, afterSeq, teamEventScanLimit)
		if err != nil {
			return nil, boundaryError("read team events for unlock recovery", err)
		}
		for i := range evs {
			ev := &evs[i]
			afterSeq = ev.Sequence
			if ev.EventType == vendorstore.EventTaskMarkedUnknown && ev.TaskID != "" {
				origins[ev.TaskID] = taskUnknownOrigin{fromAsking: ev.FromState == string(vendorstore.TaskStateAsking), seq: ev.Sequence}
			}
		}
		if len(evs) < teamEventScanLimit {
			break
		}
	}
	return origins, nil
}

func (s *teamTaskService) unknownTaskMarkedFromAsking(ctx context.Context, teamID, taskID string) (bool, int64, error) {
	origins, err := s.unknownTaskOrigins(ctx, teamID)
	if err != nil {
		return false, 0, err
	}
	orig := origins[taskID]
	return orig.fromAsking, orig.seq, nil
}

func (s *teamTaskService) rebuildUserQuestionCard(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord) {
	raw, seq, found, err := s.store.OpenLatestTaskEventPayloadWithSeq(ctx, rec.TeamID, task.TaskID, vendorstore.EventTaskQuestionAsked)
	if err != nil || !found || seq <= 0 {
		return
	}
	var qp taskQuestionPayload
	if json.Unmarshal(raw, &qp) != nil || qp.Question == "" || qp.To != askTargetUser {
		return
	}
	spec, err := s.userQuestionCardSpec(rec.TeamID, rec.WorkspaceID, task, seq, qp.Question)
	if err != nil {
		slog.WarnContext(ctx, "building rebuilt user question card failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if err := ensureRebuildableCard(s.interactions, spec); err != nil {
		slog.WarnContext(ctx, "rebuilding user question card failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
	}
}

func (s *teamTaskService) notifyDelegatorUnknown(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, parentID string, markedSeq int64) bool {
	if markedSeq <= 0 {
		markedSeq = max(task.LastEventSeq, 1)
	}
	summary := s.inspectRecoveredWorktreeSummary(ctx, rec, task.TaskID)
	payload, err := json.Marshal(mailboxDigestItem{TaskID: task.TaskID, State: string(vendorstore.TaskStateUnknown), Summary: summary})
	if err != nil {
		return false
	}
	uniqueID := fmt.Sprintf("unknown-notice:%s:%d", task.TaskID, markedSeq)
	if err := s.store.EnqueueDirectMailboxIdempotent(ctx, rec.TeamID, parentID, vendorstore.MailboxKindSend, uniqueID, payload); err != nil {
		slog.WarnContext(ctx, "enqueueing unknown task recovery notice failed", "team_id", rec.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return false
	}
	return true
}

func (s *teamTaskService) inspectRecoveredWorktreeSummary(ctx context.Context, rec *vendorstore.TeamRecord, taskID string) string {
	const actionHint = "; call fugue_task_recover(task_id, resume|keep_diff|cancel)"
	if s.git == nil || s.roots == nil {
		return "worktree=uninspected" + actionHint
	}
	wt, err := s.taskWorktreeDir(ctx, rec, taskID)
	if err != nil {
		return "worktree=unavailable (" + string(errcode.CodeOf(err)) + ")" + actionHint
	}
	st, err := s.git.WorktreeStatus(ctx, wt)
	if err != nil {
		return "worktree=missing (" + string(errcode.CodeOf(err)) + ")" + actionHint
	}
	return st.Summary + actionHint
}
