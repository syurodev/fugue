package app

import (
	"context"
	"log/slog"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/snapshot"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// onVaultLockState mirrors the vault state into the services that gate on it,
// and on an unlock starts the queued tasks and delivers the mail the lock
// held (Q-123).
func (hp *HostProcess) onVaultLockState(owner *interaction.Owner, snap vault.Snapshot) {
	unlocked := snap.State == vault.StateUnlocked
	owner.SetVaultLocked(!unlocked)
	if !unlocked {
		return
	}
	if hp.rt.teamBudgets != nil {
		hp.rt.teamBudgets.Notify()
	}
	if hp.rt.teamProposals != nil || hp.rt.teamTasks != nil {
		hp.recoverOnUnlockAsync()
	}
	if hp.rt.teamDispatch != nil {
		hp.dispatchAllAsync()
	}
	if hp.rt.teamWake != nil {
		hp.rt.teamWake.Notify()
	}
}

func (hp *HostProcess) recoverOnUnlockAsync() {
	hp.rt.goTask("team-unlock-recovery", func(ctx context.Context) error {
		if hp.rt.teamProposals != nil {
			if err := hp.rt.teamProposals.RebuildPendingProposalCards(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "rebuilding pending proposal cards failed", "error_code", string(errcode.CodeOf(err)))
			}
		}
		if hp.rt.teamTasks != nil {
			if err := hp.rt.teamTasks.RecoverOnUnlock(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "recovering unknown team tasks on unlock failed", "error_code", string(errcode.CodeOf(err)))
			}
		}
		return nil
	})
}

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
func (hp *HostProcess) dispatchTeamAsync(teamID string) {
	hp.rt.goTask("team-dispatch-"+teamID, func(ctx context.Context) error {
		hp.logDispatch(ctx, hp.rt.teamDispatch.Dispatch(ctx, teamID))
		return nil
	})
}

// dispatchAllAsync dispatches every active team, as the vault unlock does.
func (hp *HostProcess) dispatchAllAsync() {
	hp.rt.goTask("team-dispatch-all", func(ctx context.Context) error {
		hp.logDispatch(ctx, hp.rt.teamDispatch.DispatchAll(ctx))
		return nil
	})
}

func (hp *HostProcess) logDispatch(ctx context.Context, err error) {
	if err != nil && ctx.Err() == nil {
		slog.WarnContext(ctx, "dispatching team tasks failed", "error_code", string(errcode.CodeOf(err)))
	}
}

