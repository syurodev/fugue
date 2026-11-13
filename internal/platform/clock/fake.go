package clock

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"time"
)

// Fake is a clock that only moves when told to. Advance runs every timer that
// comes due, in deadline order, so a test sees the same sequence each time.
type Fake struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
	seq    uint64
	// armed is closed, then cleared, by the next arm so BlockUntilPending
	// waiters re-check without polling.
	armed chan struct{}
}

// NewFake returns a Fake that starts at start.
func NewFake(start time.Time) *Fake { return &Fake{now: start} }

// Now returns the fake time.
func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

// NewTimer returns a timer that fires when the fake time reaches now+d.
func (f *Fake) NewTimer(d time.Duration) Timer {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := &fakeTimer{clock: f, ch: make(chan time.Time, 1)}
	f.arm(t, d)
	return t
}

// Pending reports how many timers are waiting to fire right now. To wait for
// a goroutine to arm its timer use BlockUntilPending: a poll on Pending
// depends on the scheduler.
func (f *Fake) Pending() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.timers)
}

// BlockUntilPending returns once at least n timers are waiting to fire, or
// with the context's error when ctx ends first. It wakes on each arm, so it
// does not depend on how fast the goroutine under test is scheduled.
func (f *Fake) BlockUntilPending(ctx context.Context, n int) error {
	for {
		f.mu.Lock()
		if len(f.timers) >= n {
			f.mu.Unlock()
			return nil
		}
		if f.armed == nil {
			f.armed = make(chan struct{})
		}
		wake := f.armed
		f.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return fmt.Errorf("wait for pending timers: %w", context.Cause(ctx))
		}
	}
}

// Advance moves the clock forward by d, firing due timers on the way. A timer
// created by a goroutine that reacts to a firing is seen only on the next call.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	target := f.now.Add(d)
	for {
		next := f.nextDue(target)
		if next == nil {
			break
		}
		f.now = next.deadline
		f.timers = slices.DeleteFunc(f.timers, func(t *fakeTimer) bool { return t == next })
		next.armed = false
		select {
		case next.ch <- f.now:
		default:
		}
	}
	f.now = target
}

func (f *Fake) nextDue(target time.Time) *fakeTimer {
	var best *fakeTimer
	for _, t := range f.timers {
		if t.deadline.After(target) {
			continue
		}
		if best == nil || t.deadline.Before(best.deadline) || (t.deadline.Equal(best.deadline) && t.order < best.order) {
			best = t
		}
	}
	return best
}

// arm schedules t; the caller holds f.mu.
func (f *Fake) arm(t *fakeTimer, d time.Duration) {
	f.seq++
	t.order = f.seq
	t.deadline = f.now.Add(d)
	t.armed = true
	f.timers = append(f.timers, t)
	if f.armed != nil {
		close(f.armed)
		f.armed = nil
	}
}

type fakeTimer struct {
	clock    *Fake
	ch       chan time.Time
	deadline time.Time
	order    uint64
	armed    bool
}

func (t *fakeTimer) C() <-chan time.Time { return t.ch }

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	return t.disarm()
}

func (t *fakeTimer) Reset(d time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	was := t.disarm()
	select {
	case <-t.ch:
	default:
	}
	t.clock.arm(t, d)
	return was
}

// disarm removes t from the clock; the caller holds the clock's lock.
func (t *fakeTimer) disarm() bool {
	was := t.armed
	t.armed = false
	t.clock.timers = slices.DeleteFunc(t.clock.timers, func(o *fakeTimer) bool { return o == t })
	return was
}
