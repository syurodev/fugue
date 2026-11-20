package app

import (
	"cmp"
	"context"
	"fmt"
	"slices"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// QueuedTaskPosition describes a queued non-explicit-start task in the global
// dispatch queue (§7, Q-144). Startable tasks (checked via resolveTaskChain
// and startable, matching pickTeamCandidate) receive 1-based QueuePosition
// values; queued tasks that are currently blocked (parent not running/asking or
// unmet dependencies) are returned with Blocked=true and QueuePosition=0.
type QueuedTaskPosition struct {
	TeamID        string
	TaskID        string
	HierarchyRole vendorstore.TeamRole
	Depth         int
	CreatedAt     int64
	QueuePosition int
	Blocked       bool
}

type queuedTaskKey struct{ teamID, taskID string }

type globalTaskCandidate struct {
	rec   vendorstore.TeamRecord
	task  vendorstore.TaskRecord
	tasks []vendorstore.TaskRecord
}

// DispatchAll dispatches startable queued tasks across every active team in
// global §7 priority order (leads, managers, depth-1 members, deeper members,
// then CreatedAt ASC).
func (d *teamDispatcher) DispatchAll(ctx context.Context) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.VendorVaultLocked, "the vault is locked")
	}
	held := make(map[queuedTaskKey]bool)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("dispatch active teams: %w", err)
		}
		cand, ok, err := d.nextGlobalCandidate(ctx, held)
		if err != nil || !ok {
			return err
		}
		started, err := d.start(ctx, &cand.rec, &cand.task, cand.tasks)
		if err != nil {
			return err
		}
		if !started {
			held[queuedTaskKey{teamID: cand.rec.TeamID, taskID: cand.task.TaskID}] = true
		}
	}
}

func (d *teamDispatcher) nextGlobalCandidate(ctx context.Context, held map[queuedTaskKey]bool) (*globalTaskCandidate, bool, error) {
	teamIDs, err := d.deps.Store.ActiveTeamIDs(ctx)
	if err != nil {
		return nil, false, boundaryError("list the active teams", err)
	}
	var best *globalTaskCandidate
	for _, id := range teamIDs {
		rec, tasks, err := d.deps.Store.TeamSnapshot(ctx, id)
		if err != nil {
			return nil, false, boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive || rec.WaitingReason != "" {
			continue
		}
		cand := pickTeamCandidate(tasks, func(taskID string) bool {
			return held[queuedTaskKey{teamID: rec.TeamID, taskID: taskID}]
		})
		if cand == nil {
			continue
		}
		if best == nil || compareGlobalTasks(rec.TeamID, cand, best.rec.TeamID, &best.task) < 0 {
			best = &globalTaskCandidate{rec: rec, task: *cand, tasks: tasks}
		}
	}
	return best, best != nil, nil
}

// QueueSnapshot returns queued non-explicit-start tasks across all active teams
// in global §7 dispatch priority order. Startable tasks (determined by the same
// resolveTaskChain and startable checks as pickTeamCandidate) are listed first
// with 1-based QueuePosition values; blocked queued tasks (parent not
// running/asking or unmet dependencies) follow with Blocked=true and
// QueuePosition=0.
func (d *teamDispatcher) QueueSnapshot(ctx context.Context) ([]QueuedTaskPosition, error) {
	teamIDs, err := d.deps.Store.ActiveTeamIDs(ctx)
	if err != nil {
		return nil, boundaryError("list the active teams", err)
	}
	var queued []QueuedTaskPosition
	for _, id := range teamIDs {
		rec, tasks, err := d.deps.Store.TeamSnapshot(ctx, id)
		if err != nil {
			return nil, boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive {
			continue
		}
		queued = appendTeamQueuePositions(queued, tasks, rec.WaitingReason != "")
	}
	return sortAndNumberQueue(queued), nil
}

func appendTeamQueuePositions(dst []QueuedTaskPosition, tasks []vendorstore.TaskRecord, teamWaiting bool) []QueuedTaskPosition {
	byID, retryChild := resolveTaskChain(tasks)
	for i := range tasks {
		t := &tasks[i]
		if t.State != vendorstore.TaskStateQueued || t.ExplicitStart {
			continue
		}
		dst = append(dst, QueuedTaskPosition{
			TeamID: t.TeamID, TaskID: t.TaskID, HierarchyRole: t.HierarchyRole,
			Depth: t.Depth, CreatedAt: t.CreatedAt, Blocked: teamWaiting || !startable(t, byID, retryChild),
		})
	}
	return dst
}

func sortAndNumberQueue(queued []QueuedTaskPosition) []QueuedTaskPosition {
	order := make([]*QueuedTaskPosition, len(queued))
	for i := range queued {
		order[i] = &queued[i]
	}
	slices.SortFunc(order, compareQueuedTaskPositions)
	sorted := make([]QueuedTaskPosition, len(order))
	pos := 0
	for i, item := range order {
		sorted[i] = *item
		if sorted[i].Blocked {
			continue
		}
		pos++
		sorted[i].QueuePosition = pos
	}
	return sorted
}

func compareQueuedTaskPositions(a, b *QueuedTaskPosition) int {
	return cmp.Or(
		cmp.Compare(boolSortOrder(a.Blocked), boolSortOrder(b.Blocked)),
		cmp.Compare(rolePriorityTier(a.HierarchyRole, a.Depth), rolePriorityTier(b.HierarchyRole, b.Depth)),
		cmp.Compare(a.CreatedAt, b.CreatedAt),
		cmp.Compare(a.TaskID, b.TaskID),
		cmp.Compare(a.TeamID, b.TeamID),
	)
}

func boolSortOrder(v bool) int {
	if v {
		return 1
	}
	return 0
}

// nextStartable picks the highest-priority queued task the plan starts on its
// own (Q-116, Q-144): its parent is working and every dependency is done.
func nextStartable(tasks []vendorstore.TaskRecord, held map[string]bool) *vendorstore.TaskRecord {
	return pickTeamCandidate(tasks, func(taskID string) bool { return held[taskID] })
}

func pickTeamCandidate(tasks []vendorstore.TaskRecord, isHeld func(string) bool) *vendorstore.TaskRecord {
	byID, retryChild := resolveTaskChain(tasks)
	var best *vendorstore.TaskRecord
	for i := range tasks {
		t := &tasks[i]
		if t.State != vendorstore.TaskStateQueued || t.ExplicitStart || isHeld(t.TaskID) {
			continue
		}
		if !startable(t, byID, retryChild) {
			continue
		}
		if best == nil || compareTaskRecords(t, best) < 0 {
			best = t
		}
	}
	return best
}

func rolePriorityTier(role vendorstore.TeamRole, depth int) int {
	switch role {
	case vendorstore.RoleLead:
		return 0
	case vendorstore.RoleTeamManager:
		return 1
	case vendorstore.RoleMember:
		if depth <= 1 {
			return 2
		}
		return 3
	case vendorstore.RoleSolo:
		return 4
	default:
		return 4
	}
}

func compareTaskRecords(a, b *vendorstore.TaskRecord) int {
	return cmp.Or(
		cmp.Compare(rolePriorityTier(a.HierarchyRole, a.Depth), rolePriorityTier(b.HierarchyRole, b.Depth)),
		cmp.Compare(a.CreatedAt, b.CreatedAt),
		cmp.Compare(a.TaskID, b.TaskID),
	)
}

func compareGlobalTasks(teamA string, a *vendorstore.TaskRecord, teamB string, b *vendorstore.TaskRecord) int {
	return cmp.Or(compareTaskRecords(a, b), cmp.Compare(teamA, teamB))
}
