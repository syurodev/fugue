package vendorstore

import (
	"errors"
	"testing"
)

var errCommitFault = errors.New("injected commit fault")

func TestAppendBatchCommitsContiguousSequencesAtomically(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	batch := func() []*Event {
		return []*Event{
			{Type: UsageObserved, Payload: []byte("a")},
			{Type: UsageObserved, Payload: []byte("b")},
			{Type: UsageObserved, Payload: []byte("c")},
		}
	}

	if _, err := s.AppendBatch(t.Context(), "s", 1, nil); err == nil {
		t.Fatal("an empty batch was accepted")
	}

	// A failed commit leaves no event and no sequence advance behind.
	s.SetCommitFault(errCommitFault)
	if _, err := s.AppendBatch(t.Context(), "s", 1, batch()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("faulted batch: %v", err)
	}
	s.SetCommitFault(nil)
	assertLastSequence(t, s, 1)

	// A stale expected sequence rejects the whole batch.
	if _, err := s.AppendBatch(t.Context(), "s", 5, batch()); !errors.Is(err, ErrSequenceConflict) {
		t.Fatalf("stale batch: %v", err)
	}
	assertLastSequence(t, s, 1)

	last, err := s.AppendBatch(t.Context(), "s", 1, batch())
	if err != nil || last != 4 {
		t.Fatalf("batch = %d, %v, want last sequence 4", last, err)
	}
	assertLastSequence(t, s, 4)
	events, err := s.EventsSince(t.Context(), "s", 1, 10)
	requireNoError(t, err)
	if len(events) != 3 || events[0].Sequence != 2 || events[2].Sequence != 4 {
		t.Fatalf("events = %+v, want sequences 2 through 4", events)
	}
}

func assertLastSequence(t *testing.T, s *Store, want int64) {
	t.Helper()
	snap, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	if snap.LastSequence != want {
		t.Fatalf("last sequence = %d, want %d", snap.LastSequence, want)
	}
}
