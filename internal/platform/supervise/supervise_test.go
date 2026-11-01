package supervise_test

import (
	"context"
	"errors"
	"testing"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var errWork = errors.New("work failed")

func TestGoReturnsTheFunctionsResult(t *testing.T) {
	t.Parallel()
	ok := supervise.Go(t.Context(), "ok", func(context.Context) error { return nil })
	if err := ok.Wait(); err != nil {
		t.Errorf("Wait = %v, want nil", err)
	}
	failing := supervise.Go(t.Context(), "failing", func(context.Context) error { return errWork })
	if err := failing.Wait(); !errors.Is(err, errWork) {
		t.Errorf("Wait = %v, want the function's error", err)
	}
	if failing.Name() != "failing" {
		t.Errorf("Name = %q", failing.Name())
	}
}

func TestStopCancelsTheTaskContext(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	task := supervise.Go(t.Context(), "waiter", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})
	<-started
	task.Stop()
	<-task.Done()
	if err := task.Wait(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait = %v, want context.Canceled", err)
	}
}

func TestParentCancellationStopsTheTask(t *testing.T) {
	t.Parallel()
	parent, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	task := supervise.Go(parent, "child", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return nil
	})
	<-started
	cancel()
	if err := task.Wait(); err != nil {
		t.Fatalf("Wait = %v, want nil", err)
	}
}

// A panic must not take the process down, and its value must not escape: it may
// have been built from a secret.
func TestPanicIsRecoveredWithoutItsValue(t *testing.T) {
	t.Parallel()
	task := supervise.Go(t.Context(), "boom", func(context.Context) error {
		panic("sk-live-0123456789")
	})
	err := task.Wait()
	if errcode.CodeOf(err) != errcode.Internal {
		t.Fatalf("Wait = %v, want an Internal error", err)
	}
	if got := err.Error(); got != "internal: goroutine boom panicked" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestGoRefusesAnUnnamedOrEmptyTask(t *testing.T) {
	t.Parallel()
	ran := false
	cases := map[string]*supervise.Task{
		"no name": supervise.Go(t.Context(), "", func(context.Context) error { ran = true; return nil }),
		"no func": supervise.Go(t.Context(), "named", nil),
	}
	for name, task := range cases {
		if code := errcode.CodeOf(task.Wait()); code != errcode.InvalidArgument {
			t.Errorf("%s: code = %q, want invalid_argument", name, code)
		}
		task.Stop()
	}
	if ran {
		t.Error("a task without a name must not run")
	}
}
