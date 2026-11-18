package app

import (
	"context"

	"github.com/syurodev/fugue/internal/vendorstore"
)

// maxRunningDirectChildren is the §11 limit of direct child tasks one
// delegator may have working at once, kept as a fixed safety cap until the
// F11.6 scheduler reads it from the workspace configuration (Q-118).
const maxRunningDirectChildren = 4

// Admitter decides whether the dispatcher may start one more task now. It is
// the boundary F11.6 replaces with the three slot tiers, the quota gate and
// the budgets; F11.3 admits on host capacity and the direct-child cap only
// (Q-118). A refusal leaves the task queued, never failed.
type Admitter interface {
	Admit(ctx context.Context, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error)
}

// capacityReader reports whether the vendor layer has room for one more
// session.
type capacityReader interface {
	HasCapacity(ctx context.Context) (bool, error)
}

// localAdmitter is the F11.3 Admitter.
type localAdmitter struct{ capacity capacityReader }

// Admit admits a task while the host has session capacity and its delegator
// has fewer than maxRunningDirectChildren children working.
func (a localAdmitter) Admit(ctx context.Context, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error) {
	// Binding the lead spawns no session, so host capacity never holds it.
	if task.HierarchyRole == vendorstore.RoleLead {
		return true, nil
	}
	working := 0
	for i := range tasks {
		if tasks[i].ParentTaskID != task.ParentTaskID || tasks[i].TaskID == task.TaskID {
			continue
		}
		switch tasks[i].State {
		case vendorstore.TaskStateStarting, vendorstore.TaskStateRunning, vendorstore.TaskStateAsking, vendorstore.TaskStateChecking:
			working++
		case vendorstore.TaskStateQueued, vendorstore.TaskStateReview, vendorstore.TaskStateIntegrating, vendorstore.TaskStateDone,
			vendorstore.TaskStateFailed, vendorstore.TaskStateStopped, vendorstore.TaskStateCancelled, vendorstore.TaskStateUnknown:
		}
	}
	if working >= maxRunningDirectChildren {
		return false, nil
	}
	room, err := a.capacity.HasCapacity(ctx)
	if err != nil {
		return false, boundaryError("read the session capacity", err)
	}
	return room, nil
}
