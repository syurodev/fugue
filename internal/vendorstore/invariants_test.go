package vendorstore

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func createTestSession(t *testing.T, s *Store) {
	t.Helper()
	requireNoError(t, s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}))
}

func TestConcurrentAppendConflictsAndConsistentSnapshot(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	gate := make(chan struct{})
	results := make(chan error, 2)
	tasks := make([]*supervise.Task, 0, 2)
	for range 2 {
		tasks = append(tasks, supervise.Go(t.Context(), "append", func(ctx context.Context) error {
			select {
			case <-gate:
			case <-ctx.Done():
				return ctx.Err()
			}
			_, err := s.Append(ctx, "s", 1, &Event{Type: UsageObserved})
			results <- err
			return nil
		}))
	}
	close(gate)
	for _, task := range tasks {
		requireNoError(t, task.Wait())
	}
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrSequenceConflict):
			conflicts++
		default:
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("success=%d conflict=%d", successes, conflicts)
	}
	snapshot, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	events, err := s.EventsSince(t.Context(), "s", 0, 100)
	requireNoError(t, err)
	if int64(len(events)) != snapshot.LastSequence {
		t.Fatal("snapshot diverged")
	}
}

func TestItemRevisionAndBackwardPagination(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	for i, id := range []string{"a", "b", "c"} {
		_, err = s.Append(t.Context(), "s", int64(i+2), &Event{Type: TranscriptItemRecorded, Item: &Item{ID: id, TurnID: "t", Kind: testAssistantKind, Content: []byte(id)}})
		requireNoError(t, err)
	}
	_, err = s.Append(t.Context(), "s", 5, &Event{Type: TranscriptItemUpdated, Item: &Item{ID: "c", TurnID: "t", Kind: testAssistantKind, Revision: 1, Content: []byte("updated")}})
	requireNoError(t, err)
	item, content, err := s.ReadItem(t.Context(), "s", "c")
	requireNoError(t, err)
	if item.Revision != 2 || item.ID != "c" || string(content) != "updated" {
		t.Fatal("item identity or revision lost")
	}
	page, err := s.Transcript(t.Context(), "s", 6, 2)
	requireNoError(t, err)
	ids := make([]string, 0, len(page))
	for _, entry := range page {
		ids = append(ids, entry.ID)
	}
	if diff := cmp.Diff([]string{"b", "c"}, ids); diff != "" {
		t.Fatal(diff)
	}
}

func TestInvalidEnumsAndPolicyConflicts(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	for _, event := range []*Event{{Type: UsageObserved, State: testInvalid}, {Type: VendorTurnEnded, TurnID: "t", Outcome: testInvalid}, {Type: TranscriptItemRecorded, Item: &Item{Kind: testInvalid}}} {
		if _, err := s.Append(t.Context(), "s", 1, event); err == nil {
			t.Fatal("invalid enum accepted")
		}
	}
	requireNoError(t, s.UpdatePolicy(t.Context(), "s", 0, ApprovalAsk))
	if err := s.UpdatePolicy(t.Context(), "s", 0, ApprovalAuto); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("revision conflict: %v", err)
	}
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorSessionClosed, StopProof: OutcomeUnknown})
	requireNoError(t, err)
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved}); !errors.Is(err, ErrSequenceConflict) {
		t.Fatal("closed session accepted append")
	}
}

func TestMidCommitFailureRollsBackPayloadEventAndProjection(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	s.SetCommitFault(ErrUnavailable)
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: TranscriptItemRecorded, Item: &Item{ID: "x", TurnID: "t", Kind: testAssistantKind, Content: []byte("atomic marker")}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("fault: %v", err)
	}
	s.SetCommitFault(nil)
	snap, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	items, err := s.Transcript(t.Context(), "s", 0, 100)
	requireNoError(t, err)
	var payloadCount int
	requireNoError(t, s.db.QueryRowContext(t.Context(), "SELECT (SELECT COUNT(*) FROM vendor_payloads)+(SELECT COUNT(*) FROM vendor_session_keys)").Scan(&payloadCount))
	events, eventErr := s.EventsSince(t.Context(), "s", 0, 100)
	requireNoError(t, eventErr)
	if len(events) != 2 {
		t.Fatal("partial event remained")
	}
	if snap.LastSequence != 2 || len(items) != 0 || payloadCount != 0 {
		t.Fatal("partial commit remained")
	}
}

const (
	testAssistantKind = "assistant_message"
	testInvalid       = "invalid"
)

func TestProcessIdentitySurvivesResumeHandleUpdate(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	requireNoError(t, s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting, ProcessPID: 42, ProcessStartTime: testProcessStart}))
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, ResumeHandle: testResumeHandle})
	requireNoError(t, err)
	identities, err := s.Recover(t.Context())
	requireNoError(t, err)
	if len(identities) != 1 || identities[0].PID != 42 || identities[0].StartTime != testProcessStart {
		t.Fatal("process identity erased")
	}
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, ProcessPID: 55}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatal("PID without start time accepted")
	}
}

func TestItemVersionsRetainProtectedPayloadReferences(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: TranscriptItemRecorded, Item: &Item{ID: "i", TurnID: "t", Kind: testAssistantKind, Content: []byte("first"), Raw: []byte("raw-first")}})
	requireNoError(t, err)
	_, err = s.Append(t.Context(), "s", 3, &Event{Type: TranscriptItemUpdated, Item: &Item{ID: "i", TurnID: "t", Kind: testAssistantKind, Revision: 1, Content: []byte("second"), Raw: []byte("raw-second")}})
	requireNoError(t, err)
	events, err := s.EventsSince(t.Context(), "s", 2, 10)
	requireNoError(t, err)
	if len(events) != 2 {
		t.Fatal("versions missing")
	}
	first, err := s.OpenPayload(t.Context(), "s", events[0].Metadata.ContentID)
	requireNoError(t, err)
	raw, err := s.OpenPayload(t.Context(), "s", events[0].Metadata.RawID)
	requireNoError(t, err)
	if string(first) != "first" || string(raw) != "raw-first" {
		t.Fatal("prior protected version lost")
	}
}

func TestSnapshotAndEventsStayConsistentDuringAppends(t *testing.T) {
	t.Parallel()
	s, _ := testStore(t)
	createTestSession(t, s)
	gate := make(chan struct{})
	writer := supervise.Go(t.Context(), "journal-writer", func(ctx context.Context) error {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
		for sequence := int64(1); sequence <= 12; sequence++ {
			if _, err := s.Append(ctx, "s", sequence, &Event{Type: UsageObserved}); err != nil {
				return err
			}
		}
		return nil
	})
	close(gate)
	for range 12 {
		assertSnapshotPrefix(t, s)
	}
	requireNoError(t, writer.Wait())
	assertSnapshotPrefix(t, s)
}

func assertSnapshotPrefix(t *testing.T, s *Store) {
	t.Helper()
	snapshot, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	events, err := s.EventsSince(t.Context(), "s", 0, 100)
	requireNoError(t, err)
	if int64(len(events)) < snapshot.LastSequence {
		t.Fatal("snapshot referenced missing committed event")
	}
	for index := range events {
		if events[index].Sequence != int64(index+1) {
			t.Fatal("durable sequence gap")
		}
	}
}
