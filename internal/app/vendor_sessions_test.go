package app

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testActorSessionID = "actor-session"
	testVendorName     = "claude"
	testAccountID      = "account"
	testActorRole      = "assistant"
	testFugueTurnID    = "fugue-turn"
	testVendorTurnID   = "vendor-123e4567-e89b-12d3-a456-426614174000"
	testNoticeLevel    = "info"
	testLateSteerCode  = "late_steer"
)

var errInjectedAppendFailure = errors.New("injected append failure")

type actorTestAdapter struct {
	session *actorTestSession
	started chan *vendor.StartSpec
}

func mustNewVendorSessions(t *testing.T, ctx context.Context, deps *VendorSessionDeps) *VendorSessions {
	t.Helper()
	manager, err := NewVendorSessions(ctx, deps)
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func mustStartActorSession(t *testing.T, manager *VendorSessions) {
	t.Helper()
	session := &vendorstore.Session{ID: testActorSessionID, Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}
	if err := manager.Start(t.Context(), session, &vendor.StartSpec{SessionID: testActorSessionID}); err != nil {
		t.Fatal(err)
	}
}

func mustSendActorInput(t *testing.T, manager *VendorSessions, text string) {
	t.Helper()
	if err := manager.Send(t.Context(), testActorSessionID, vendor.Input{Text: text}); err != nil {
		t.Fatal(err)
	}
}

func mustCloseActorSessions(t *testing.T, manager *VendorSessions) {
	t.Helper()
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func (a actorTestAdapter) Probe(context.Context, vendor.AccountHome) (vendor.Probe, error) {
	return vendor.Probe{}, nil
}

func (a actorTestAdapter) Start(_ context.Context, spec *vendor.StartSpec) (vendor.Session, error) {
	if a.started != nil {
		copySpec := *spec
		a.started <- &copySpec
	}
	return a.session, nil
}

type actorTestSession struct {
	observations     chan claude.Observation
	turnID           string
	sent             int
	steered          atomic.Int32
	closed           bool
	interrupted      atomic.Int32
	closeCalls       atomic.Int32
	closeCanceled    atomic.Bool
	closeStarted     chan struct{}
	closeStartedOnce sync.Once
	closeGate        <-chan struct{}
	interruptStarted chan struct{}
	interruptGate    <-chan struct{}
}

func newActorTestSession() *actorTestSession {
	return &actorTestSession{observations: make(chan claude.Observation, 256), turnID: testFugueTurnID}
}

func (s *actorTestSession) Observations() <-chan claude.Observation { return s.observations }
func (s *actorTestSession) Identity() process.Identity {
	return process.Identity{PID: 4242, StartToken: "fake-start-token"}
}
func (s *actorTestSession) PID() int { return 4242 }
func (s *actorTestSession) Send(context.Context, vendor.Input) (vendor.TurnRef, error) {
	s.sent++
	return vendor.TurnRef{ID: s.turnID}, nil
}

func (s *actorTestSession) Interrupt(context.Context, vendor.TurnRef) error {
	s.interrupted.Add(1)
	if s.interruptStarted != nil {
		close(s.interruptStarted)
	}
	if s.interruptGate != nil {
		<-s.interruptGate
	}
	return nil
}

func (s *actorTestSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error {
	s.steered.Add(1)
	return nil
}

func (*actorTestSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return vendor.ErrUnsupported
}

func (s *actorTestSession) Close(ctx context.Context, _ vendor.CloseReason) (process.StopProof, error) {
	s.closeCalls.Add(1)
	if ctx.Err() != nil {
		s.closeCanceled.Store(true)
		return process.StopProof{Status: process.StopProofUnknown}, fmt.Errorf("fake close canceled: %w", ctx.Err())
	}
	if s.closeStarted != nil {
		s.closeStartedOnce.Do(func() { close(s.closeStarted) })
	}
	if s.closeGate != nil {
		<-s.closeGate
	}
	s.closed = true
	return process.StopProof{Status: process.StopProofProved}, nil
}

func TestActorPersistsVendorInitiatedTurnFromLateSteer(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	item := transcript.Item{
		ID: "notice-vendor", SessionID: testActorSessionID, TurnID: testVendorTurnID, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: testLateSteerCode},
		Content:  transcript.NoticeContent{Text: "late steer"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{item}, TurnEnd: &claude.TurnEnd{TurnID: testVendorTurnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		events, eventErr := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
		if eventErr != nil {
			return false
		}
		for i := range events {
			if events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.TurnID == testVendorTurnID && events[i].Metadata.Outcome == vendorstore.OutcomeCompleted {
				return true
			}
		}
		return false
	})
	_, turns, _, err := f.store.Overview(t.Context(), testActorSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("open turns = %v, want completed vendor turn", turns)
	}
	itemMeta, err := f.store.StoredItemMeta(t.Context(), testActorSessionID, "notice-vendor")
	if err != nil || itemMeta.TurnID != testVendorTurnID || itemMeta.Metadata.Origin != vendorstore.TurnOriginVendor {
		t.Fatalf("stored vendor item = %+v, %v; want vendor-origin grouping", itemMeta, err)
	}
	mustCloseActorSessions(t, manager)
}

func TestVendorSessionSteerInterruptAndList(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "start turn")
	if err := manager.Steer(t.Context(), testActorSessionID, vendor.Input{Text: "steer turn"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Interrupt(t.Context(), testActorSessionID); err != nil {
		t.Fatal(err)
	}
	if got := session.steered.Load(); got != 1 {
		t.Fatalf("steer calls = %d, want 1", got)
	}
	sessions, err := manager.List(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for i := range sessions {
		found = found || sessions[i].ID == testActorSessionID
	}
	if !found {
		t.Fatalf("vendor sessions = %+v; want %q", sessions, testActorSessionID)
	}
	session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
		return err == nil && snap.State == vendorstore.StateReady
	})
	mustCloseActorSessions(t, manager)
}

func TestActorAbsorbedSendEndsFugueTurn(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "please handle this")
	vendorRecord := transcript.Item{
		ID: "notice-vendor", SessionID: testActorSessionID, TurnID: testVendorTurnID, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: testLateSteerCode},
		Content:  transcript.NoticeContent{Text: "late steer"},
	}
	absorbed := transcript.Item{
		ID: "notice-absorbed", SessionID: testActorSessionID, TurnID: testFugueTurnID, Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: "absorbed_into_vendor_turn"},
		Content:  transcript.NoticeContent{Text: "absorbed"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{vendorRecord, absorbed}, TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateReady
	})
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("session after absorbed send = %+v, %v", snap, err)
	}
	_, turns, _, err := f.store.Overview(t.Context(), testActorSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0] != testVendorTurnID {
		t.Fatalf("open turns = %v, want only vendor-origin turn", turns)
	}
	assertAbsorbedTurnItems(t, f)
	mustCloseActorSessions(t, manager)
}