// wireTeamDispatch builds the dispatcher and the wake pump and hands every
// newly active team to the former and every mailbox turn to the latter.
func (hp *HostProcess) wireTeamDispatch(ctx, base context.Context, vs *vendorstore.Store, sessions *VendorSessions, accounts *vendor.Accounts, v *vault.Service, deps *HostDeps) {
	admit := deps.TeamAdmitter
	if admit == nil {
		admit = newSchedulerAdmitter(sessions, vs, accounts)
	}
	teamsCfg := globalOrWorkspaceTeamsConfigReader(hp.rt.workspaces)
	sessions.SetWakeHook(func(sessionID string) { hp.rt.teamWake.NotifySessionReady(sessionID) })
	var recoverLead func(context.Context, string) error
	if hp.rt.teamTasks != nil {
		recoverLead = hp.rt.teamTasks.OnSessionResumed
	}
	hp.rt.teamWake = newTeamWake(&teamWakeDeps{
		Store: vs, Sessions: vendorTaskSessions{svc: hp.rt.vendors}, Roots: hp.rt.workspaces.domain,
		Clock: deps.Clock, IDs: deps.IDs, Vault: v, Hub: hp.rt.hub, DataDir: deps.DataDir,
		RecoverLead: recoverLead,
	})
	hp.rt.teamWake.Start(base)
	hp.rt.teamBudgets = newTeamBudgets(&teamBudgetsDeps{
		Store: vs, Sessions: vendorTaskSessions{svc: hp.rt.vendors}, Interactions: hp.rt.interactions,
		TeamsConfig: teamsCfg, Vault: v, Clock: deps.Clock, IDs: deps.IDs, Wake: hp.rt.teamWake,
		OnTeamResume: func(teamID string) { hp.dispatchTeamAsync(teamID) },
	})
	if hp.rt.interactions != nil {
		hp.rt.interactions.SetTeamDeadlineResolver(hp.rt.teamBudgets)
	}
	hp.rt.teamBudgets.Start(base)
	sessions.SetObservationHook(func(sessionID string) {
		hp.rt.teamBudgets.ObserveSession(sessionID)
	})
	sessions.SetTurnStartedHook(func(sessionID string) {
		hp.rt.goTask("team.turn-budget-check", func(checkCtx context.Context) error {
			hp.rt.teamBudgets.OnTurnStarted(checkCtx, sessionID)
			return nil
		})
	})
	hp.rt.teamDispatch = newTeamDispatcher(&teamDispatcherDeps{
		Store: vs, Sessions: vendorTaskSessions{svc: hp.rt.vendors}, Admit: admit,
		Roots: hp.rt.workspaces.domain, Git: locateTeamGit(ctx, deps), DataDir: deps.DataDir, IDs: deps.IDs, Vault: v,
		Wake: hp.rt.teamWake, Clock: deps.Clock, TeamsConfig: teamsCfg, Budgets: hp.rt.teamBudgets,
		Disk: resolveDiskSpace(deps.DiskSpace),
	})
	hp.rt.teamDispatch.Start(base)
	if hp.rt.teamTasks != nil {
		hp.wireTeamTaskHooks(base, sessions, deps)
	}
	hp.rt.teamProposals.SetClock(deps.Clock)
	hp.rt.teamProposals.SetOnTimerArmed(func() { hp.rt.teamBudgets.Notify() })
	hp.rt.teamProposals.SetOnTeamActive(func(teamID string) {
		hp.rt.teamBudgets.Notify()
		hp.dispatchTeamAsync(teamID)
	})
	hp.rt.teamProposals.SetOnMailbox(func() { hp.rt.teamWake.Notify() })
}

func (hp *HostProcess) wireTeamTaskHooks(base context.Context, sessions *VendorSessions, deps *HostDeps) {
	hp.rt.teamTasks.SetRuntime(hp.rt.teamDispatch, hp.rt.teamWake)
	hp.rt.teamTasks.SetSessions(vendorTaskSessions{svc: hp.rt.vendors})
	hp.rt.teamTasks.SetTeamsConfig(globalOrWorkspaceTeamsConfigReader(hp.rt.workspaces))
	hp.rt.teamTasks.SetBudgets(hp.rt.teamBudgets)
	hp.rt.teamTasks.SetClock(deps.Clock)
	mq := newTeamMergeQueue(hp.rt.teamTasks)
	hp.rt.teamTasks.SetIntegration(hp.rt.workspaces.domain, mq)
	mq.Start(base)
	hp.rt.vendors.SetOnSessionResumed(func(resCtx context.Context, sessionID string) error {
		return hp.rt.teamTasks.OnSessionResumed(resCtx, sessionID)
	})
	sessions.SetTurnCompletedHook(func(sessionID string) {
		hp.rt.goTask("team.no-report-check", func(checkCtx context.Context) error {
			hp.rt.teamTasks.OnTurnCompleted(checkCtx, sessionID)
			return nil
		})
	})
	sessions.SetTurnFailedHook(func(sessionID, failureCode string) {
		hp.rt.goTask("team.turn-failure-check", func(checkCtx context.Context) error {
			hp.rt.teamTasks.OnTurnFailed(checkCtx, sessionID, failureCode)
			return nil
		})
	})
	sessions.SetSessionDiedHook(func(sessionID string) {
		hp.rt.goTask("team.session-died-check", func(checkCtx context.Context) error {
			hp.rt.teamTasks.OnSessionDied(checkCtx, sessionID)
			return nil
		})
	})
}
