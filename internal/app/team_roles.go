package app

import (
	"context"

	"github.com/syurodev/fugue/internal/vendorstore"
)

// sessionTeamRole resolves the team role of a session from the store: the
// task row of a task session, the lead role of a session that owns an active
// team, and the empty role of a plain session. A token's role is fixed at
// spawn, so a solo session that became lead has no other source; the caller's
// claims never decide this (Q-117, authorization in Go).
func sessionTeamRole(ctx context.Context, store *vendorstore.Store, sessionID string) (vendorstore.TeamRole, error) {
	task, found, err := store.TaskForSession(ctx, sessionID)
	if err != nil {
		return "", boundaryError("read the session's task", err)
	}
	if found {
		return task.HierarchyRole, nil
	}
	_, active, err := store.ActiveTeamForSession(ctx, sessionID)
	if err != nil {
		return "", boundaryError("read the session's team", err)
	}
	if active {
		return vendorstore.RoleLead, nil
	}
	return "", nil
}