func assertAbsorbedTurnItems(t *testing.T, f *transcriptFixture) {
	t.Helper()
	vendorItem, vendorErr := f.store.StoredItemMeta(t.Context(), testActorSessionID, "notice-vendor")
	absorbedItem, absorbedErr := f.store.StoredItemMeta(t.Context(), testActorSessionID, "notice-absorbed")
	if vendorErr != nil || absorbedErr != nil || vendorItem.TurnID != testVendorTurnID || vendorItem.Metadata.Origin != vendorstore.TurnOriginVendor || absorbedItem.TurnID != testFugueTurnID || absorbedItem.Metadata.Code != "absorbed_into_vendor_turn" {
		t.Fatalf("vendor item/absorbed notice = %+v/%+v, errors %v/%v", vendorItem, absorbedItem, vendorErr, absorbedErr)
	}
}

func TestVRE11VaultLockInterruptsAndWaitsWithoutPlaintext(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.interruptStarted = make(chan struct{})
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "protected user input")
	f.vault.Lock()
	<-session.interruptStarted
	session.observations <- claude.Observation{TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateWaiting && snap.WaitingReason == string(vendor.WaitingVaultLocked)
	})
	if session.interrupted.Load() != 1 {
		t.Fatalf("Interrupt calls = %d, want 1", session.interrupted.Load())
	}
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	assertInterruptedTurnEvent(t, events)
	protected := transcript.Item{
		ID: "locked-item", SessionID: testActorSessionID, TurnID: testFugueTurnID, Kind: transcript.AssistantMessage,
		Metadata: transcript.AssistantMessageMetadata{ModelID: "safe-model"},
		Content:  transcript.AssistantMessageContent{Text: "vendor secret output"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{protected}}
	awaitActor(t, func() bool {
		_, itemErr := f.store.StoredItemMeta(t.Context(), testActorSessionID, "locked-item")
		return itemErr == nil
	})
	assertStoredItemHasNoPayload(t, f, "locked-item")
	if err := f.vault.Unlock(t.Context(), redact.NewSecret(testMasterPW)); err != nil {
		t.Fatal(err)
	}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateReady
	})
	mustCloseActorSessions(t, manager)
}

