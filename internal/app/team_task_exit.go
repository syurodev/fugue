package app

// The §12 mid-turn exit path (F11.9): when a team task's vendor process exits
// unexpectedly, its task moves to unknown and its direct delegator receives
// the same idempotent unknown notice the unlock recovery sends (Q-151,
// Q-159 A). The task is never rerun; only an explicit recovery decision
// leaves unknown. A clean stop, close, detach, idle detach, task end and host
// shutdown all set a close reason first and never reach this path.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// OnSessionDied marks the team task of a session whose vendor process exited
// unexpectedly (§12: the agent dies mid-turn). Only a running or asking task
// moves to unknown; a task still starting fails through its intro path, and
// checking or later states do not depend on the vendor process.
func (s *teamTaskService) OnSessionDied(ctx context.Context, sessionID string) {
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "session-exit check failed to read task", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if !found {
		return
	}
	if err := s.markTaskUnknownOnExit(ctx, &task); err != nil {
		slog.WarnContext(ctx, "marking task unknown after session exit failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if s.wake != nil {
		s.wake.Notify()
	}
}

// markTaskUnknownOnExit appends TaskMarkedUnknown for a live running or
// asking task whose session died, then notifies its direct delegator once.
func (s *teamTaskService) markTaskUnknownOnExit(ctx context.Context, task *vendorstore.TaskRecord) error {
	if task.State != vendorstore.TaskStateRunning && task.State != vendorstore.TaskStateAsking {
		return nil
	}
	for attempt := range maxAppendRetries {
		done, retry, seq, err := s.tryAppendExitUnknown(ctx, task.TeamID, task.TaskID)
		if err != nil {
			return err
		}
		if !retry {
			if done && seq > 0 {
				s.notifyDelegatorOnExit(ctx, task.TeamID, task.TaskID, seq)
			}
			return nil
		}
		if attempt+1 == maxAppendRetries {
			return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
		}
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

// tryAppendExitUnknown appends TaskMarkedUnknown while the task is still
// running or asking; a concurrent turn end, report or cancel owns the outcome
// instead, so the exit never overwrites it.
func (s *teamTaskService) tryAppendExitUnknown(ctx context.Context, teamID, taskID string) (done, retry bool, seq int64, err error) {
	rec, tasks, snapErr := s.store.TeamSnapshot(ctx, teamID)
	if snapErr != nil {
		return false, false, 0, boundaryError("read the exited task team", snapErr)
	}
	if rec.State != vendorstore.TeamStateActive {
		return true, false, 0, nil
	}
	cur, ok := findTask(tasks, taskID)
	if !ok || (cur.State != vendorstore.TaskStateRunning && cur.State != vendorstore.TaskStateAsking) {
		return true, false, 0, nil
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return false, false, 0, errcode.Wrap(errcode.Internal, "generate exit unknown event ID", idErr)
	}
	seq, appErr := s.store.AppendTeamEvent(ctx, teamID, rec.LastSeq, &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskMarkedUnknown,
	})
	if appErr != nil {
		if errors.Is(appErr, vendorstore.ErrSequenceConflict) {
			return false, true, 0, nil
		}
		return false, false, 0, boundaryError("record the exit unknown event", appErr)
	}
	return true, false, seq, nil
}

// notifyDelegatorOnExit sends the unknown notice to the task's direct
// delegator. A lead task has no delegator; the user sees it on the board.
func (s *teamTaskService) notifyDelegatorOnExit(ctx context.Context, teamID, taskID string, markedSeq int64) {
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		slog.WarnContext(ctx, "reading the exited task team for the notice failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	cur, ok := findTask(tasks, taskID)
	if !ok {
		return
	}
	parentID := resolvedParentID(cur.ParentTaskID, tasks)
	if parentID == "" {
		return
	}
	s.notifyDelegatorUnknown(ctx, &rec, cur, tasks, parentID, markedSeq)
}

// exitUnknownSummary renders the delegator notice summary: the worktree
// snapshot (Q-151), prefixed for an unknown manager with the count of its
// still-active children (§12 takeover, Q-159 A).
func (s *teamTaskService) exitUnknownSummary(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) string {
	summary := s.inspectRecoveredWorktreeSummary(ctx, rec, task.TaskID)
	if task.HierarchyRole != vendorstore.RoleTeamManager {
		return summary
	}
	byID, retryChild := resolveTaskChain(tasks)
	active := 0
	for i := range tasks {
		if sameAttemptChain(tasks[i].ParentTaskID, task.TaskID, byID, retryChild) && !teamTaskTerminal(tasks[i].State) {
			active++
		}
	}
	return fmt.Sprintf("team manager is unknown; %d child task(s) still active; ", active) + summary
}
