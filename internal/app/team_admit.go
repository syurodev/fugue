package app

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// maxRunningDirectChildren is the §11 limit of direct child tasks one
// delegator may have working at once (Q-118).
const maxRunningDirectChildren = 4

// Admitter decides whether the dispatcher may start one more task now. A
// refusal leaves the task queued, never failed.
type Admitter interface {
	Admit(ctx context.Context, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error)
}

// capacityReader reports whether the vendor layer has room for one more
// session.
type capacityReader interface {
	HasCapacity(ctx context.Context) (bool, error)
}

type accountCapacityReader interface {
	capacityReader
	HasCapacityFor(ctx context.Context, vendorName, accountID string) (bool, error)
}

// localAdmitter checks the direct-child cap and host capacity.
type localAdmitter struct{ capacity capacityReader }

// Admit admits a task while the host has session capacity and its delegator
// has fewer than maxRunningDirectChildren children working.
func (a localAdmitter) Admit(ctx context.Context, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error) {
	// Binding the lead spawns no session, so host capacity never holds it.
	if task.HierarchyRole == vendorstore.RoleLead {
		return true, nil
	}
	if workingDirectChildren(task, tasks) >= maxRunningDirectChildren {
		return false, nil
	}
	room, err := a.capacity.HasCapacity(ctx)
	if err != nil {
		return false, boundaryError("read the session capacity", err)
	}
	return room, nil
}

type schedulerAdmitter struct {
	capacity accountCapacityReader
	store    *vendorstore.Store
	accounts *vendor.Accounts
	picker   *accountPicker
}

func newSchedulerAdmitter(capacity accountCapacityReader, store *vendorstore.Store, accounts *vendor.Accounts) Admitter {
	return schedulerAdmitter{
		capacity: capacity,
		store:    store,
		accounts: accounts,
		picker:   newAccountPicker(accounts, nil, nil),
	}
}

func newSchedulerAdmitterWithConfig(capacity accountCapacityReader, store *vendorstore.Store, accounts *vendor.Accounts, hostCfg HostConfigReader, clk clock.Clock) Admitter {
	return schedulerAdmitter{
		capacity: capacity,
		store:    store,
		accounts: accounts,
		picker:   newAccountPicker(accounts, hostCfg, clk),
	}
}

// Admit checks the §11 4-child cap (Q-118), the §8 85% quota gate (OR-09),
// and the §7 per-account and machine-wide session tiers for the task's resolved vendor.
func (a schedulerAdmitter) Admit(ctx context.Context, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (bool, error) {
	if task.HierarchyRole == vendorstore.RoleLead {
		return true, nil
	}
	if workingDirectChildren(task, tasks) >= maxRunningDirectChildren {
		return false, nil
	}
	vendorName, modelName := "", ""
	if a.store != nil {
		_, role, code, err := resolveTaskProfile(ctx, a.store, task.TeamID, task.TaskID, task.CatalogRole)
		if err != nil {
			return false, err
		}
		if code != "" {
			// Let startChild fail the invalid task with its stable failure code.
			return true, nil
		}
		vendorName, modelName = role.Vendor, role.Model
	}
	if vendorName == "" {
		room, err := a.capacity.HasCapacity(ctx)
		if err != nil {
			return false, boundaryError("read the session capacity", err)
		}
		return room, nil
	}
	return a.admitVendorCapacity(ctx, vendorName, modelName)
}

func (a schedulerAdmitter) admitVendorCapacity(ctx context.Context, vendorName, modelName string) (bool, error) {
	if a.picker != nil && a.accounts != nil {
		room, ok, err := a.picker.PickWithCapacity(ctx, a.capacity, vendorName, modelName)
		if err != nil {
			return false, boundaryError("read the session capacity", err)
		}
		return ok && room != "", nil
	}
	if a.accounts != nil && !a.accounts.QuotaAllows(vendorName) {
		return false, nil
	}
	room, err := a.capacity.HasCapacityFor(ctx, vendorName, vendorDefaultAccount)
	if err != nil {
		return false, boundaryError("read the session capacity", err)
	}
	return room, nil
}

func workingDirectChildren(task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) int {
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
	return working
}
