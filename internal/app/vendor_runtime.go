package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/agy"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendoradapter/codex"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// initVendor composes the vendor runtime on the host store: the vault (locked
// on every start; its unlock RPC arrives with F10.5), the vendor store, the
// transcript read model, the Claude adapter, the session actors and the
// vendor-sessions topic. ctx bounds the setup reads; base is the host's
// lifetime context, captured in closures and never stored.
//
// Vendor-session recovery runs here, before StartHost returns and therefore
// before the Host API accepts its first client (vendor-runtime §11): unfinished
// sessions come back detached, open turns turn unknown, and the leftover
// process trees of the previous incarnation are reaped. No session is
// respawned and no turn is replayed; a client brings a detached session back
// through Resume.
func initVendor(ctx, base context.Context, hp *HostProcess, store *storage.Store, deps *HostDeps) error {
	v, err := vault.NewService(ctx, vault.NewSQLKeyringStore(store.DB()), deps.IDs)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "create the host vault", err)
	}
	vs, err := vendorstore.New(ctx, store.DB(), v, deps.Clock, nil)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "open the vendor store", err)
	}
	if _, recErr := vs.RecoverTeams(ctx); recErr != nil {
		return errors.Join(errcode.Wrap(errcode.Internal, "recover team tasks", recErr), vs.Close())
	}
	identities, recErr := vs.Recover(ctx)
	if recErr != nil {
		return errors.Join(errcode.Wrap(errcode.Internal, "recover vendor sessions", recErr), vs.Close())
	}
	reapLeftoverVendorProcesses(ctx, identities, deps)
	transcripts, err := NewTranscripts(base, TranscriptDeps{Store: vs, Hub: hp.rt.hub, Vault: v, Clock: deps.Clock})
	if err != nil {
		return errors.Join(err, vs.Close())
	}
	sessions, err := composeVendorSessions(ctx, base, hp, vs, transcripts, v, deps)
	if err != nil {
		return errors.Join(err, transcripts.Close(), vs.Close())
	}
	hp.vault = v
	hp.vaultRef.Store(v)
	var unregisterVaultGuard func()
	if hp.rt.interactions != nil {
		owner := hp.rt.interactions.owner
		unregisterVaultGuard = v.RegisterLockGuard(func(snap vault.Snapshot) {
			hp.onVaultLockState(base, owner, snap)
		})
	}
	hp.rt.transcripts = transcripts
	hp.vendorStore = vs
	hp.closeVendor = func() error {
		if unregisterVaultGuard != nil {
			unregisterVaultGuard()
		}
		var budgetErr error
		if hp.rt.teamBudgets != nil {
			budgetErr = hp.rt.teamBudgets.Stop()
		}
		// Stops run on the host's lifetime context without its cancellation,
		// so a host close never skips the F9.2 stop order.
		err := errors.Join(budgetErr, sessions.CloseAll(context.WithoutCancel(base)), transcripts.Close(), vs.Close())
		hp.rt.sessionTools.tokens.revokeAll()
		hp.rt.sessionTools.endAllSessions()
		return err
	}
	return nil
}

func composeVendorSessions(ctx, base context.Context, hp *HostProcess, vs *vendorstore.Store, transcripts *Transcripts, v *vault.Service, deps *HostDeps) (*VendorSessions, error) {
	if err := transcripts.RegisterStoredSessions(ctx); err != nil {
		return nil, err
	}
	adapter, adapters := buildVendorAdapters(deps)
	accounts := vendor.NewAccounts(deps.Clock, []vendor.AccountVendor{
		{Name: VendorClaude, DirectQuota: true}, {Name: VendorCodex, DirectQuota: true}, {Name: VendorAGY},
	})
	tokens := newSessionTokens(deps.Random)
	endSession := newSessionEndedHook(base, hp, tokens)
	transcripts.onPurge = endSession
	hp.vendorAdapter = adapter
	sessions, err := composeVendorSessionManager(ctx, base, hp, vs, transcripts, v, adapter, adapters, accounts, deps, endSession)
	if err != nil {
		return nil, err
	}
	wireTeamServices(ctx, hp, vs, sessions, v, tokens, deps)
	hp.rt.vendors = &VendorService{
		sessions: sessions, store: vs, adapter: adapter, adapters: adapters, vault: v, workspaces: hp.rt.workspaces.domain,
		ids: deps.IDs, hostEnv: deps.VendorHostEnv, ops: newVendorOps(deps.Clock, hp.rt.goTask),
		tokens: tokens, bridge: deps.Bridge, accounts: accounts,
	}
	hp.wireTeamDispatch(ctx, base, vs, sessions, accounts, v, deps)
	hp.rt.accountsService, err = newAccountsService(accounts, hp.rt.vendors, hp.rt.hub, accountsServiceDeps{
		Clock: deps.Clock, IDs: deps.IDs, GoTask: hp.rt.goTask,
		OnQuotaReopen: func() { hp.dispatchAllAsync(base) },
	})
	if err != nil {
		return nil, err
	}
	// The proposal card's account line reads the accounts service (F11.2b
	// moved it here from F11.2a).
	hp.rt.teamProposals.SetAccounts(hp.rt.accountsService)
	return sessions, nil
}

