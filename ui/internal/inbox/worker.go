package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const workerBuffer = 16

var (
	errWorkerQueueFull = errors.New("the inbox request queue is full")
	errMissingPin      = errors.New("interaction request_id and incarnation are required")
)

// OpKind identifies one inbox Host API operation executed off the window goroutine.
type OpKind uint8

// The inbox worker operations.
const (
	OpListPending OpKind = iota
	OpAnswer
)

// Request carries one inbox operation pinned to (RequestID, Incarnation).
type Request struct {
	Kind   OpKind
	Pin    Pin
	Answer client.AnswerInteractionInput
}

// Result delivers one completed inbox operation to the window goroutine.
type Result struct {
	Kind        OpKind
	Pin         Pin
	Incarnation string
	List        client.PendingInteractionList
	Answer      client.InteractionAnswerResult
	Code        string
	Err         error
}

// Client is the Host API surface the inbox worker calls.
type Client interface {
	HostStatus(ctx context.Context) (client.HostStatus, error)
	ListPendingInteractions(ctx context.Context) (client.PendingInteractionList, error)
	AnswerInteraction(ctx context.Context, in *client.AnswerInteractionInput) (client.InteractionAnswerResult, error)
}

// Worker runs inbox Host API calls off the window goroutine.
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

// NewWorker builds an inbox worker using clientOf and optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	return &Worker{
		clientOf: clientOf, onResult: onResult, newID: newCommandIDs(),
		reqs: make(chan Request, workerBuffer), results: make(chan Result, workerBuffer), done: make(chan struct{}),
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

// Do queues req for off-thread execution, enforcing Pin and minting CommandID.
func (w *Worker) Do(req *Request) {
	q := *req
	if q.Kind == OpAnswer {
		if !q.Pin.Valid() {
			w.deliver(&Result{Kind: q.Kind, Pin: q.Pin, Err: errMissingPin})
			return
		}
		q.Answer.RequestID, q.Answer.Incarnation = q.Pin.RequestID, q.Pin.Incarnation
		if q.Answer.CommandID == "" {
			q.Answer.CommandID = w.newID()
		}
	}
	select {
	case w.reqs <- q:
	default:
		w.deliver(&Result{Kind: q.Kind, Pin: q.Pin, Err: errWorkerQueueFull})
	}
}

// Results returns the channel delivering completed inbox operations.
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
		w.deliver(&Result{Kind: req.Kind, Pin: req.Pin, Err: client.ErrDisconnected})
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
	res := Result{Kind: req.Kind, Pin: req.Pin}
	switch req.Kind {
	case OpListPending:
		res.List, res.Err = c.ListPendingInteractions(ctx)
		if res.Err == nil {
			if len(res.List.Requests) > 0 {
				res.Incarnation = res.List.Requests[0].Incarnation
			} else if st, stErr := c.HostStatus(ctx); stErr == nil {
				res.Incarnation = st.Incarnation
			}
		}
	case OpAnswer:
		res.Answer, res.Err = c.AnswerInteraction(ctx, &req.Answer)
	}
	if res.Err != nil {
		res.Code, _ = client.InteractionCodeOf(res.Err)
	}
	return res
}

func newCommandIDs() func() string {
	host, err := os.Hostname()
	if err != nil {
		host = "unknown-host"
	}
	prefix := fmt.Sprintf("inbox:%s:%d", host, os.Getpid())
	var mu sync.Mutex
	var seq uint64
	return func() string {
		mu.Lock()
		seq++
		n := seq
		mu.Unlock()
		sum := sha256.Sum256([]byte(prefix + ":" + strconv.FormatUint(n, 10)))
		return hex.EncodeToString(sum[:16])
	}
}
