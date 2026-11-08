package vendorstore

import (
	"context"
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func TestDatabaseFailuresAreTypedAndDoNotReturnPartialReads(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	requireNoError(t, db.Close())
	operations := []func(context.Context) error{
		func(ctx context.Context) error {
			return s.CreateSession(ctx, &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting})
		},
		func(ctx context.Context) error {
			_, err := s.Append(ctx, "s", 0, &Event{Type: UsageObserved})
			return err
		},
		func(ctx context.Context) error { _, err := s.Snapshot(ctx, "s"); return err },
		func(ctx context.Context) error { _, err := s.EventsSince(ctx, "s", 0, 10); return err },
		func(ctx context.Context) error { _, err := s.Transcript(ctx, "s", 0, 10); return err },
		func(ctx context.Context) error { _, err := s.Recover(ctx); return err },
		func(ctx context.Context) error { return s.PurgeSession(ctx, "s") },
		func(ctx context.Context) error { return s.UpdatePolicy(ctx, "s", 0, ApprovalAuto) },
	}
	for _, operation := range operations {
		if err := operation(t.Context()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("untyped storage failure: %v", err)
		}
	}
}

func TestInvalidArgumentsAndMissingReads(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if err := s.CreateSession(t.Context(), nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("nil session accepted")
	}
	if _, err := s.Append(t.Context(), "s", 0, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("nil event accepted")
	}
	if _, err := s.Append(t.Context(), "s", 0, &Event{Type: "unsupported"}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("unknown event accepted")
	}
	if _, err := s.Snapshot(t.Context(), "missing"); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatal("missing session did not fail typed")
	}
	if _, err := s.Transcript(t.Context(), "s", 0, 0); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("invalid transcript limit")
	}
	if _, err := s.EventsSince(t.Context(), "s", 0, 0); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("invalid event limit")
	}
	if err := s.UpdatePolicy(t.Context(), "s", 0, "invalid mode"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("invalid policy accepted")
	}
}

func TestTurnOutcomeIsSetOnceAndSessionFieldsSurvive(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	requireNoError(t, s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, TeamID: "team", TaskID: "task", ProcessPID: 42, ProcessStartToken: testStartToken, ResumeHandle: testResumeHandle}))
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: VendorTurnEnded, TurnID: "t", Outcome: OutcomeCompleted})
	requireNoError(t, err)
	if _, err = s.Append(t.Context(), "s", 3, &Event{Type: VendorTurnEnded, TurnID: "t", Outcome: OutcomeInterrupted}); !errors.Is(err, ErrTurnNotOpen) {
		t.Fatal("turn outcome overwritten")
	}
	snapshot, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	if snapshot.TeamID != "team" || snapshot.TaskID != "task" || snapshot.ResumeHandle != testResumeHandle || snapshot.ProcessPID != 42 {
		t.Fatal("session metadata lost")
	}
}

func TestMetadataAllowlistAndUnavailablePayloads(t *testing.T) {
	t.Parallel()
	s, v, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	invalid := &Event{Type: TranscriptItemRecorded, Item: &Item{ID: "i", TurnID: "t", Kind: "assistant_message", Metadata: ItemMetadata{Tool: "not-an-assistant-field"}}}
	if _, err = s.Append(t.Context(), "s", 2, invalid); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("metadata allowlist bypassed")
	}
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: TranscriptItemRecorded, Item: &Item{ID: "i", TurnID: "t", Kind: "assistant_message", Metadata: ItemMetadata{Model: "model"}, Content: []byte("protected")}})
	requireNoError(t, err)
	if _, _, err = s.ReadItem(t.Context(), "s", "missing"); errcode.CodeOf(err) != errcode.NotFound {
		t.Fatal("missing item accepted")
	}
	if _, err = s.OpenPayload(t.Context(), "s", "missing"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("missing payload did not fail typed")
	}
	v.Lock()
	if _, _, err = s.ReadItem(t.Context(), "s", "i"); err == nil {
		t.Fatal("locked read succeeded")
	}
	if _, err = s.OpenPayload(t.Context(), "s", "missing"); err == nil {
		t.Fatal("locked payload read succeeded")
	}
}

