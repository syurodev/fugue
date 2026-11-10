package app

import (
	"context"
	"runtime"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const testIdleGraceReason = "idle_grace"

type gatedGraceClock struct {
	*clock.Fake
	entered chan struct{}
	release chan struct{}
}
type gatedGraceTimer struct {
	clock.Timer
	owner *gatedGraceClock
	once  sync.Once
}

func (c *gatedGraceClock) NewTimer(d time.Duration) clock.Timer {
	t := c.Fake.NewTimer(d)
	select {
	case <-c.entered:
		return t
	default:
		return &gatedGraceTimer{Timer: t, owner: c}
	}
}

func (t *gatedGraceTimer) C() <-chan time.Time {
	t.once.Do(func() { close(t.owner.entered); <-t.owner.release })
	return t.Timer.C()
}

func TestGraceWakeRearmsAfterDisarm(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clk := clock.NewFake(time.Unix(0, 0))
	gated := &gatedGraceClock{Fake: clk, entered: make(chan struct{}), release: make(chan struct{})}
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	drains := make(chan string, 1)
	rt, _ := newRuntime(t, cfg, clk, func(_ context.Context, _ bool, reason string) error { drains <- reason; return nil })
	rt.mu.Lock()
	rt.clk = gated
	rt.mu.Unlock()
	if err := rt.LeaseAcquired(t.Context(), "a", KindApp); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "a")
	<-gated.entered
	// Hold the loop's sampled disarm open until it has consumed the new wake.
	rt.mu.Lock()
	oldDisarm := rt.disarm
	rt.disarm = make(chan struct{})
	rt.mu.Unlock()
	if err := rt.LeaseAcquired(t.Context(), "b", KindApp); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "b")
	close(gated.release)
	defer close(oldDisarm)
	clk.Advance(cfg.IdleGrace)
	select {
	case reason := <-drains:
		if reason != testIdleGraceReason {
			t.Fatalf("reason = %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("grace wake was lost")
	}
}

func TestGraceRandomLeaseCyclesEventuallyDrain(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	clk := clock.NewFake(time.Unix(0, 0))
	cfg := testHostConfig()
	cfg.IdleGrace = 10 * time.Second
	drains := make(chan string, 1)
	rt, _ := newRuntime(t, cfg, clk, func(_ context.Context, _ bool, reason string) error { drains <- reason; return nil })
	var random uint32 = 42
	active := make([]string, 0, 1000)
	for i := range 1000 {
		id := strconv.Itoa(i)
		if err := rt.LeaseAcquired(t.Context(), id, KindApp); err != nil {
			t.Fatal(err)
		}
		active = append(active, id)
		random ^= random << 13
		random ^= random >> 17
		random ^= random << 5
		if random%2 == 0 {
			index := int(random%1000) % len(active)
			rt.LeaseReleased(t.Context(), active[index])
			active[index] = active[len(active)-1]
			active = active[:len(active)-1]
		}
		if i%7 == 0 {
			runtime.Gosched()
		}
	}
	for _, id := range active {
		rt.LeaseReleased(t.Context(), id)
	}
	// Ensure a final arming even if the random sequence already ended idle.
	if err := rt.LeaseAcquired(t.Context(), "final", KindApp); err != nil {
		t.Fatal(err)
	}
	rt.LeaseReleased(t.Context(), "final")
	clk.Advance(cfg.IdleGrace)
	select {
	case reason := <-drains:
		if reason != testIdleGraceReason {
			t.Fatalf("reason = %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("idle host did not drain")
	}
}
