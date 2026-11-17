package testutil

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

// HangTimeout bounds a wait that must never take long. It is generous and
// fixed on purpose: it exists to turn a deadlock into a failure before the
// package timeout, never to measure speed. A test that needs a tighter bound
// is asserting performance and belongs in internal/perf.
const HangTimeout = time.Minute

// errHang marks a wait that outlived its hang timeout.
var errHang = errors.New("hang")

// pollEvery is the real interval between checks of state outside the process.
const pollEvery = 10 * time.Millisecond

// tb is the part of testing.TB the helpers use; a narrow interface lets the
// package test the failure paths with a recording stand-in.
type tb interface {
	Helper()
	Fatalf(format string, args ...any)
	Cleanup(fn func())
	Context() context.Context
}

func hangGuard(t tb, d time.Duration) <-chan time.Time {
	t.Helper()
	timer := clock.Real{}.NewTimer(d)
	t.Cleanup(func() { timer.Stop() })
	return timer.C()
}

// Await returns the next value of ch and fails the test if none arrives within
// HangTimeout. Wait for a process or server by a signal it sends (a closed
// channel, a ready line), not by polling the time.
func Await[T any](t tb, ch <-chan T) T {
	t.Helper()
	return await(t, ch, HangTimeout)
}

func await[T any](t tb, ch <-chan T, d time.Duration) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-hangGuard(t, d):
		t.Fatalf("hang: no signal within %v", d)
		var zero T
		return zero
	}
}

// Poll calls cond until it reports true and fails the test after HangTimeout.
// Use it only for state outside the process that sends no signal (a pid file,
// a path appearing); inside the process, wait on a channel with Await.
func Poll(t tb, what string, cond func() bool) {
	t.Helper()
	poll(t, what, HangTimeout, cond)
}

func poll(t tb, what string, d time.Duration, cond func() bool) {
	t.Helper()
	if err := waitFor(t.Context(), what, d, cond); err != nil {
		t.Fatalf("%v", err)
	}
}

// WaitFor is Poll for code with no testing.T, such as the helper process a
// test re-executes: it returns an error instead of failing a test.
func WaitFor(ctx context.Context, what string, cond func() bool) error {
	return waitFor(ctx, what, HangTimeout, cond)
}

func waitFor(ctx context.Context, what string, d time.Duration, cond func() bool) error {
	timer := clock.Real{}.NewTimer(d)
	defer timer.Stop()
	for !cond() {
		select {
		case <-timer.C():
			return fmt.Errorf("%w: %s did not happen within %v", errHang, what, d)
		default:
		}
		if err := clock.Sleep(ctx, clock.Real{}, pollEvery); err != nil {
			return fmt.Errorf("poll %s: %w", what, err)
		}
	}
	return nil
}

// Spin calls cond until it reports true, yielding to other goroutines between
// calls, and fails the test after HangTimeout. Use it for state inside the
// process that sends no signal, such as a timer armed on a fake clock that the
// test must advance, where Poll's real 10 ms pause would slow every round.
// cond may do work (advance the clock, drain a queue) as well as check.
func Spin(t tb, what string, cond func() bool) {
	t.Helper()
	spin(t, what, HangTimeout, cond)
}

func spin(t tb, what string, d time.Duration, cond func() bool) {
	t.Helper()
	guard := hangGuard(t, d)
	for !cond() {
		select {
		case <-guard:
			t.Fatalf("hang: %s did not happen within %v", what, d)
			return
		default:
		}
		runtime.Gosched()
	}
}

// OSClock is the clock for a test whose subject owns real operating-system
// timers, such as a process teardown deadline or a lock retry against another
// process. It supplies timers and the current time to that subject; a test
// never reads Now from it to measure how long something took.
func OSClock() clock.Clock {
	return clock.Real{}
}
