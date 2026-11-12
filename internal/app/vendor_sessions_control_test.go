package app

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func TestSendAppendFailureClosesSession(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	// SetCommitFault is persistent across appends, so it would also reject the
	// close event that must durably follow the failed turn start. Inject only
	// VendorTurnStarted here and let the close event use the real store.
	store := &failAppendVendorStore{Store: f.store, eventType: vendorstore.VendorTurnStarted}
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	err := manager.Send(t.Context(), testActorSessionID, vendor.Input{Text: "fail append and close"})
	if err == nil {
		t.Fatal("Send succeeded after turn append failure")
	}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateClosed
	})
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == vendorstore.VendorTurnStarted {
			t.Fatal("failed turn start was persisted")
		}
	}
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.CloseReason != string(vendor.CloseActorFailure) {
		t.Fatalf("close reason = %q, %v", snap.CloseReason, err)
	}
}

func TestUserInterruptKeepsDrainingObservations(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.interruptStarted = make(chan struct{})
	interruptGate := make(chan struct{})
	session.interruptGate = interruptGate
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "interrupt and drain")
	if err := manager.Interrupt(t.Context(), testActorSessionID); err != nil {
		t.Fatalf("Interrupt RPC = %v", err)
	}
	<-session.interruptStarted
	producer, sent := produceInterruptObservations(t, session)
	awaitActor(t, func() bool { return sent.Load() == 301 })
	if err := producer.Wait(); err != nil {
		t.Fatal(err)
	}
	close(interruptGate)
	session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool { return hasInterruptedTurnEvent(t, f.store) })
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("session after interrupt = %+v, %v; want ready", snap, err)
	}
	mustCloseActorSessions(t, manager)
}

func TestUserInterruptEscalationRecordsUnknown(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.interruptErr = errInjectedAppendFailure
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "interrupt escalation")
	if err := manager.Interrupt(t.Context(), testActorSessionID); err != nil {
		t.Fatalf("Interrupt RPC = %v", err)
	}
	awaitActor(t, func() bool { return hasUnknownTurnEvent(t, f.store) })
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateDetached {
		t.Fatalf("session after interrupt escalation = %+v, %v; want detached", snap, err)
	}
	close(session.observations)
	awaitActor(t, func() bool {
		current, snapshotErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapshotErr == nil && current.State == vendorstore.StateClosed
	})
}

func hasUnknownTurnEvent(t *testing.T, store *vendorstore.Store) bool {
	t.Helper()
	events, err := store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		return false
	}
	for i := range events {
		if events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.Outcome == vendorstore.OutcomeUnknown {
			return true
		}
	}
	return false
}

func produceInterruptObservations(t *testing.T, session *actorTestSession) (*supervise.Task, *atomic.Int32) {
	t.Helper()
	var sent atomic.Int32
	task := supervise.Go(t.Context(), "test-user-interrupt-observations", func(ctx context.Context) error {
		for range 301 {
			select {
			case session.observations <- claude.Observation{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			sent.Add(1)
		}
		return nil
	})
	return task, &sent
}

func hasInterruptedTurnEvent(t *testing.T, store *vendorstore.Store) bool {
	t.Helper()
	events, err := store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		return false
	}
	for i := range events {
		if events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.Outcome == vendorstore.OutcomeInterrupted {
			return true
		}
	}
	return false
}

func TestCloseCompletesWhileSendIsWedged(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.wedgedSend = true
	session.sendStarted = make(chan struct{})
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	sendDone := make(chan error, 1)
	sendTask := supervise.Go(t.Context(), "test-close-wedged-send", func(ctx context.Context) error {
		sendDone <- manager.Send(ctx, testActorSessionID, vendor.Input{Text: strings.Repeat("x", 1<<20)})
		return nil
	})
	<-session.sendStarted
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.Close(ctx, testActorSessionID, vendor.CloseUserRequest); err != nil {
		t.Fatalf("Close while Send wedged = %v", err)
	}
	if err := <-sendDone; !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("Send after Close = %v, want ErrSessionClosed", err)
	}
	if err := sendTask.Wait(); err != nil {
		t.Fatal(err)
	}
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateClosed {
		t.Fatalf("closed session = %+v, %v", snap, err)
	}
	assertClosedWithProof(t, f, process.StopProofProved)
}

func TestCloseAllCompletesWhileSendIsWedged(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.wedgedSend = true
	session.sendStarted = make(chan struct{})
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	sendDone := make(chan error, 1)
	sendTask := supervise.Go(t.Context(), "test-close-all-wedged-send", func(ctx context.Context) error {
		sendDone <- manager.Send(ctx, testActorSessionID, vendor.Input{Text: strings.Repeat("x", 1<<20)})
		return nil
	})
	<-session.sendStarted
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := manager.CloseAll(ctx); err != nil {
		t.Fatalf("CloseAll while Send wedged = %v", err)
	}
	if err := <-sendDone; !errors.Is(err, vendor.ErrSessionClosed) {
		t.Fatalf("Send after CloseAll = %v, want ErrSessionClosed", err)
	}
	if err := sendTask.Wait(); err != nil {
		t.Fatal(err)
	}
	assertClosedWithProof(t, f, process.StopProofProved)
}

func assertClosedWithProof(t *testing.T, f *transcriptFixture, want process.StopProofStatus) {
	t.Helper()
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateClosed || snap.StopProof != string(want) {
		t.Fatalf("closed session = %+v, %v", snap, err)
	}
}
