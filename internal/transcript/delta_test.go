package transcript

import (
	"context"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

type deltaSink struct {
	mu     sync.Mutex
	events []Delta
	clock  *clock.Fake
	times  []time.Time
}

func (s *deltaSink) Publish(_ context.Context, d Delta) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, d)
	s.times = append(s.times, s.clock.Now())
}

func (s *deltaSink) snapshot() []Delta {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Delta(nil), s.events...)
}

func awaitDelta(t *testing.T, c *clock.Fake, sink *deltaSink, count int) {
	t.Helper()
	for range 100000 {
		if len(sink.snapshot()) >= count {
			return
		}
		if c.Pending() > 0 {
			c.Advance(time.Second/30 + 1)
		}
		runtime.Gosched()
	}
	t.Fatal("delta was not published")
}

func TestDeltaCoalescerCapsAt30PerSecondAndKeepsLatest(t *testing.T) {
	t.Parallel()
	c := clock.NewFake(time.Time{})
	sink := &deltaSink{clock: c}
	d := NewDeltaCoalescer(t.Context(), c, sink, maskingRedactor{})
	d.Update("one", ViewText, "first")
	awaitDelta(t, c, sink, 1)
	d.Update("one", ViewText, "old")
	d.Update("one", ViewText, "latest")
	awaitDelta(t, c, sink, 2)
	if got := sink.snapshot()[1].View.Text; got != "latest" {
		t.Fatalf("latest view = %q", got)
	}
	for i := range 31 {
		d.Update(strconv.Itoa(i), ViewText, "value")
	}
	for i := 3; i <= 33; i++ {
		awaitDelta(t, c, sink, i)
	}
	sink.mu.Lock()
	elapsed := sink.times[30].Sub(sink.times[0])
	sink.mu.Unlock()
	if elapsed < time.Second {
		t.Fatal("more than 30 publishes within a second")
	}

	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestDeltaCoalescerPublishesRedactedViewsAndFlushesOnClose(t *testing.T) {
	t.Parallel()
	c := clock.NewFake(time.Time{})
	sink := &deltaSink{clock: c}
	d := NewDeltaCoalescer(t.Context(), c, sink, maskingRedactor{})
	d.Update("one", ViewText, strings.Repeat("x", 65530)+"cross-secret")
	closer := supervise.Go(t.Context(), "close transcript test", func(context.Context) error { return d.Close() })
	awaitDelta(t, c, sink, 1)
	if err := closer.Wait(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sink.snapshot()[0].View.Text, "cross-") {
		t.Fatal("unredacted delta")
	}
}
