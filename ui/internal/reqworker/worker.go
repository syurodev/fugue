package reqworker

import (
	"context"
	"sync"
)

// DefaultBuffer is the bounded queue capacity for requests and results.
const DefaultBuffer = 16

// Worker executes typed requests on a background goroutine and delivers
// results onto a bounded channel with an optional wakeup callback.
type Worker[Req, Res any] struct {
	exec     func(ctx context.Context, req Req) Res
	onFull   func(req Req) Res
	onResult func()
	reqs     chan Req
	results  chan Res
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// New builds a bounded request worker using exec to handle each request,
// optional onFull when the request queue is saturated, and optional onResult
// to wake the window event loop after queueing a result.
func New[Req, Res any](
	buf int,
	exec func(ctx context.Context, req Req) Res,
	onFull func(req Req) Res,
	onResult func(),
) *Worker[Req, Res] {
	if buf <= 0 {
		buf = DefaultBuffer
	}
	return &Worker[Req, Res]{
		exec:     exec,
		onFull:   onFull,
		onResult: onResult,
		reqs:     make(chan Req, buf),
		results:  make(chan Res, buf),
		done:     make(chan struct{}),
	}
}

// Start launches the worker goroutine until Stop or ctx cancellation.
func (w *Worker[Req, Res]) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
}

// Stop cancels the worker goroutine and waits for it to exit. It is idempotent.
func (w *Worker[Req, Res]) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	<-w.done
}

// Do queues req for off-thread execution, delivering onFull immediately if
// the queue is full.
func (w *Worker[Req, Res]) Do(req Req) {
	select {
	case w.reqs <- req:
	default:
		if w.onFull != nil {
			res := w.onFull(req)
			w.deliver(res)
		}
	}
}

// Results returns the channel delivering completed responses.
func (w *Worker[Req, Res]) Results() <-chan Res { return w.results }

func (w *Worker[Req, Res]) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.reqs:
			res := w.exec(ctx, req)
			if ctx.Err() != nil {
				return
			}
			select {
			case w.results <- res:
				if w.onResult != nil {
					w.onResult()
				}
			case <-ctx.Done():
				return
			}
		}
	}
}

func (w *Worker[Req, Res]) deliver(res Res) {
	select {
	case w.results <- res:
		if w.onResult != nil {
			w.onResult()
		}
	default:
	}
}
