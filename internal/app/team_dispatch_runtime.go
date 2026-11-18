package app

import (
	"context"
	"log/slog"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// locateTeamGit builds the git manager of the team worktrees. A machine
// without a usable git, or a data directory the manager refuses, yields nil:
// a team task that needs a worktree then fails closed (F11.3a, Q-119).
func locateTeamGit(ctx context.Context, deps *HostDeps) *gitops.Manager {
	path := deps.GitBinary
	if path == "" {
		path = snapshot.Locate(ctx).Path
	}
	if path == "" {
		return nil
	}
	m, err := gitops.NewManager(path, deps.DataDir, deps.Clock)
	if err != nil {
		slog.WarnContext(ctx, "the team git manager is unavailable", "error_code", string(errcode.CodeOf(err)))
		return nil
	}
	return m
}

// dispatchTeamAsync runs one dispatch of a team on the host's lifetime; the
// decision that activated the team never waits for sessions to spawn. A failed
// run is logged by its code only and retried by the next trigger.
func (hp *HostProcess) dispatchTeamAsync(base context.Context, teamID string) {
	supervise.Go(base, "team-dispatch-"+teamID, func(ctx context.Context) error {
		hp.logDispatch(ctx, hp.rt.teamDispatch.Dispatch(ctx, teamID))
		return nil
	})
}

// dispatchAllAsync dispatches every active team, as the vault unlock does.
func (hp *HostProcess) dispatchAllAsync(base context.Context) {
	supervise.Go(base, "team-dispatch-all", func(ctx context.Context) error {
		hp.logDispatch(ctx, hp.rt.teamDispatch.DispatchAll(ctx))
		return nil
	})
}

func (hp *HostProcess) logDispatch(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "dispatching team tasks failed", "error_code", string(errcode.CodeOf(err)))
	}
}

// wireTeamDispatch builds the dispatcher and hands every newly active team
// to it.
func (hp *HostProcess) wireTeamDispatch(ctx, base context.Context, vs *vendorstore.Store, sessions *VendorSessions, v *vault.Service, deps *HostDeps) {
	admit := deps.TeamAdmitter
	if admit == nil {
		admit = localAdmitter{capacity: sessions}
	}
	hp.rt.teamDispatch = newTeamDispatcher(&teamDispatcherDeps{
		Store: vs, Sessions: vendorTaskSessions{svc: hp.rt.vendors}, Admit: admit,
		Roots: hp.rt.workspaces.domain, Git: locateTeamGit(ctx, deps), DataDir: deps.DataDir, IDs: deps.IDs, Vault: v,
	})
	hp.rt.teamProposals.SetOnTeamActive(func(teamID string) { hp.dispatchTeamAsync(base, teamID) })
}
