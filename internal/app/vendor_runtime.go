package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
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
	transcripts, err := NewTranscripts(base, TranscriptDeps{Store: vs, Hub: hp.rt.hub, Vault: v, Clock: deps.Clock})
	if err != nil {
		return errors.Join(err, vs.Close())
	}
	sessions, err := composeVendorSessions(ctx, base, hp, vs, transcripts, v, deps)
	if err != nil {
		return errors.Join(err, transcripts.Close(), vs.Close())
	}
	hp.vault = v
	hp.rt.transcripts = transcripts
	hp.vendorStore = vs
	hp.closeVendor = func() error {
		// Stops run on the host's lifetime context without its cancellation,
		// so a host close never skips the F9.2 stop order.
		return errors.Join(sessions.CloseAll(context.WithoutCancel(base)), transcripts.Close(), vs.Close())
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
	options := []claude.Option{claude.WithClock(deps.Clock), claude.WithIDs(deps.IDs)}
	if deps.VendorBinary != "" {
		options = append(options, claude.WithBinary(deps.VendorBinary))
	}
	adapter := claude.NewAdapter(deps.VendorHostEnv, options...)
	sessions, err := NewVendorSessions(base, &VendorSessionDeps{
		Store: topicNotifyingStore{Store: vs, topic: topic}, Transcripts: transcripts, Adapter: adapter,
		Vault: v, Clock: deps.Clock, IDs: deps.IDs, Logger: slog.Default(),
	})
	if err != nil {
		return nil, err
	}
	hp.rt.vendors = &VendorService{
		sessions: sessions, store: vs, adapter: adapter, vault: v, workspaces: hp.rt.workspaces.domain,
		ids: deps.IDs, hostEnv: deps.VendorHostEnv, ops: newVendorOps(deps.Clock, hp.rt.goTask),
	}
	return sessions, nil
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
