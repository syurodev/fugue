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
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type actorTestAdapter struct {
	session *actorTestSession
	started chan *vendor.StartSpec
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
	observations  chan claude.Observation
	turnID        string
	sent          int
	closed        bool
	interrupted   atomic.Int32
	closeCalls    atomic.Int32
	closeCanceled atomic.Bool
	closeStarted  chan struct{}
	closeGate     <-chan struct{}
}

func newActorTestSession() *actorTestSession {
	return &actorTestSession{observations: make(chan claude.Observation, 256), turnID: "fugue-turn"}
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
	return nil
}
func (*actorTestSession) Steer(context.Context, vendor.TurnRef, vendor.Input) error { return nil }
func (*actorTestSession) Resolve(context.Context, vendor.ApprovalRef, vendor.Decision) error {
	return vendor.ErrUnsupported
}

func (s *actorTestSession) Close(ctx context.Context, _ vendor.CloseReason) (process.StopProof, error) {
	s.closeCalls.Add(1)
	if ctx.Err() != nil {
		s.closeCanceled.Store(true)
		return process.StopProof{Status: process.StopProofUnknown}, ctx.Err()
	}
	if s.closeStarted != nil {
		close(s.closeStarted)
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
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	item := transcript.Item{
		ID: "notice-vendor", SessionID: "actor-session", TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: "info", Code: "late_steer"},
		Content:  transcript.NoticeContent{Text: "late steer"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{item}, TurnEnd: &claude.TurnEnd{TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		events, eventErr := f.store.EventsSince(t.Context(), "actor-session", 0, 100)
		if eventErr != nil {
			return false
		}
		for i := range events {
			if events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.TurnID == "vendor-123e4567-e89b-12d3-a456-426614174000" && events[i].Metadata.Outcome == vendorstore.OutcomeCompleted {
				return true
			}
		}
		return false
	})
	_, turns, _, err := f.store.Overview(t.Context(), "actor-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 0 {
		t.Fatalf("open turns = %v, want completed vendor turn", turns)
	}
	itemMeta, err := f.store.StoredItemMeta(t.Context(), "actor-session", "notice-vendor")
	if err != nil || itemMeta.TurnID != "vendor-123e4567-e89b-12d3-a456-426614174000" || itemMeta.Metadata.Origin != vendorstore.TurnOriginVendor {
		t.Fatalf("stored vendor item = %+v, %v; want vendor-origin grouping", itemMeta, err)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestActorAbsorbedSendEndsFugueTurn(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Send(t.Context(), "actor-session", vendor.Input{Text: "please handle this"}); err != nil {
		t.Fatal(err)
	}
	vendorRecord := transcript.Item{
		ID: "notice-vendor", SessionID: "actor-session", TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: "info", Code: "late_steer"},
		Content:  transcript.NoticeContent{Text: "late steer"},
	}
	absorbed := transcript.Item{
		ID: "notice-absorbed", SessionID: "actor-session", TurnID: "fugue-turn", Kind: transcript.Notice,
		Metadata: transcript.NoticeMetadata{Level: "info", Code: "absorbed_into_vendor_turn"},
		Content:  transcript.NoticeContent{Text: "absorbed"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{vendorRecord, absorbed}, TurnEnd: &claude.TurnEnd{TurnID: "fugue-turn", Outcome: claude.OutcomeCompleted}}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.State == vendorstore.StateReady
	})
	snap, err := f.store.Snapshot(t.Context(), "actor-session")
	if err != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("session after absorbed send = %+v, %v", snap, err)
	}
	_, turns, _, err := f.store.Overview(t.Context(), "actor-session")
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 1 || turns[0] != "vendor-123e4567-e89b-12d3-a456-426614174000" {
		t.Fatalf("open turns = %v, want only vendor-origin turn", turns)
	}
	vendorItem, vendorErr := f.store.StoredItemMeta(t.Context(), "actor-session", "notice-vendor")
	absorbedItem, absorbedErr := f.store.StoredItemMeta(t.Context(), "actor-session", "notice-absorbed")
	if vendorErr != nil || absorbedErr != nil || vendorItem.TurnID != "vendor-123e4567-e89b-12d3-a456-426614174000" || vendorItem.Metadata.Origin != vendorstore.TurnOriginVendor || absorbedItem.TurnID != "fugue-turn" || absorbedItem.Metadata.Code != "absorbed_into_vendor_turn" {
		t.Fatalf("vendor item/absorbed notice = %+v/%+v, errors %v/%v", vendorItem, absorbedItem, vendorErr, absorbedErr)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestVRE11VaultLockInterruptsAndWaitsWithoutPlaintext(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Send(t.Context(), "actor-session", vendor.Input{Text: "protected user input"}); err != nil {
		t.Fatal(err)
	}
	f.vault.Lock()
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.State == vendorstore.StateWaiting && snap.WaitingReason == string(vendor.WaitingVaultLocked)
	})
	if session.interrupted.Load() != 1 {
		t.Fatalf("Interrupt calls = %d, want 1", session.interrupted.Load())
	}
	events, err := f.store.EventsSince(t.Context(), "actor-session", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var interruptedTurn bool
	for i := range events {
		interruptedTurn = interruptedTurn || (events[i].Type == vendorstore.VendorTurnEnded && events[i].Metadata.Outcome == vendorstore.OutcomeInterrupted)
	}
	if !interruptedTurn {
		t.Fatal("vault lock did not durably end the active turn as interrupted")
	}
	protected := transcript.Item{
		ID: "locked-item", SessionID: "actor-session", TurnID: "fugue-turn", Kind: transcript.AssistantMessage,
		Metadata: transcript.AssistantMessageMetadata{ModelID: "safe-model"},
		Content:  transcript.AssistantMessageContent{Text: "vendor secret output"},
	}
	session.observations <- claude.Observation{Records: []transcript.Item{protected}}
	awaitActor(t, func() bool {
		_, itemErr := f.store.StoredItemMeta(t.Context(), "actor-session", "locked-item")
		return itemErr == nil
	})
	stored, err := f.store.StoredItemMeta(t.Context(), "actor-session", "locked-item")
	if err != nil || stored.PayloadID != "" {
		t.Fatalf("locked item payload = %q, %v; want no protected payload", stored.PayloadID, err)
	}
	if err := f.vault.Unlock(t.Context(), redact.NewSecret(testMasterPW)); err != nil {
		t.Fatal(err)
	}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.State == vendorstore.StateReady
	})
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestVRE06HostCrashDuringTurnMarksDetachedAndUnknown(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	adapter := actorTestAdapter{session: session}
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: adapter, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Send(t.Context(), "actor-session", vendor.Input{Text: "run once"}); err != nil {
		t.Fatal(err)
	}
	snap, err := f.store.Snapshot(t.Context(), "actor-session")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Append(t.Context(), "actor-session", snap.LastSequence, &vendorstore.Event{Type: vendorstore.VendorProcessExited, StopProof: string(process.StopProofProved)}); err != nil {
		t.Fatal(err)
	}
	identities, err := f.store.Recover(t.Context())
	if err != nil || len(identities) != 1 || identities[0].PID != 4242 || identities[0].StartToken != "fake-start-token" {
		t.Fatalf("recovery identities = %+v, %v", identities, err)
	}
	snap, err = f.store.Snapshot(t.Context(), "actor-session")
	if err != nil || snap.State != vendorstore.StateDetached {
		t.Fatalf("recovered session = %+v, %v; want detached", snap, err)
	}
	var outcome string
	if err := f.db.QueryRowContext(t.Context(), `SELECT outcome FROM vendor_turns WHERE session_id='actor-session' AND turn_id='fugue-turn'`).Scan(&outcome); err != nil || outcome != vendorstore.OutcomeUnknown {
		t.Fatalf("recovered turn outcome = %q, %v; want unknown", outcome, err)
	}
	events, err := f.store.EventsSince(t.Context(), "actor-session", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	var retainedProof bool
	for i := range events {
		if events[i].Type == vendorstore.VendorProcessExited && events[i].Metadata.StopProof == string(process.StopProofProved) {
			retainedProof = true
		}
	}
	if !retainedProof || session.sent != 1 {
		t.Fatalf("stop proof retained = %v, vendor sends = %d; want proof and no replay", retainedProof, session.sent)
	}
}

func TestActorProcessExitPersistsStopProofBeforeClose(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Send(t.Context(), "actor-session", vendor.Input{Text: "running at process exit"}); err != nil {
		t.Fatal(err)
	}
	close(session.observations)
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.State == vendorstore.StateClosed
	})
	events, err := f.store.EventsSince(t.Context(), "actor-session", 0, 100)
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
	if !(turnUnknownAt > 0 && turnUnknownAt < processExitAt && processExitAt < closeAt) {
		t.Fatalf("event order turn=%d process=%d close=%d", turnUnknownAt, processExitAt, closeAt)
	}
}

