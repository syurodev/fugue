package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const workerBuffer = 16

var errWorkerQueueFull = errors.New("the secrets request queue is full")

// OpKind identifies one Secret Store Host API operation executed off the window goroutine.
type OpKind uint8

// The Secret Store worker operations.
const (
	OpListSecrets OpKind = iota
	OpPutSecret
	OpDeleteSecret
)

// Request carries one Secret Store operation.
type Request struct {
	Kind      OpKind
	ScopeKind string
	ScopeID   string
	Put       client.PutSecretInput
	SecretID  string
	CommandID string
}

// Result delivers one completed Secret Store operation to the window goroutine.
type Result struct {
	Kind     OpKind
	Secrets  []client.SecretMeta
	Secret   client.SecretMeta
	Deleted  bool
	SecretID string
	Err      error
}

// Client is the Host API surface the secrets worker calls.
type Client interface {
	ListSecrets(ctx context.Context, scopeKind, scopeID string) ([]client.SecretMeta, error)
	PutSecret(ctx context.Context, in *client.PutSecretInput) (client.SecretMeta, error)
	DeleteSecret(ctx context.Context, commandID, secretID string) (bool, error)
}

// Worker runs Secret Store Host API calls off the window goroutine.
type Worker struct {
	clientOf func() Client
	onResult func()
	newID    func() string
	reqs     chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorker builds a secrets worker using clientOf and optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	return &Worker{
		clientOf: clientOf,
		onResult: onResult,
		newID:    newCommandIDs(),
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

// Do queues req for off-thread execution, minting CommandID for mutations and
// cloning/clearing plaintext buffers on zero-retention paths.
func (w *Worker) Do(req *Request) {
	q := *req
	if q.Kind == OpPutSecret {
		q.Put.Value = slices.Clone(req.Put.Value)
		clear(req.Put.Value)
		if q.Put.CommandID == "" {
			q.Put.CommandID = w.newID()
		}
	} else if q.Kind == OpDeleteSecret && q.CommandID == "" {
		q.CommandID = w.newID()
	}
	select {
	case w.reqs <- q:
	default:
		clear(q.Put.Value)
		w.deliver(&Result{Kind: q.Kind, SecretID: q.SecretID, Err: errWorkerQueueFull})
	}
}

// Results returns the channel delivering completed Secret Store operations.
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
	defer clear(req.Put.Value)
	c := w.clientOf()
	if c == nil {
		w.deliver(&Result{Kind: req.Kind, SecretID: req.SecretID, Err: client.ErrDisconnected})
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
	res := Result{Kind: req.Kind, SecretID: req.SecretID}
	switch req.Kind {
	case OpListSecrets:
		list, err := c.ListSecrets(ctx, req.ScopeKind, req.ScopeID)
		res.Secrets, res.Err = list, err
	case OpPutSecret:
		defer clear(req.Put.Value)
		sec, err := c.PutSecret(ctx, &req.Put)
		res.Secret, res.Err = sec, err
	case OpDeleteSecret:
		deleted, err := c.DeleteSecret(ctx, req.CommandID, req.SecretID)
		res.Deleted, res.Err = deleted, err
	}
	return res
}

func newCommandIDs() func() string {
	var seq uint64
	pid := os.Getpid()
	return func() string {
		seq++
		sum := sha256.Sum256([]byte(fmt.Sprintf("ui-secrets-%d-%d", pid, seq)))
		return "ui-sec-" + strconv.FormatUint(seq, 10) + "-" + hex.EncodeToString(sum[:6])
	}
}