func TestVaultLockInterruptKeepsDrainingObservations(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	interruptGate := make(chan struct{})
	session := newActorTestSession()
	session.interruptStarted = make(chan struct{})
	session.interruptGate = interruptGate
	var releaseOnce sync.Once
	releaseInterrupt := func() { releaseOnce.Do(func() { close(interruptGate) }) }
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	defer func() {
		releaseInterrupt()
		mustCloseActorSessions(t, manager)
	}()
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "protected user input")
	f.vault.Lock()
	<-session.interruptStarted

	producer, sent := sendVaultLockObservationBurst(t.Context(), session)
	awaitActor(t, func() bool { return sent.Load() == 301 })
	if err := producer.Wait(); err != nil {
		t.Fatal(err)
	}
	releaseInterrupt()
	awaitActor(t, func() bool {
		snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
		return err == nil && snap.State == vendorstore.StateWaiting && snap.WaitingReason == string(vendor.WaitingVaultLocked)
	})
	if session.interrupted.Load() != 1 {
		t.Fatalf("Interrupt calls = %d, want 1", session.interrupted.Load())
	}
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 400)
	if err != nil {
		t.Fatal(err)
	}
	assertInterruptedTurnEvent(t, events)
	assertStoredItemHasNoPayload(t, f, "locked-burst-final")
}

func sendVaultLockObservationBurst(ctx context.Context, session *actorTestSession) (*supervise.Task, *atomic.Int32) {
	var sent atomic.Int32
	producer := supervise.Go(ctx, "test-vendor-observation-burst", func(ctx context.Context) error {
		for range 300 {
			select {
			case session.observations <- claude.Observation{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			sent.Add(1)
		}
		item := transcript.Item{
			ID: "locked-burst-final", SessionID: testActorSessionID, TurnID: testFugueTurnID, Kind: transcript.AssistantMessage,
			Metadata: transcript.AssistantMessageMetadata{ModelID: "safe-model"},
			Content:  transcript.AssistantMessageContent{Text: "protected vendor output"},
		}
		select {
		case session.observations <- claude.Observation{Records: []transcript.Item{item}, TurnEnd: &claude.TurnEnd{TurnID: testFugueTurnID, Outcome: claude.OutcomeCompleted}}:
		case <-ctx.Done():
			return ctx.Err()
		}
		sent.Add(1)
		return nil
	})
	return producer, &sent
}

func assertInterruptedTurnEvent(t *testing.T, events []vendorstore.RecordedEvent) {
	t.Helper()
	for i := range events {
		if events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.Outcome == vendorstore.OutcomeInterrupted {
			return
		}
	}
	t.Fatal("vault lock did not durably end the active turn as interrupted")
}

func assertStoredItemHasNoPayload(t *testing.T, f *transcriptFixture, itemID string) {
	t.Helper()
	stored, err := f.store.StoredItemMeta(t.Context(), testActorSessionID, itemID)
	if err != nil || stored.PayloadID != "" {
		t.Fatalf("locked item payload = %q, %v; want no protected payload", stored.PayloadID, err)
	}
}

func TestVRE06HostCrashDuringTurnMarksDetachedAndUnknown(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	adapter := actorTestAdapter{session: session}
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: adapter, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "run once")
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil {
		t.Fatal(err)
	}
	appendRecoveredExit(t, f, snap.LastSequence)
	identities, err := f.store.Recover(t.Context())
	if err != nil || len(identities) != 1 || identities[0].PID != 4242 || identities[0].StartToken != "fake-start-token" {
		t.Fatalf("recovery identities = %+v, %v", identities, err)
	}
	snap, err = f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateDetached {
		t.Fatalf("recovered session = %+v, %v; want detached", snap, err)
	}
	assertRecoveredTurnUnknown(t, f)
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	retainedProof := hasProvedProcessExit(events)
	if !retainedProof || session.sent != 1 {
		t.Fatalf("stop proof retained = %v, vendor sends = %d; want proof and no replay", retainedProof, session.sent)
	}
}

func appendRecoveredExit(t *testing.T, f *transcriptFixture, sequence int64) {
	t.Helper()
	_, err := f.store.Append(t.Context(), testActorSessionID, sequence, &vendorstore.Event{Type: vendorstore.VendorProcessExited, StopProof: string(process.StopProofProved)})
	if err != nil {
		t.Fatal(err)
	}
}

func assertRecoveredTurnUnknown(t *testing.T, f *transcriptFixture) {
	t.Helper()
	_, turns, _, err := f.store.Overview(t.Context(), testActorSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("recovered open turns = %v; want none", turns)
	}
	// Store.Recover updates the turn projection atomically without appending
	// public events; vendorstore's recovery test checks the projected unknown outcome.
}

func hasProvedProcessExit(events []vendorstore.RecordedEvent) bool {
	for i := range events {
		if events[i].Type == vendorstore.VendorProcessExited && events[i].Metadata.StopProof == string(process.StopProofProved) {
			return true
		}
	}
	return false
}

func TestActorProcessExitPersistsStopProofBeforeClose(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "running at process exit")
	close(session.observations)
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateClosed
	})
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	turnUnknownAt, processExitAt, closeAt := int64(0), int64(0), int64(0)
	for i := range events {
		switch events[i].Type {
		case vendorstore.VendorTurnEnded:
			if events[i].Metadata.Outcome == vendorstore.OutcomeUnknown {
				turnUnknownAt = events[i].Sequence
			}
		case vendorstore.VendorProcessExited:
			processExitAt = events[i].Sequence
			if events[i].Metadata.StopProof != string(process.StopProofProved) {
				t.Fatalf("process exit StopProof = %q, want proved", events[i].Metadata.StopProof)
			}
		case vendorstore.VendorSessionClosed:
			closeAt = events[i].Sequence
		}
	}
	if turnUnknownAt <= 0 || turnUnknownAt >= processExitAt || processExitAt >= closeAt {
		t.Fatalf("event order turn=%d process=%d close=%d", turnUnknownAt, processExitAt, closeAt)
	}
}