func TestConstructorsAndSessionValidation(t *testing.T) {
	t.Parallel()
	s, db := testStore(t)
	if _, err := New(t.Context(), nil, nil, s.clock, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("nil database accepted")
	}
	if _, err := New(t.Context(), db, nil, nil, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("nil clock accepted")
	}
	if err := s.CreateSession(t.Context(), &Session{}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("empty session accepted")
	}
	requireNoError(t, s.Close())
	requireNoError(t, s.Close())
}

func TestApprovalModesAreValidatedOnCreateAndUpdate(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	if err := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, ApprovalMode: "allow_everything"}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("unknown initial mode accepted")
	}
	createTestSession(t, s)
	if err := s.UpdatePolicy(t.Context(), "s", 0, "autoo"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("unknown updated mode accepted")
	}
	for revision, mode := range []string{ApprovalPlan, ApprovalAsk, ApprovalAuto, ApprovalFull} {
		requireNoError(t, s.UpdatePolicy(t.Context(), "s", int64(revision), mode))
	}
}

func TestClosedSessionRejectsAppendAndPolicyWithErrSessionClosed(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorSessionClosed, StopProof: StopProved})
	requireNoError(t, err)
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved})
	if !errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("append to closed session: %v", err)
	}
	err = s.UpdatePolicy(t.Context(), "s", 0, ApprovalAsk)
	if !errors.Is(err, ErrSessionClosed) || errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("policy on closed session: %v", err)
	}
	if errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("closed session code: %v", errcode.CodeOf(err))
	}
}

func TestMissingSessionIsNotFoundNotConflict(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	_, err := s.Append(t.Context(), "missing", 0, &Event{Type: UsageObserved})
	if !errors.Is(err, ErrSessionNotFound) || errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("append to missing session: %v", err)
	}
	err = s.UpdatePolicy(t.Context(), "missing", 0, ApprovalAsk)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("policy on missing session: %v", err)
	}
}

func TestTurnEndWithoutOpenTurnIsNotFound(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnEnded, TurnID: "never-started", Outcome: OutcomeCompleted})
	if !errors.Is(err, ErrTurnNotOpen) || errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("end of unknown turn: %v", err)
	}
	snap, snapErr := s.Snapshot(t.Context(), "s")
	requireNoError(t, snapErr)
	if snap.LastSequence != 1 {
		t.Fatalf("failed turn end advanced the journal to %d", snap.LastSequence)
	}
}

func TestUpdateOfMissingItemIsNotFoundAndStaleRevisionIsConflict(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	missing := &Event{Type: TranscriptItemUpdated, Item: &Item{ID: "missing", TurnID: "t", Kind: KindAssistantMessage, Revision: 1}}
	if _, err = s.Append(t.Context(), "s", 2, missing); !errors.Is(err, ErrItemNotFound) || errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("update of missing item: %v", err)
	}
	recorded := &Event{Type: TranscriptItemRecorded, Item: &Item{ID: "i", TurnID: "t", Kind: KindAssistantMessage}}
	_, err = s.Append(t.Context(), "s", 2, recorded)
	requireNoError(t, err)
	stale := &Event{Type: TranscriptItemUpdated, Item: &Item{ID: "i", TurnID: "t", Kind: KindAssistantMessage, Revision: 7}}
	if _, err = s.Append(t.Context(), "s", 3, stale); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("stale item revision: %v", err)
	}
}

func TestProcessIdentityIsValidated(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	scopeOnly := &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, ProcessScopeUnit: testScopeUnit}
	if err := s.CreateSession(t.Context(), scopeOnly); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("scope unit without a PID accepted")
	}
	createTestSession(t, s)
	if _, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, ProcessScopeUnit: testScopeUnit}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("event scope unit without a PID accepted")
	}
}
