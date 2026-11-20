package accounts

import (
	"context"
	"errors"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const workerBuffer = 16

var errWorkerQueueFull = errors.New("the accounts request queue is full")

// OpKind identifies one accounts Host API operation executed off the window goroutine.
type OpKind uint8

// The accounts worker operations.
const (
	OpListAccounts OpKind = iota
	OpStartLogin
	OpCancelLogin
)

// Request carries one accounts operation.
type Request struct {
	Kind   OpKind
	Vendor string
}

// Result delivers one completed accounts operation to the window goroutine.
type Result struct {
	Kind        OpKind
	Vendor      string
	Incarnation string
	Accounts    []client.Account
	StartLogin  client.StartLoginResult
	Code        string
	Err         error
}

// Client is the Host API surface the accounts worker calls.
type Client interface {
	HostStatus(ctx context.Context) (client.HostStatus, error)
	ListAccounts(ctx context.Context) ([]client.Account, error)
	StartLogin(ctx context.Context, vendor string) (client.StartLoginResult, error)
	CancelLogin(ctx context.Context, vendor string) error
}

// Worker runs accounts Host API calls off the window goroutine.
type Worker struct {
	clientOf func() Client
	onResult func()
	reqs     chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorker builds an accounts worker using clientOf and optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	return &Worker{
		clientOf: clientOf,
		onResult: onResult,
		reqs:     make(chan Request, workerBuffer),
		results:  make(chan Result, workerBuffer),
		done:     make(chan struct{}),
	}
}

// Start runs the worker goroutine until Stop or ctx cancellation.
func (w *Worker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
}

// Stop stops the worker goroutine and waits for it to exit.
func (w *Worker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	<-w.done
}

// Do queues req for off-thread execution.
func (w *Worker) Do(req *Request) {
	select {
	case w.reqs <- *req:
	default:
		w.deliver(&Result{Kind: req.Kind, Vendor: req.Vendor, Err: errWorkerQueueFull})
	}
}

// Results returns the channel delivering completed accounts operations.
func (w *Worker) Results() <-chan Result { return w.results }

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.reqs:
			w.handle(ctx, &req)
		}
	}
}

func (w *Worker) handle(ctx context.Context, req *Request) {
	c := w.clientOf()
	if c == nil {
		w.deliver(&Result{Kind: req.Kind, Vendor: req.Vendor, Err: client.ErrDisconnected})
		return
	}
	res := w.execute(ctx, c, req)
	if ctx.Err() != nil {
		return
	}
	select {
	case w.results <- res:
		if w.onResult != nil {
			w.onResult()
		}
	case <-ctx.Done():
	}
}

func (w *Worker) deliver(res *Result) {
	select {
	case w.results <- *res:
		if w.onResult != nil {
			w.onResult()
		}
	default:
	}
}

func (w *Worker) execute(ctx context.Context, c Client, req *Request) Result {
	res := Result{Kind: req.Kind, Vendor: req.Vendor}
	switch req.Kind {
	case OpListAccounts:
		res.Accounts, res.Err = c.ListAccounts(ctx)
		if res.Err == nil {
			if st, stErr := c.HostStatus(ctx); stErr == nil {
				res.Incarnation = st.Incarnation
			}
		}
	case OpStartLogin:
		res.StartLogin, res.Err = c.StartLogin(ctx, req.Vendor)
	case OpCancelLogin:
		res.Err = c.CancelLogin(ctx, req.Vendor)
	}
	if res.Err != nil {
		res.Code, _ = client.AccountsCodeOf(res.Err)
	}
	return res
}
