package vault

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const redactedMask = "[redacted]"

var errWorkerQueueFull = errors.New("the vault request queue is full")

// OpKind identifies one vault operation executed off the window goroutine.
type OpKind uint8

// The vault worker operations.
const (
	OpStatus OpKind = iota
	OpSetup
	OpUnlock
	OpUnlockRecovery
	OpLock
)

// Request carries one vault operation. Secret is cleared immediately after the
// RPC returns, on cancellation, and when dropped. Every formatting path redacts it.
type Request struct {
	Kind   OpKind
	Secret []byte
}

// String implements fmt.Stringer without revealing Secret.
func (r Request) String() string {
	return fmt.Sprintf("{Kind:%d Secret:%s}", r.Kind, redactedMask)
}

// GoString implements fmt.GoStringer without revealing Secret.
func (r Request) GoString() string {
	return fmt.Sprintf("vault.Request{Kind:%d, Secret:%s}", r.Kind, redactedMask)
}

// Format implements fmt.Formatter so no verb prints Secret.
func (r Request) Format(f fmt.State, _ rune) {
	if _, err := f.Write([]byte(r.String())); err != nil {
		return
	}
}

// LogValue implements slog.LogValuer without revealing Secret.
func (r Request) LogValue() slog.Value {
	return slog.GroupValue(slog.Int("kind", int(r.Kind)), slog.String("secret", redactedMask))
}

// Result delivers the completed vault operation to the window goroutine.
type Result struct {
	Kind        OpKind
	Status      *Status
	RecoveryKey []byte
	Code        string
	Err         error
}

// RetryAfter returns the server retry-after delay in seconds when known.
func (r Result) RetryAfter() int64 {
	if r.Status != nil {
		return r.Status.RetryAfterSeconds
	}
	return 0
}

// String implements fmt.Stringer without revealing RecoveryKey.
func (r Result) String() string {
	return fmt.Sprintf("{Kind:%d Status:%+v RecoveryKey:%s Code:%q RetryAfterSeconds:%d Err:%v}",
		r.Kind, r.Status, redactedMask, r.Code, r.RetryAfter(), r.Err)
}

// GoString implements fmt.GoStringer without revealing RecoveryKey.
func (r Result) GoString() string {
	return fmt.Sprintf("vault.Result{Kind:%d, Status:%#v, RecoveryKey:%s, Code:%q, RetryAfterSeconds:%d, Err:%v}",
		r.Kind, r.Status, redactedMask, r.Code, r.RetryAfter(), r.Err)
}

// Format implements fmt.Formatter so no verb prints RecoveryKey.
func (r Result) Format(f fmt.State, _ rune) {
	if _, err := f.Write([]byte(r.String())); err != nil {
		return
	}
}

// LogValue implements slog.LogValuer without revealing RecoveryKey.
func (r Result) LogValue() slog.Value {
	state := ""
	if r.Status != nil {
		state = r.Status.State
	}
	return slog.GroupValue(
		slog.Int("kind", int(r.Kind)),
		slog.String("state", state),
		slog.String("code", r.Code),
		slog.String("recovery_key", redactedMask),
	)
}

// Clear zeros RecoveryKey in memory.
func (r Result) Clear() {
	clear(r.RecoveryKey)
}

// Client is the Host API client surface the vault worker calls.
type Client interface {
	VaultStatus(ctx context.Context) (client.VaultStatus, error)
	SetupVault(ctx context.Context, passphrase []byte) (client.VaultSetupResult, error)
	UnlockVault(ctx context.Context, passphrase []byte) (client.VaultStatus, error)
	UnlockVaultWithRecovery(ctx context.Context, recoveryKey []byte) (client.VaultStatus, error)
	LockVault(ctx context.Context) (client.VaultStatus, error)
}

const workerBuffer = 8

// Worker executes vault RPCs off the window goroutine and clears secret byte
// slices on every path.
type Worker struct {
	clientOf func() Client
	reqs     chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorker builds a vault worker using clientOf to obtain the active client.
func NewWorker(clientOf func() Client) *Worker {
	return &Worker{
		clientOf: clientOf,
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

// Do queues req for execution, clearing req.Secret if the queue is full.
func (w *Worker) Do(req *Request) {
	select {
	case w.reqs <- *req:
		// Clear the caller's reference slice header only after ownership passes;
		// the worker clears the underlying bytes after the RPC returns.
		req.Secret = nil
	default:
		clear(req.Secret)
		req.Secret = nil
		w.results <- Result{Kind: req.Kind, Err: errWorkerQueueFull}
	}
}

// Results returns the channel delivering completed vault operations.
func (w *Worker) Results() <-chan Result { return w.results }

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	defer w.drainQueued()
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.reqs:
			w.handle(ctx, &req)
		}
	}
}

func (w *Worker) drainQueued() {
	for {
		select {
		case req := <-w.reqs:
			clear(req.Secret)
		default:
			return
		}
	}
}

func (w *Worker) handle(ctx context.Context, req *Request) {
	defer clear(req.Secret)
	c := w.clientOf()
	if c == nil {
		return
	}
	res := w.execute(ctx, c, req)
	// Clear before delivering: a caller that reads the result must already
	// see the request bytes zeroed, not after a race with the deferred clear.
	clear(req.Secret)
	if ctx.Err() != nil {
		res.Clear()
		return
	}
	select {
	case w.results <- res:
	case <-ctx.Done():
		res.Clear()
	}
}

func (w *Worker) execute(ctx context.Context, c Client, req *Request) Result {
	var (
		st  client.VaultStatus
		rec []byte
		err error
	)
	switch req.Kind {
	case OpStatus:
		st, err = c.VaultStatus(ctx)
	case OpSetup:
		var setupRes client.VaultSetupResult
		setupRes, err = c.SetupVault(ctx, req.Secret)
		st = setupRes.Status
		rec = setupRes.RecoveryKey
	case OpUnlock:
		st, err = c.UnlockVault(ctx, req.Secret)
	case OpUnlockRecovery:
		st, err = c.UnlockVaultWithRecovery(ctx, req.Secret)
	case OpLock:
		st, err = c.LockVault(ctx)
	}
	code, _ := client.VaultCodeOf(err)
	retryAfter, _ := client.VaultRetryAfter(err)
	resSt := statusFromClient(st)
	if retryAfter > 0 {
		resSt.RetryAfterSeconds = retryAfter
	}
	res := Result{
		Kind:        req.Kind,
		RecoveryKey: rec,
		Code:        code,
		Err:         err,
	}
	if err == nil || retryAfter > 0 {
		res.Status = &resSt
	}
	return res
}

func statusFromClient(st client.VaultStatus) Status {
	return Status{
		State:             st.State,
		Revision:          st.Revision,
		Incarnation:       st.Incarnation,
		RetryAfterSeconds: st.RetryAfterSeconds,
	}
}
