package testutil

import (
	"context"
	"strings"
	"testing"
	"time"
)

// failed is the sentinel a recording stand-in panics with to stop the helper
// the way testing.T.Fatalf stops a test.
type failed struct{ msg string }

type recorder struct {
	testing.TB
	context func() context.Context
	cleanup []func()
}

func (r *recorder) Helper()                           {}
func (r *recorder) Context() context.Context          { return r.context() }
func (r *recorder) Cleanup(fn func())                 { r.cleanup = append(r.cleanup, fn) }
func (r *recorder) Fatalf(format string, args ...any) { panic(failed{msg: format}) }

func fatalMessage(t *testing.T, fn func()) (msg string) {
	t.Helper()
	defer func() {
		if p := recover(); p != nil {
			f, ok := p.(failed)
			if !ok {
				panic(p)
			}
			msg = f.msg
		}
	}()
	fn()
	return ""
}

func TestAwaitReturnsTheSignal(t *testing.T) {
	t.Parallel()
	ch := make(chan int, 1)
	ch <- 7
	if got := Await(t, ch); got != 7 {
		t.Fatalf("Await = %d, want 7", got)
	}
}

func TestAwaitFailsWhenNothingArrives(t *testing.T) {
	t.Parallel()
	r := &recorder{context: t.Context}
	msg := fatalMessage(t, func() { await(r, make(chan int), time.Millisecond) })
	if !strings.Contains(msg, "hang") {
		t.Fatalf("message = %q, want a hang report", msg)
	}
	for _, fn := range r.cleanup {
		fn()
	}
}

func TestHangGuardFiresAndStops(t *testing.T) {
	t.Parallel()
	r := &recorder{context: t.Context}
	<-hangGuard(r, time.Millisecond)
	for _, fn := range r.cleanup {
		fn()
	}
	if HangTimeout < 30*time.Second {
		t.Fatalf("HangTimeout = %v, a hang guard must be generous", HangTimeout)
	}
}

func TestPollReturnsOnceTheConditionHolds(t *testing.T) {
	t.Parallel()
	calls := 0
	Poll(t, "third call", func() bool {
		calls++
		return calls == 3
	})
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPollFailsAfterTheGuard(t *testing.T) {
	t.Parallel()
	r := &recorder{context: t.Context}
	msg := fatalMessage(t, func() { poll(r, "never", time.Millisecond, func() bool { return false }) })
	if !strings.Contains(msg, "hang") {
		t.Fatalf("message = %q, want a hang report", msg)
	}
}

func TestPollFailsWhenTheTestContextEnds(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r := &recorder{context: func() context.Context { return ctx }}
	msg := fatalMessage(t, func() { poll(r, "never", HangTimeout, func() bool { return false }) })
	if !strings.Contains(msg, "poll") {
		t.Fatalf("message = %q, want a poll failure", msg)
	}
}

func TestOSClockHasRealTimers(t *testing.T) {
	t.Parallel()
	timer := OSClock().NewTimer(time.Millisecond)
	Await(t, timer.C())
}
