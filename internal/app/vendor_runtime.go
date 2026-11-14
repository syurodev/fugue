package app

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
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
			owner.SetVaultLocked(snap.State != vault.StateUnlocked)
		})
	}
	hp.rt.transcripts = transcripts
	hp.vendorStore = vs
	hp.closeVendor = func() error {
		if unregisterVaultGuard != nil {
			unregisterVaultGuard()
		}
		// Stops run on the host's lifetime context without its cancellation,
		// so a host close never skips the F9.2 stop order.
		err := errors.Join(sessions.CloseAll(context.WithoutCancel(base)), transcripts.Close(), vs.Close())
		hp.rt.sessionTools.tokens.revokeAll()
		return err
	}
	return nil
}

func composeVendorSessions(ctx, base context.Context, hp *HostProcess, vs *vendorstore.Store, transcripts *Transcripts, v *vault.Service, deps *HostDeps) (*VendorSessions, error) {
	if err := transcripts.RegisterStoredSessions(ctx); err != nil {
		return nil, err
	}
	topic, err := newVendorSessionsTopic(ctx, vs, hp.rt.hub)
	if err != nil {
		return nil, err
	}
	options := []claude.Option{
		claude.WithClock(deps.Clock),
		claude.WithIDs(deps.IDs),
		// One systemd probe per owner (the vendor runtime), never per spawn:
		// the probe caches its capability check, so every session of this host
		// reuses the answer (F9.2b review note).
		claude.WithSystemdProbe(process.NewSystemdProbe()),
	}
	if deps.VendorBinary != "" {
		options = append(options, claude.WithBinary(deps.VendorBinary))
	}
	// The launcher's XDG_RUNTIME_DIR is the raw value of the injected
	// environment, never config.RuntimeDir, whose per-user fallback is not a
	// systemd runtime dir (F9.2b review note). Empty leaves containment
	// unattempted, which is the stage-A macOS path.
	if dir := rawXDGRuntimeDir(deps.VendorHostEnv); dir != "" {
		options = append(options, claude.WithXDGRuntimeDir(dir))
	}
	adapter := claude.NewAdapter(deps.VendorHostEnv, options...)
	// A session.s token dies with its actor, with a purge and with the host.
	tokens := newSessionTokens(deps.Random)
	transcripts.onPurge = tokens.revoke
	hp.vendorAdapter = adapter
	sessions, err := NewVendorSessions(base, &VendorSessionDeps{
		Store: topicNotifyingStore{Store: vs, topic: topic}, Transcripts: transcripts, Adapter: adapter,
		Vault: v, Clock: deps.Clock, IDs: deps.IDs, Logger: slog.Default(), OnSessionEnded: tokens.revoke,
		TestDelaySpawnRegistration: deps.TestDelaySpawnRegistration,
		TestOnWaitInflightSpawn:    deps.TestOnWaitInflightSpawn,
	})
	if err != nil {
		return nil, err
	}
	hp.rt.sessionTools = newSessionToolService(tokens, deps.TestFugueTools, vs, hp.rt.interactions, hp.rt.tags, deps.IDs)
	hp.rt.vendors = &VendorService{
		sessions: sessions, store: vs, adapter: adapter, vault: v, workspaces: hp.rt.workspaces.domain,
		ids: deps.IDs, hostEnv: deps.VendorHostEnv, ops: newVendorOps(deps.Clock, hp.rt.goTask),
		tokens: tokens, bridge: deps.Bridge,
	}
	return sessions, nil
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

// TranscriptService returns the composed transcript read model.
func (rt *HostRuntime) TranscriptService() (*Transcripts, error) {
	if rt.transcripts == nil {
		return nil, errcode.New(errcode.Unavailable, "the transcript service is unavailable")
	}
	return rt.transcripts, nil
}