func newSessionEndedHook(base context.Context, hp *HostProcess, tokens *sessionTokens) func(string) {
	// A session's token, tombstones and relayed interactions die with its actor, with a purge and with the host.
	return func(sessionID string) {
		tokens.revoke(sessionID)
		if hp.rt.sessionTools != nil {
			hp.rt.sessionTools.endSession(sessionID)
		}
		if hp.rt.interactions != nil && hp.rt.interactions.owner != nil {
			hp.rt.interactions.owner.ResetSession(sessionID)
		}
		hp.cancelTeamProposalOnEnd(base, sessionID)
	}
}

func composeVendorSessionManager(
	ctx, base context.Context,
	hp *HostProcess,
	vs *vendorstore.Store,
	transcripts *Transcripts,
	v *vault.Service,
	adapter vendor.Adapter,
	adapters map[string]vendor.Adapter,
	accounts *vendor.Accounts,
	deps *HostDeps,
	endSession func(string),
) (*VendorSessions, error) {
	topic, err := newVendorSessionsTopic(ctx, vs, hp.rt.hub)
	if err != nil {
		return nil, err
	}
	sessionEndSettled := func(sessionID string) <-chan struct{} {
		if hp.rt.teamProposals == nil {
			return nil
		}
		return hp.rt.teamProposals.SessionEndSettled(sessionID)
	}
	return NewVendorSessions(base, &VendorSessionDeps{
		Store: topicNotifyingStore{Store: vs, topic: topic}, Transcripts: transcripts,
		Adapter: adapter, Adapters: adapters, Accounts: accounts,
		HostResources: hp.rt.hostResources, HostConfig: globalHostConfigReader(hp.rt.workspaces),
		TeamsConfig: globalOrWorkspaceTeamsConfigReader(hp.rt.workspaces),
		HasPendingApproval: func(sessionID string) bool {
			return hp.rt.interactions != nil && hp.rt.interactions.owner.HasPendingForSession(sessionID)
		},
		Vault: v, Clock: deps.Clock, IDs: deps.IDs, Logger: slog.Default(), OnSessionEnded: endSession, SessionEndSettled: sessionEndSettled,
		TestDelaySpawnRegistration: deps.TestDelaySpawnRegistration,
		TestOnWaitInflightSpawn:    deps.TestOnWaitInflightSpawn,
	})
}

func wireTeamServices(
	ctx context.Context,
	hp *HostProcess,
	vs *vendorstore.Store,
	sessions *VendorSessions,
	v *vault.Service,
	tokens *sessionTokens,
	deps *HostDeps,
) {
	hp.rt.teamProposals = newTeamProposalService(vs, sessions, hp.rt.workspaces.domain, hp.rt.interactions, nopUsageHistory{}, deps.IDs, tokens.liveSession)
	// The interaction service consumes the answers of the team proposal, amend,
	// and task-question cards; wiring happens before any client can answer.
	hp.rt.interactions.SetTeamCardResolver(hp.rt.teamProposals)
	hp.rt.teamTasks = newTeamTaskService(vs, vendorTaskSessions{svc: &VendorService{sessions: sessions, store: vs}}, hp.rt.workspaces.domain, locateTeamGit(ctx, deps), deps.DataDir, deps.IDs, v, deps.TeamResultGate)
	hp.rt.teamTasks.SetInteractions(hp.rt.interactions)
	hp.rt.interactions.SetTaskQuestionResolver(hp.rt.teamTasks)
	tools := append(slices.Clone(deps.TestFugueTools), teamProposeTool(hp.rt.teamProposals), teamAmendTool(hp.rt.teamProposals))
	tools = append(tools, teamTaskTools(hp.rt.teamTasks)...)
	hp.rt.sessionTools = newSessionToolService(tokens, tools, vs, hp.rt.interactions, hp.rt.tags, deps.IDs, sessions)
	sessions.SetRelay(hp.rt.sessionTools)
}

func buildVendorAdapters(deps *HostDeps) (*claude.Adapter, map[string]vendor.Adapter) {
	// One systemd probe per owner (the vendor runtime), never per spawn:
	// the probe caches its capability check, so every session of this host
	// reuses the answer (F9.2b review note).
	systemdProbe := process.NewSystemdProbe()
	xdgDir := rawXDGRuntimeDir(deps.VendorHostEnv)

	claudeOpts := []claude.Option{
		claude.WithClock(deps.Clock),
		claude.WithIDs(deps.IDs),
		claude.WithSystemdProbe(systemdProbe),
	}
	if deps.VendorBinary != "" {
		claudeOpts = append(claudeOpts, claude.WithBinary(deps.VendorBinary))
	}
	if xdgDir != "" {
		claudeOpts = append(claudeOpts, claude.WithXDGRuntimeDir(xdgDir))
	}
	claudeAdapter := claude.NewAdapter(deps.VendorHostEnv, claudeOpts...)

	codexOpts := []codex.Option{
		codex.WithClock(deps.Clock),
		codex.WithIDs(deps.IDs),
		codex.WithSystemdProbe(systemdProbe),
	}
	codexBin := deps.CodexBinary
	if codexBin == "" {
		codexBin = deps.VendorBinary
	}
	if codexBin != "" {
		codexOpts = append(codexOpts, codex.WithBinary(codexBin))
	}
	if xdgDir != "" {
		codexOpts = append(codexOpts, codex.WithXDGRuntimeDir(xdgDir))
	}
	codexAdapter := codex.NewAdapter(deps.VendorHostEnv, codexOpts...)
	agyAdapter := buildAgyAdapter(deps, systemdProbe, xdgDir)

	return claudeAdapter, map[string]vendor.Adapter{
		VendorClaude: claudeAdapter,
		VendorCodex:  codexAdapter,
		VendorAGY:    agyAdapter,
	}
}