func TestVendorSessionResumeUsesStoredHandleAndStartsNewProcess(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	firstSession := newActorTestSession()
	firstSession.closeStarted = make(chan struct{})
	actorCtx, cancelActor := context.WithCancel(t.Context())
	firstManager := mustNewVendorSessions(t, actorCtx, &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: firstSession}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, firstManager)
	resumeHandle := "123e4567-e89b-12d3-a456-426614174000"
	firstSession.observations <- claude.Observation{Init: &claude.Init{SessionID: resumeHandle}}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.ResumeHandle == resumeHandle
	})
	cancelActor()
	<-firstSession.closeStarted
	if err := firstManager.actors[testActorSessionID].task.Wait(); err != nil {
		t.Fatalf("stopped actor = %v", err)
	}
	if _, err := f.store.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	secondSession := newActorTestSession()
	started := make(chan *vendor.StartSpec, 1)
	secondManager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: secondSession, started: started}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("resume-item-"),
	})
	if err := secondManager.Resume(t.Context(), testActorSessionID, &vendor.StartSpec{SessionID: testActorSessionID}); err != nil {
		t.Fatal(err)
	}
	if spec := <-started; spec.ResumeSessionID != resumeHandle {
		t.Fatalf("resume handle passed to adapter = %q, want stored handle", spec.ResumeSessionID)
	}
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("resumed session = %+v, %v; want ready", snap, err)
	}
	mustCloseActorSessions(t, secondManager)
}

func TestActorAppendFailureDoesNotAdvanceSequence(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	store := &failAppendVendorStore{Store: f.store, eventType: vendorstore.VendorTurnStarted}
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	err := manager.Send(t.Context(), testActorSessionID, vendor.Input{Text: "fail append"})
	if !errors.Is(err, ErrVendorActorFailed) {
		t.Fatalf("Send error = %v, want typed actor failure", err)
	}
	snap, err := f.store.Snapshot(t.Context(), testActorSessionID)
	if err != nil || snap.LastSequence != 2 || snap.State != vendorstore.StateReady {
		t.Fatalf("session after failed append = %+v, %v; want sequence 2 and ready", snap, err)
	}
}

