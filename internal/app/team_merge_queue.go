package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type mergeTargetKey struct {
	repoID       string
	targetBranch string
}

type mergeQueueEntry struct {
	teamID string
	taskID string
}

type targetMergeQueue struct {
	running bool
	entries []mergeQueueEntry
}

// teamMergeQueue serializes diff task integration per target branch (§9):
// rebase onto the latest target, rerun §10 gates 1-4, and fast-forward merge.
// A delegator with write access integrates children only at its turn boundary
// after a result commit of its own changes.
type teamMergeQueue struct {
	svc *teamTaskService

	mu      sync.Mutex
	targets map[mergeTargetKey]*targetMergeQueue
	wakeCh  chan struct{}
	idleCh  chan struct{}
}

func newTeamMergeQueue(svc *teamTaskService) *teamMergeQueue {
	idle := make(chan struct{})
	close(idle)
	return &teamMergeQueue{
		svc:     svc,
		targets: make(map[mergeTargetKey]*targetMergeQueue),
		wakeCh:  make(chan struct{}, 1),
		idleCh:  idle,
	}
}

// Start runs the per-target merge queue coordinator until ctx is cancelled.
func (q *teamMergeQueue) Start(ctx context.Context) {
	supervise.Go(ctx, "team-merge-queue", func(runCtx context.Context) error {
		q.runLoop(runCtx)
		return nil
	})
}

// Notify wakes the merge queue so targets waiting on a turn boundary re-check.
func (q *teamMergeQueue) Notify() {
	q.mu.Lock()
	for _, tq := range q.targets {
		if len(tq.entries) > 0 {
			q.markBusyLocked()
			break
		}
	}
	q.mu.Unlock()
	select {
	case q.wakeCh <- struct{}{}:
	default:
	}
}

// Enqueue adds a task in TaskStateIntegrating to its target branch queue.
func (q *teamMergeQueue) Enqueue(ctx context.Context, teamID, taskID string) error {
	rec, tasks, err := q.svc.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return boundaryError("read team for merge queue", err)
	}
	task, ok := findTask(tasks, taskID)
	if !ok {
		return errcode.New(errcode.NotFound, "team task not found")
	}
	targetBranch, err := parentBranch(task, tasks)
	if err != nil {
		return err
	}
	_, repoID := resolveTeamRepo(ctx, q.svc.workspaces, &rec)
	key := mergeTargetKey{repoID: repoID, targetBranch: targetBranch}

	q.mu.Lock()
	tq, exists := q.targets[key]
	if !exists {
		tq = &targetMergeQueue{}
		q.targets[key] = tq
	}
	for _, existing := range tq.entries {
		if existing.teamID == teamID && existing.taskID == taskID {
			q.mu.Unlock()
			q.Notify()
			return nil
		}
	}
	tq.entries = append(tq.entries, mergeQueueEntry{teamID: teamID, taskID: taskID})
	q.markBusyLocked()
	q.mu.Unlock()
	q.Notify()
	return nil
}

// WaitIdle blocks until all queued merge entries have finished processing or
// paused for a turn boundary, or until ctx is cancelled.
func (q *teamMergeQueue) WaitIdle(ctx context.Context) error {
	q.mu.Lock()
	ch := q.idleCh
	q.mu.Unlock()
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait merge queue idle: %w", ctx.Err())
	}
}

func (q *teamMergeQueue) markBusyLocked() {
	select {
	case <-q.idleCh:
		q.idleCh = make(chan struct{})
	default:
	}
}

func (q *teamMergeQueue) updateIdleLocked() {
	for _, tq := range q.targets {
		if tq.running {
			return
		}
	}
	select {
	case <-q.idleCh:
	default:
		close(q.idleCh)
	}
}

func (q *teamMergeQueue) runLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-q.wakeCh:
			q.dispatchReadyTargets(ctx)
		}
	}
}

func (q *teamMergeQueue) dispatchReadyTargets(ctx context.Context) {
	q.mu.Lock()
	var toStart []mergeTargetKey
	for key, tq := range q.targets {
		if !tq.running && len(tq.entries) > 0 {
			tq.running = true
			toStart = append(toStart, key)
		}
	}
	q.mu.Unlock()
	for _, key := range toStart {
		k := key
		supervise.Go(ctx, "merge-target:"+k.targetBranch, func(wCtx context.Context) error {
			q.runTarget(wCtx, k)
			return nil
		})
	}
}

