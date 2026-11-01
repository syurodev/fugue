package clock_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/clock"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

func epoch() time.Time { return time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC) }

func TestFakeNowOnlyMovesWhenAdvanced(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	if !f.Now().Equal(epoch()) {
		t.Fatalf("Now = %v, want %v", f.Now(), epoch())
	}
	f.Advance(90 * time.Second)
	if want := epoch().Add(90 * time.Second); !f.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", f.Now(), want)
	}
}

func TestFakeTimersFireInDeadlineOrder(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	late := f.NewTimer(30 * time.Second)
	early := f.NewTimer(10 * time.Second)
	tied := f.NewTimer(10 * time.Second)
	if got := f.Pending(); got != 3 {
		t.Fatalf("Pending = %d, want 3", got)
	}
	f.Advance(15 * time.Second)
	var fired []string
	for name, timer := range map[string]clock.Timer{"early": early, "tied": tied} {
		select {
		case at := <-timer.C():
			fired = append(fired, name+"@"+at.Sub(epoch()).String())
		default:
			t.Fatalf("%s did not fire", name)
		}
	}
	if len(fired) != 2 {
		t.Fatalf("fired = %v", fired)
	}
	select {
	case <-late.C():
		t.Fatal("the 30s timer fired after 15s")
	default:
	}
	f.Advance(15 * time.Second)
	select {
	case at := <-late.C():
		if want := epoch().Add(30 * time.Second); !at.Equal(want) {
			t.Fatalf("fired at %v, want %v", at, want)
		}
	default:
		t.Fatal("the 30s timer did not fire at 30s")
	}
	if got := f.Pending(); got != 0 {
		t.Fatalf("Pending = %d, want 0", got)
	}
}

// A timer reports the time it was due, not the time the clock was advanced to.
func TestFakeTimerReportsItsDeadline(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	timer := f.NewTimer(time.Second)
	f.Advance(time.Hour)
	got := <-timer.C()
	if want := epoch().Add(time.Second); !got.Equal(want) {
		t.Fatalf("fired at %v, want %v", got, want)
	}
	if !f.Now().Equal(epoch().Add(time.Hour)) {
		t.Fatalf("Now = %v, want one hour on", f.Now())
	}
}

func TestFakeStopAndReset(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	timer := f.NewTimer(10 * time.Second)
	if !timer.Stop() {
		t.Fatal("Stop on a pending timer must report true")
	}
	if timer.Stop() {
		t.Fatal("Stop on a stopped timer must report false")
	}
	f.Advance(time.Minute)
	select {
	case <-timer.C():
		t.Fatal("a stopped timer fired")
	default:
	}
	if timer.Reset(5 * time.Second) {
		t.Fatal("Reset on a stopped timer must report false")
	}
	if !timer.Reset(20 * time.Second) {
		t.Fatal("Reset on a pending timer must report true")
	}
	f.Advance(19 * time.Second)
	select {
	case <-timer.C():
		t.Fatal("the reset timer fired early")
	default:
	}
	f.Advance(time.Second)
	select {
	case <-timer.C():
	default:
		t.Fatal("the reset timer did not fire")
	}
}

func TestFakeResetDropsAnUnreadFiring(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	timer := f.NewTimer(time.Second)
	f.Advance(time.Second)
	timer.Reset(time.Hour)
	select {
	case <-timer.C():
		t.Fatal("a stale firing survived Reset")
	default:
	}
}

// firesImmediately advances the fake clock as soon as a timer exists, which lets
// a test run Sleep to completion without a second goroutine.
type firesImmediately struct{ *clock.Fake }

func (c firesImmediately) NewTimer(d time.Duration) clock.Timer {
	t := c.Fake.NewTimer(d)
	c.Advance(d)
	return t
}

func TestSleepWaitsForTheClock(t *testing.T) {
	t.Parallel()
	f := clock.NewFake(epoch())
	if err := clock.Sleep(t.Context(), firesImmediately{f}, time.Minute); err != nil {
		t.Fatalf("Sleep = %v, want nil", err)
	}
	if want := epoch().Add(time.Minute); !f.Now().Equal(want) {
		t.Fatalf("Now = %v, want %v", f.Now(), want)
	}
}

func TestSleepStopsWhenTheContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := clock.Sleep(ctx, clock.NewFake(epoch()), time.Hour)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Sleep = %v, want context.Canceled", err)
	}
}

func TestRealClockMovesForward(t *testing.T) {
	t.Parallel()
	var c clock.Clock = clock.Real{}
	before := c.Now()
	timer := c.NewTimer(time.Millisecond)
	<-timer.C()
	if diff := cmp.Diff(true, !c.Now().Before(before)); diff != "" {
		t.Fatalf("time went backwards (-want +got):\n%s", diff)
	}
	if timer.Stop() {
		t.Fatal("Stop on a fired timer must report false")
	}
	if timer.Reset(time.Hour) {
		t.Fatal("Reset on a fired timer must report false")
	}
	if !timer.Stop() {
		t.Fatal("Stop on a re-armed timer must report true")
	}
}