func TestVendorSessionCapacityRejectsThirdActiveProcess(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	err := manager.Start(t.Context(), &vendorstore.Session{ID: "over-capacity", Vendor: testVendorName, AccountID: testAccountID, Role: testActorRole}, &vendor.StartSpec{SessionID: "over-capacity"})
	if !errors.Is(err, ErrVendorSessionCapacity) {
		t.Fatalf("third active session error = %v, want capacity error", err)
	}
	mustCloseActorSessions(t, manager)
}

func TestActorObservationBackpressurePreservesAll(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	const observationCount = 300
	for i := range observationCount {
		item := transcript.Item{
			ID: fmt.Sprintf("notice-%03d", i), SessionID: testActorSessionID, TurnID: testVendorTurnID, Kind: transcript.Notice,
			Metadata: transcript.NoticeMetadata{Level: testNoticeLevel, Code: testLateSteerCode},
			Content:  transcript.NoticeContent{Text: "notice"},
		}
		session.observations <- claude.Observation{Records: []transcript.Item{item}}
	}
	awaitActor(t, func() bool {
		_, itemErr := f.store.StoredItemMeta(t.Context(), testActorSessionID, "notice-299")
		return itemErr == nil
	})
	items, err := f.store.Transcript(t.Context(), testActorSessionID, 0, 1000)
	if err != nil || len(items) != observationCount {
		t.Fatalf("durable observations = %d, %v; want %d", len(items), err, observationCount)
	}
	mustCloseActorSessions(t, manager)
}

func TestActorUsesCommittedTranscriptPath(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	mustSendActorInput(t, manager, "persist then publish")
	item, err := f.store.StoredItemMeta(t.Context(), testActorSessionID, "actor-item--1")
	if err != nil || item.ID != "actor-item--1" {
		t.Fatalf("actor transcript item = %+v, %v", item, err)
	}
	mustCloseActorSessions(t, manager)
}

func TestCloseRPCDeadlineDoesNotSkipStopOrder(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.closeStarted = make(chan struct{})
	closeGate := make(chan struct{})
	session.closeGate = closeGate
	manager := mustNewVendorSessions(t, t.Context(), &VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	mustStartActorSession(t, manager)
	callCtx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	call := supervise.Go(callCtx, "test.close-vendor-session", func(ctx context.Context) error {
		result <- manager.Close(ctx, testActorSessionID, vendor.CloseUserRequest)
		return nil
	})
	<-session.closeStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close wait = %v, want context canceled", err)
	}
	if err := call.Wait(); err != nil {
		t.Fatalf("close call task = %v", err)
	}
	close(closeGate)
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), testActorSessionID)
		return snapErr == nil && snap.State == vendorstore.StateClosed
	})
	if err := manager.Close(t.Context(), testActorSessionID, vendor.CloseUserRequest); err != nil {
		t.Fatalf("retry after close = %v", err)
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("process stop calls = %d, want one completed stop order", session.closeCalls.Load())
	}
	if session.closeCanceled.Load() {
		t.Fatal("the RPC cancellation reached the process stop operation")
	}
	events, err := f.store.EventsSince(t.Context(), testActorSessionID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var proof string
	for i := range events {
		if events[i].Type == vendorstore.VendorSessionClosed {
			proof = events[i].Metadata.StopProof
		}
	}
	if proof != string(process.StopProofProved) {
		t.Fatalf("stored StopProof = %q, want proved", proof)
	}
}

type failAppendVendorStore struct {
	*vendorstore.Store
	mu        sync.Mutex
	eventType string
	failed    bool
}

func (s *failAppendVendorStore) Append(ctx context.Context, session string, sequence int64, event *vendorstore.Event) (int64, error) {
	s.mu.Lock()
	if !s.failed && event.Type == s.eventType {
		s.failed = true
		s.mu.Unlock()
		return 0, errInjectedAppendFailure
	}
	s.mu.Unlock()
	next, err := s.Store.Append(ctx, session, sequence, event)
	if err != nil {
		return 0, fmt.Errorf("delegate vendor event append: %w", err)
	}
	return next, nil
}

func awaitActor(t *testing.T, condition func() bool) {
	t.Helper()
	for range 1000000 {
		if condition() {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("session actor did not reach the expected durable state")
}