func TestVendorSessionResumeUsesStoredHandleAndStartsNewProcess(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	firstSession := newActorTestSession()
	actorCtx, cancelActor := context.WithCancel(t.Context())
	firstManager, err := NewVendorSessions(actorCtx, VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: firstSession}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := firstManager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	resumeHandle := "123e4567-e89b-12d3-a456-426614174000"
	firstSession.observations <- claude.Observation{Init: &claude.Init{SessionID: resumeHandle}}
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.ResumeHandle == resumeHandle
	})
	cancelActor()
	awaitActor(t, func() bool { return firstSession.closeCalls.Load() == 1 })
	if _, err := f.store.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	secondSession := newActorTestSession()
	started := make(chan *vendor.StartSpec, 1)
	secondManager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: secondSession, started: started}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("resume-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := secondManager.Resume(t.Context(), "actor-session", &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if spec := <-started; spec.ResumeSessionID != resumeHandle {
		t.Fatalf("resume handle passed to adapter = %q, want stored handle", spec.ResumeSessionID)
	}
	snap, err := f.store.Snapshot(t.Context(), "actor-session")
	if err != nil || snap.State != vendorstore.StateReady {
		t.Fatalf("resumed session = %+v, %v; want ready", snap, err)
	}
	if err := secondManager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestActorAppendFailureDoesNotAdvanceSequence(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	store := &failAppendVendorStore{Store: f.store, eventType: vendorstore.VendorTurnStarted}
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	err = manager.Send(t.Context(), "actor-session", vendor.Input{Text: "fail append"})
	if !errors.Is(err, ErrVendorActorFailed) {
		t.Fatalf("Send error = %v, want typed actor failure", err)
	}
	snap, err := f.store.Snapshot(t.Context(), "actor-session")
	if err != nil || snap.LastSequence != 2 || snap.State != vendorstore.StateReady {
		t.Fatalf("session after failed append = %+v, %v; want sequence 2 and ready", snap, err)
	}
}