func buildAgyAdapter(deps *HostDeps, systemdProbe *process.SystemdProbe, xdgDir string) *agy.Adapter {
	opts := []agy.Option{
		agy.WithClock(deps.Clock),
		agy.WithIDs(deps.IDs),
		agy.WithSystemdProbe(systemdProbe),
	}
	bin := deps.AgyBinary
	if bin == "" {
		bin = deps.VendorBinary
	}
	if bin != "" {
		opts = append(opts, agy.WithBinary(bin))
	}
	if xdgDir != "" {
		opts = append(opts, agy.WithXDGRuntimeDir(xdgDir))
	}
	if deps.Config.HostVersion != "" {
		opts = append(opts, agy.WithClientVersion(deps.Config.HostVersion))
	}
	return agy.NewAdapter(deps.VendorHostEnv, opts...)
}

// reapLeftoverVendorProcesses kills the process trees the previous host
// incarnation recorded (vendor-runtime §11): the store verified their identity
// when it read them, and Reap verifies the start token again before it signals
// anything, so a recycled PID is never killed. A leftover that cannot be
// reaped is logged and left for the session's close or resume paths; startup
// never fails for it.
func reapLeftoverVendorProcesses(ctx context.Context, identities []vendorstore.ProcessIdentity, deps *HostDeps) {
	for _, id := range identities {
		identity := process.Identity{PID: int(id.PID), StartToken: id.StartToken, ScopeUnit: id.ScopeUnit}
		if _, err := process.Reap(ctx, identity, &process.ReapOptions{Clock: deps.Clock}); err != nil {
			slog.Warn("reaping a leftover vendor process failed",
				"error_code", string(errcode.CodeOf(err)))
		}
	}
}

// rawXDGRuntimeDir reads XDG_RUNTIME_DIR straight out of the injected host
// environment snapshot, without config's absolute-path fallbacks.
func rawXDGRuntimeDir(env []string) string {
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, config.EnvRuntimeDir+"="); ok {
			return value
		}
	}
	return ""
}

// Vault returns the host's vault. Clients cannot unlock it before F10.5; the
// composition root and tests hold it.
func (hp *HostProcess) Vault() *vault.Service { return hp.vault }

// VendorService returns the vendor session service the Host API serves.
func (rt *HostRuntime) VendorService() (*VendorService, error) {
	if rt.vendors == nil {
		return nil, errcode.New(errcode.Unavailable, "the vendor service is unavailable")
	}
	return rt.vendors, nil
}

// AccountsService returns the vendor accounts service the Host API serves.
func (rt *HostRuntime) AccountsService() (*AccountsService, error) {
	if rt.accountsService == nil {
		return nil, errcode.New(errcode.Unavailable, "the accounts service is unavailable")
	}
	return rt.accountsService, nil
}

// TranscriptService returns the composed transcript read model.
func (rt *HostRuntime) TranscriptService() (*Transcripts, error) {
	if rt.transcripts == nil {
		return nil, errcode.New(errcode.Unavailable, "the transcript service is unavailable")
	}
	return rt.transcripts, nil
}

// cancelTeamProposalOnEnd cancels the session's pending proposal when its
// session ends (Q-104). The cancel runs off the close path on its own
// goroutine, on the host's base context so a shutdown stops it: a stopped
// cancel leaves the proposal pending, the fail-closed side. endSession fires
// inside requestClose while the actor and the close sequence still write, and
// the single SQLite connection serializes writers; the session's actor holds
// its final close writes until the cancel settles (SessionEndSettled),
// because the store refuses the item flip once the session row is closed.
func (hp *HostProcess) cancelTeamProposalOnEnd(base context.Context, sessionID string) {
	if hp.rt.teamProposals == nil {
		return
	}
	settled := hp.rt.teamProposals.BeginSessionEnd(sessionID)
	supervise.Go(base, "team-cancel-"+sessionID, func(taskCtx context.Context) error {
		defer settled()
		if cancelErr := hp.rt.teamProposals.CancelForSession(taskCtx, sessionID); cancelErr != nil && taskCtx.Err() == nil {
			slog.WarnContext(taskCtx, "cancelling the pending team proposal failed",
				"session_id", sessionID, "error_code", string(errcode.CodeOf(cancelErr)))
		}
		return nil
	})
}
