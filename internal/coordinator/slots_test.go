package coordinator

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/kernel"
)

func TestReserveNeverExceedsCapacity(t *testing.T) {
	t.Parallel()

	s := newTestSlots(2)

	ref1, ok1 := reserveSlot(s)
	if !ok1 || ref1 == "" {
		t.Fatalf("first reserve failed: ref=%q, ok=%v", ref1, ok1)
	}

	ref2, ok2 := reserveSlot(s)
	if !ok2 || ref2 == "" {
		t.Fatalf("second reserve failed: ref=%q, ok=%v", ref2, ok2)
	}
	if ref1 == ref2 {
		t.Fatalf("reservation refs must be unique, got %q twice", ref1)
	}

	ref3, ok3 := reserveSlot(s)
	if ok3 || ref3 != "" {
		t.Fatalf("third reserve succeeded beyond capacity 2: ref=%q, ok=%v", ref3, ok3)
	}

	if err := s.release(ref1); err != nil {
		t.Fatalf("release ref1 failed: %v", err)
	}

	ref4, ok4 := reserveSlot(s)
	if !ok4 || ref4 == "" {
		t.Fatalf("reserve after release failed: ref=%q, ok=%v", ref4, ok4)
	}
}

func TestHandoffSucceedsOnceAndNeverBlocks(t *testing.T) {
	t.Parallel()

	s := newTestSlots(1)

	ref, ok := reserveSlot(s)
	if !ok {
		t.Fatal("expected reserve to succeed")
	}

	dummyWork := Work{
		Lease: Lease{
			SessionID:        "s1",
			RunID:            "r1",
			InvocationID:     "i1",
			AttemptID:        "a1",
			AdmissionToken:   1,
			OwnerIncarnation: 1,
			ExecutorFence:    1,
		},
		Dispatch: new(kernel.ModelDispatchStartedEvent),
	}

	if err := s.handoff(ref, &dummyWork); err != nil {
		t.Fatalf("expected first handoff to succeed, got %v", err)
	}

	err := s.handoff(ref, &dummyWork)
	if !errors.Is(err, ErrHandoffFailed) {
		t.Fatalf("expected second handoff to fail with ErrHandoffFailed, got %v", err)
	}
}

func TestReleaseOfHandedOffSlotIsRejected(t *testing.T) {
	t.Parallel()

	s := newTestSlots(1)

	ref, ok := reserveSlot(s)
	if !ok {
		t.Fatal("expected reserve to succeed")
	}

	dummyWork := Work{
		Lease: Lease{
			SessionID:        "s1",
			RunID:            "r1",
			InvocationID:     "i1",
			AttemptID:        "a1",
			AdmissionToken:   1,
			OwnerIncarnation: 1,
			ExecutorFence:    1,
		},
		Dispatch: new(kernel.ModelDispatchStartedEvent),
	}

	if err := s.handoff(ref, &dummyWork); err != nil {
		t.Fatalf("handoff failed: %v", err)
	}

	err := s.release(ref)
	if !errors.Is(err, ErrHandoffFailed) {
		t.Fatalf("expected release of handed-off slot to return ErrHandoffFailed, got %v", err)
	}

	_, canReserve := reserveSlot(s)
	if canReserve {
		t.Fatal("expected handed-off slot to still be occupied")
	}
}

func TestSlotsEdgeCases(t *testing.T) {
	t.Parallel()

	s0 := newTestSlots(0)
	if s0.capacity != 1 {
		t.Errorf("expected default capacity 1 for 0, got %d", s0.capacity)
	}

	s := newTestSlots(1)
	if err := s.complete("non-existent"); !errors.Is(err, ErrHandoffFailed) {
		t.Errorf("expected ErrHandoffFailed for unknown complete, got %v", err)
	}
	if err := s.release("non-existent"); !errors.Is(err, ErrHandoffFailed) {
		t.Errorf("expected ErrHandoffFailed for unknown release, got %v", err)
	}
	if err := s.handoff("non-existent", &Work{}); !errors.Is(err, ErrHandoffFailed) {
		t.Errorf("expected ErrHandoffFailed for unknown handoff, got %v", err)
	}

	ref, ok := reserveSlot(s)
	if !ok {
		t.Fatal("reserve failed")
	}
	s.handoffCh <- &Work{} // fill channel
	if err := s.handoff(ref, &Work{}); !errors.Is(err, ErrHandoffFailed) {
		t.Errorf("expected ErrHandoffFailed when handoff channel full, got %v", err)
	}
	<-s.handoffCh // drain
}
