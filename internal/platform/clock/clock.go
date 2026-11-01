package clock

import (
	"context"
	"fmt"
	"time"
)

// Clock reads the time and creates timers. Code that needs time takes a Clock
// so tests can drive it with Fake instead of sleeping.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
}

// Timer fires once on C after its duration, like time.Timer.
type Timer interface {
	C() <-chan time.Time
	// Stop prevents the timer from firing and reports whether it was still
	// pending.
	Stop() bool
	// Reset re-arms the timer to fire after d and reports whether it was still
	// pending.
	Reset(d time.Duration) bool
}

// Real is the wall clock.
type Real struct{}

// Now returns the current wall-clock time.
func (Real) Now() time.Time { return time.Now() }

// NewTimer returns a timer backed by the runtime.
func (Real) NewTimer(d time.Duration) Timer { return realTimer{t: time.NewTimer(d)} }

type realTimer struct{ t *time.Timer }

func (r realTimer) C() <-chan time.Time        { return r.t.C }
func (r realTimer) Stop() bool                 { return r.t.Stop() }
func (r realTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// Sleep waits for d on c, or until ctx is done, whichever is first. It returns
// the context's error when the context ends the wait.
func Sleep(ctx context.Context, c Clock, d time.Duration) error {
	t := c.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C():
		return nil
	case <-ctx.Done():
		return fmt.Errorf("sleep interrupted: %w", context.Cause(ctx))
	}
}
