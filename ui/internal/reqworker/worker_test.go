package reqworker

import (
	"context"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func TestWorkerExecutesAndWakes(t *testing.T) {
	t.Parallel()
	var wakes atomic.Int32
	w := New[int, int](4, func(_ context.Context, v int) int {
		return v * 2
	}, func(v int) int {
		return -v
	}, func() {
		wakes.Add(1)
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.Do(21)
	got := <-w.Results()
	if got != 42 {
		t.Fatalf("got %d, want 42", got)
	}
	if wakes.Load() != 1 {
		t.Fatalf("wakes = %d, want 1", wakes.Load())
	}
}

func TestWorkerFullQueueInvokesOnFull(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	entered := make(chan struct{})
	w := New[int, int](1, func(_ context.Context, v int) int {
		if v == 1 {
			close(entered)
			<-gate
		}
		return v
	}, func(v int) int {
		return -v
	}, nil)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	w.Start(ctx)
	defer w.Stop()

	w.Do(1)
	<-entered
	w.Do(2) // occupies the buffer of 1
	w.Do(3) // overflows and triggers onFull(-3)
	close(gate)

	var got []int
	for range 3 {
		got = append(got, <-w.Results())
	}
	foundNeg := false
	for _, v := range got {
		if v == -3 {
			foundNeg = true
		}
	}
	if !foundNeg {
		t.Fatalf("expected onFull result -3 in %v", got)
	}
}
