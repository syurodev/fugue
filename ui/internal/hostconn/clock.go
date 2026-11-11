package hostconn

import "time"

// Timer is one stoppable one-shot timer.
type Timer interface {
	// C fires once after the timer's duration.
	C() <-chan time.Time
	// Stop cancels the timer; it reports whether the timer had not fired.
	Stop() bool
}

// Clock is the manager's only time source: reconnect backoff arms its timers
// here, so tests drive reconnection deterministically and production code
// never reads the wall clock.
type Clock interface {
	NewTimer(d time.Duration) Timer
}

// RealClock implements Clock with the standard library timers. It is the
// production wiring; tests use a fake.
type RealClock struct{}

// NewTimer implements Clock.
func (RealClock) NewTimer(d time.Duration) Timer {
	t := time.NewTimer(d)
	return realTimer{t}
}

// Now reads the wall clock; the markdown preview of F10.3b uses RealClock as
// the highlight budget's time source.
func (RealClock) Now() time.Time { return time.Now() }

// realTimer adapts *time.Timer to Timer.
type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time { return r.t.C }

func (r realTimer) Stop() bool { return r.t.Stop() }

// backoffBase and backoffCap bound the reconnect backoff: the first retry
// waits half a second, every failure doubles the wait, and no wait exceeds
// ten seconds.
const (
	backoffBase = 500 * time.Millisecond
	backoffCap  = 10 * time.Second
)

// backoffFor returns the wait before the nth retry, counting from 1, capped
// at backoffCap.
func backoffFor(n int) time.Duration {
	d := backoffBase
	for range n - 1 {
		d *= 2
		if d >= backoffCap {
			return backoffCap
		}
	}
	return d
}
