package secrets

import (
	"context"
	"errors"
	"slices"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/cmdid"
	"github.com/syurodev/fugue/ui/internal/reqworker"
)

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
	newID    func() string
	core     *reqworker.Worker[Request, Result]
}

// NewWorker builds a secrets worker using clientOf and optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	w := &Worker{
		clientOf: clientOf,
		newID:    cmdid.New(),
	}
	w.core = reqworker.New(
		reqworker.DefaultBuffer,
		w.handle,
		func(q Request) Result {
			clear(q.Put.Value)
			return Result{Kind: q.Kind, SecretID: q.SecretID, Err: errWorkerQueueFull}
		},
		onResult,
	)
	return w
}

// Start runs the worker goroutine until Stop or ctx cancellation.
func (w *Worker) Start(ctx context.Context) { w.core.Start(ctx) }

// Stop stops the worker goroutine and waits for it to exit.
func (w *Worker) Stop() { w.core.Stop() }

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
	w.core.Do(q)
}

// Results returns the channel delivering completed Secret Store operations.
func (w *Worker) Results() <-chan Result { return w.core.Results() }

func (w *Worker) handle(ctx context.Context, req Request) Result {
	defer clear(req.Put.Value)
	c := w.clientOf()
	if c == nil {
		return Result{Kind: req.Kind, SecretID: req.SecretID, Err: client.ErrDisconnected}
	}
	return w.execute(ctx, c, &req)
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