func TestVendorSessionCapacityRejectsThirdActiveProcess(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	err = manager.Start(t.Context(), &vendorstore.Session{ID: "over-capacity", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "over-capacity"})
	if !errors.Is(err, ErrVendorSessionCapacity) {
		t.Fatalf("third active session error = %v, want capacity error", err)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestActorObservationBackpressurePreservesAll(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	const observationCount = 300
	for i := range observationCount {
		item := transcript.Item{
			ID: fmt.Sprintf("notice-%03d", i), SessionID: "actor-session", TurnID: "vendor-123e4567-e89b-12d3-a456-426614174000", Kind: transcript.Notice,
			Metadata: transcript.NoticeMetadata{Level: "info", Code: "late_steer"},
			Content:  transcript.NoticeContent{Text: "notice"},
		}
		session.observations <- claude.Observation{Records: []transcript.Item{item}}
	}
	awaitActor(t, func() bool {
		_, itemErr := f.store.StoredItemMeta(t.Context(), "actor-session", "notice-299")
		return itemErr == nil
	})
	items, err := f.store.Transcript(t.Context(), "actor-session", 0, 1000)
	if err != nil || len(items) != observationCount {
		t.Fatalf("durable observations = %d, %v; want %d", len(items), err, observationCount)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestActorUsesCommittedTranscriptPath(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.Send(t.Context(), "actor-session", vendor.Input{Text: "persist then publish"}); err != nil {
		t.Fatal(err)
	}
	item, err := f.store.StoredItemMeta(t.Context(), "actor-session", "actor-item--1")
	if err != nil || item.ID != "actor-item--1" {
		t.Fatalf("actor transcript item = %+v, %v", item, err)
	}
	if err := manager.CloseAll(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestCloseRPCDeadlineDoesNotSkipStopOrder(t *testing.T) {
	t.Parallel()
	f := newTranscriptFixture(t, nil)
	session := newActorTestSession()
	session.closeStarted = make(chan struct{})
	closeGate := make(chan struct{})
	session.closeGate = closeGate
	manager, err := NewVendorSessions(t.Context(), VendorSessionDeps{
		Store: f.store, Transcripts: f.service, Adapter: actorTestAdapter{session: session}, Vault: f.vault,
		Clock: f.clock, IDs: ids.NewSequence("actor-item-"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Start(t.Context(), &vendorstore.Session{ID: "actor-session", Vendor: "claude", AccountID: "account", Role: "assistant"}, &vendor.StartSpec{SessionID: "actor-session"}); err != nil {
		t.Fatal(err)
	}
	callCtx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- manager.Close(callCtx, "actor-session", vendor.CloseUserRequest) }()
	<-session.closeStarted
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled close wait = %v, want context canceled", err)
	}
	close(closeGate)
	awaitActor(t, func() bool {
		snap, snapErr := f.store.Snapshot(t.Context(), "actor-session")
		return snapErr == nil && snap.State == vendorstore.StateClosed
	})
	if err := manager.Close(t.Context(), "actor-session", vendor.CloseUserRequest); err != nil {
		t.Fatalf("retry after close = %v", err)
	}
	if session.closeCalls.Load() != 1 {
		t.Fatalf("process stop calls = %d, want one completed stop order", session.closeCalls.Load())
	}
	if session.closeCanceled.Load() {
		t.Fatal("the RPC cancellation reached the process stop operation")
	}
	events, err := f.store.EventsSince(t.Context(), "actor-session", 0, 100)
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
		return 0, errors.New("injected append failure")
	}
	s.mu.Unlock()
	return s.Store.Append(ctx, session, sequence, event)
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