func (q *teamMergeQueue) runTarget(ctx context.Context, key mergeTargetKey) {
	for {
		if ctx.Err() != nil {
			q.finishTarget(key, false)
			return
		}
		entry, ok := q.peekTarget(key)
		if !ok {
			q.finishTarget(key, false)
			return
		}
		waitTurn := q.processEntry(ctx, entry)
		if waitTurn {
			q.finishTarget(key, true)
			return
		}
		q.popTarget(key)
	}
}

func (q *teamMergeQueue) peekTarget(key mergeTargetKey) (mergeQueueEntry, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	tq := q.targets[key]
	if tq == nil || len(tq.entries) == 0 {
		return mergeQueueEntry{}, false
	}
	return tq.entries[0], true
}

func (q *teamMergeQueue) popTarget(key mergeTargetKey) {
	q.mu.Lock()
	defer q.mu.Unlock()
	tq := q.targets[key]
	if tq == nil || len(tq.entries) == 0 {
		return
	}
	tq.entries = tq.entries[1:]
	if len(tq.entries) == 0 && !tq.running {
		delete(q.targets, key)
	}
}

func (q *teamMergeQueue) finishTarget(key mergeTargetKey, waitingTurn bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if tq := q.targets[key]; tq != nil {
		tq.running = false
		if !waitingTurn && len(tq.entries) == 0 {
			delete(q.targets, key)
		}
	}
	q.updateIdleLocked()
}

type mergeEntryPaths struct {
	rec          vendorstore.TeamRecord
	tasks        []vendorstore.TaskRecord
	task         *vendorstore.TaskRecord
	repoDir      string
	repoID       string
	targetBranch string
	taskBranch   string
	worktreeDir  string
	parentID     string
}

func (q *teamMergeQueue) processEntry(ctx context.Context, entry mergeQueueEntry) (waitTurn bool) {
	ep, ok := q.resolveEntryPaths(ctx, entry)
	if !ok {
		return false
	}
	if wait, prepErr := q.commitManagerAtTurnBoundary(ctx, &ep.rec, ep.tasks, ep.repoID, ep.parentID); prepErr != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "turn-boundary manager commit failed", "team_id", entry.teamID, "task_id", entry.taskID, "error_code", string(errcode.CodeOf(prepErr)))
		}
		return false
	} else if wait {
		return true
	}
	q.executeMergeEntry(ctx, &ep)
	return false
}

func (q *teamMergeQueue) resolveEntryPaths(ctx context.Context, entry mergeQueueEntry) (mergeEntryPaths, bool) {
	rec, tasks, err := q.svc.store.TeamSnapshot(ctx, entry.teamID)
	if err != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "merge queue failed to read team snapshot", "team_id", entry.teamID, "error_code", string(errcode.CodeOf(err)))
		}
		return mergeEntryPaths{}, false
	}
	if rec.State != vendorstore.TeamStateActive {
		return mergeEntryPaths{}, false
	}
	task, ok := findTask(tasks, entry.taskID)
	if !ok || task.State != vendorstore.TaskStateIntegrating {
		return mergeEntryPaths{}, false
	}
	repoDir, repoID := resolveTeamRepo(ctx, q.svc.workspaces, &rec)
	if repoDir == "" || q.svc.git == nil {
		return mergeEntryPaths{}, false
	}
	targetBranch, err := parentBranch(task, tasks)
	if err != nil {
		return mergeEntryPaths{}, false
	}
	taskBranch, err := gitops.TaskBranchName(gitTaskID(task.TaskID))
	if err != nil {
		return mergeEntryPaths{}, false
	}
	worktreeDir, err := gitops.TaskWorktreePath(q.svc.dataDir, repoID, gitTaskID(task.TaskID))
	if err != nil {
		return mergeEntryPaths{}, false
	}
	return mergeEntryPaths{
		rec: rec, tasks: tasks, task: task, repoDir: repoDir, repoID: repoID,
		targetBranch: targetBranch, taskBranch: taskBranch, worktreeDir: worktreeDir,
		parentID: resolvedParentID(task.ParentTaskID, tasks),
	}, true
}

