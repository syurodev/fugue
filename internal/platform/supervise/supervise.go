package supervise

import (
	"context"
	"errors"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Task is a supervised goroutine. The caller that started it owns it: Stop asks
// it to finish and Wait collects its result.
type Task struct {
	name   string
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

// Go runs fn in a goroutine under a context derived from ctx. The goroutine ends
// when fn returns, when ctx is done or when Stop is called; fn must watch its
// context. A panic in fn is recovered and reported as an Internal error without
// the panic value, which may hold a secret. An empty name or a nil fn starts
// nothing and returns a finished Task carrying an InvalidArgument error.
func Go(ctx context.Context, name string, fn func(context.Context) error) *Task {
	t := &Task{name: name, cancel: func() {}, done: make(chan struct{}), err: nil}
	if name == "" || fn == nil {
		t.err = errcode.New(errcode.InvalidArgument, "supervise.Go needs a name and a function")
		close(t.done)
		return t
	}
	child, cancel := context.WithCancel(ctx)
	t.cancel = cancel
	go t.run(child, fn)
	return t
}

func (t *Task) run(ctx context.Context, fn func(context.Context) error) {
	defer close(t.done)
	defer t.cancel()
	defer func() {
		if recover() != nil {
			t.err = errcode.New(errcode.Internal, "goroutine "+t.name+" panicked")
			slog.ErrorContext(ctx, "goroutine panicked", "goroutine", t.name)
		}
	}()
	t.err = fn(ctx)
	if t.err != nil && !errors.Is(t.err, context.Canceled) {
		slog.ErrorContext(ctx, "goroutine failed", "goroutine", t.name, "error", t.err)
	}
}

// Name returns the name the task was started with.
func (t *Task) Name() string { return t.name }

// Stop cancels the task's context and returns without waiting.
func (t *Task) Stop() { t.cancel() }

// Done is closed when the goroutine has returned.
func (t *Task) Done() <-chan struct{} { return t.done }

// Wait blocks until the goroutine has returned and gives its error.
func (t *Task) Wait() error {
	<-t.done
	return t.err
}