func (q *teamMergeQueue) executeMergeEntry(ctx context.Context, ep *mergeEntryPaths) {
	reb, rebErr := q.rebaseTaskBranch(ctx, &ep.rec, ep.tasks, ep.repoDir, ep.worktreeDir, ep.targetBranch, ep.taskBranch)
	if rebErr != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "merge queue rebase failed", "team_id", ep.rec.TeamID, "task_id", ep.task.TaskID, "error_code", string(errcode.CodeOf(rebErr)))
		}
		return
	}
	if reb.Conflicted {
		q.recordConflict(ctx, ep.rec.TeamID, ep.task.TaskID, ep.parentID, reb.ConflictedFiles)
		return
	}
	verdict, gateErr := q.svc.resultGate.Evaluate(ctx, ep.rec.TeamID, ep.task.TaskID)
	if gateErr != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "post-rebase result gate failed", "team_id", ep.rec.TeamID, "task_id", ep.task.TaskID, "error_code", string(errcode.CodeOf(gateErr)))
		}
		return
	}
	if !verdict.Ready {
		return
	}
	if verdict.Bounce || !verdict.Passed {
		q.recordPostRebaseGateFailure(ctx, ep.rec.TeamID, ep.task.TaskID, ep.task.SessionID, ep.parentID, verdict)
		return
	}
	mergedSHA, ffErr := q.svc.git.FastForwardBranch(ctx, ep.repoDir, ep.targetBranch, ep.taskBranch)
	if ffErr != nil {
		if ctx.Err() == nil {
			slog.WarnContext(ctx, "fast-forward merge failed", "team_id", ep.rec.TeamID, "task_id", ep.task.TaskID, "error_code", string(errcode.CodeOf(ffErr)))
		}
		return
	}
	q.recordIntegrated(ctx, ep.rec.TeamID, ep.task.TaskID, ep.task.SessionID, ep.parentID, ep.targetBranch, mergedSHA)
}

func (q *teamMergeQueue) commitManagerAtTurnBoundary(
	ctx context.Context,
	rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	repoID, parentID string,
) (waitTurn bool, err error) {
	parentTask, ok := findTask(tasks, parentID)
	if !ok || parentTask.HierarchyRole != vendorstore.RoleTeamManager {
		return false, nil
	}
	hasWrite := q.taskHasWritePermission(ctx, rec.TeamID, parentTask)
	if !hasWrite {
		return false, nil
	}
	if parentTask.SessionID != "" {
		snap, snapErr := q.svc.store.Snapshot(ctx, parentTask.SessionID)
		if snapErr == nil && snap.State == vendorstore.StateRunning {
			return true, nil
		}
	}
	mgrWT, err := gitops.TaskWorktreePath(q.svc.dataDir, repoID, gitTaskID(parentTask.TaskID))
	if err != nil {
		return false, boundaryError("resolve manager worktree", err)
	}
	if _, commitErr := q.svc.git.CommitWorktree(ctx, mgrWT, "fugue: "+parentTask.TaskID); commitErr != nil {
		return false, boundaryError("commit manager worktree", commitErr)
	}
	return false, nil
}

func (q *teamMergeQueue) taskHasWritePermission(ctx context.Context, teamID string, task *vendorstore.TaskRecord) bool {
	role, found, err := decidedRoleFor(ctx, q.svc.store, teamID, task.CatalogRole)
	if err == nil && found {
		return role.Permission == string(teams.PermissionWrite)
	}
	return task.Deliverable != vendorstore.DeliverableReport
}

func (q *teamMergeQueue) rebaseTaskBranch(
	ctx context.Context,
	rec *vendorstore.TeamRecord,
	tasks []vendorstore.TaskRecord,
	repoDir, worktreeDir, targetBranch, taskBranch string,
) (gitops.RebaseResult, error) {
	teamBranch, err := gitops.TeamBranchName(rec.TeamID)
	if err != nil {
		return gitops.RebaseResult{}, boundaryError("derive team branch", err)
	}
	if targetBranch == teamBranch {
		upstreamBase, reparented, rpErr := reparentedTaskBase(ctx, q.svc.git, repoDir, teamBranch, taskBranch, tasks)
		if rpErr != nil {
			return gitops.RebaseResult{}, rpErr
		}
		if reparented {
			reb, rebErr := q.svc.git.RebaseOnto(ctx, worktreeDir, teamBranch, upstreamBase)
			return reb, boundaryError("rebase reparented task", rebErr)
		}
	}
	reb, rebErr := q.svc.git.Rebase(ctx, worktreeDir, targetBranch)
	return reb, boundaryError("rebase task", rebErr)
}
