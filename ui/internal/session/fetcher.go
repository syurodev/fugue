package session

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// ResultKind names what a fetcher result carries.
type ResultKind uint8

// The result kinds.
const (
	ResultSessions ResultKind = iota
	ResultPage
)

// Result is one completed read, delivered to the window goroutine.
type Result struct {
	Kind ResultKind
	// Session names the session a page was read for.
	Session string
	// Sessions is the list read; Items is the page read.
	Sessions client.SessionList
	Items    []client.TranscriptItem
	// Older reports whether the page was an older page (before > 0).
	Older bool
	// Err is the read's failure, if any; the caller decides what it shows.
	Err error
}

// request is one queued read.
type request struct {
	session string
	kind    ResultKind
	before  int64
}

// requestBuffer bounds the request queue. A request dropped because the queue
// is full, or because the host is unreachable, is harmless: the window
// re-requests when its triggers fire again.
const requestBuffer = 32

// Fetcher performs the session model's reads off the window goroutine: one
// goroutine takes requests, runs the blocking pkg/client reads against the
// manager's current connection, and hands the results back. Requests while
// the host is unreachable are dropped; the window re-requests on reconnect.
type Fetcher struct {
	clientOf func() *client.Client
	reqs     chan request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewFetcher builds a fetcher that reads through clientOf, which returns the
// manager's current connection or nil while disconnected.
func NewFetcher(clientOf func() *client.Client) *Fetcher {
	return &Fetcher{
		clientOf: clientOf,
		reqs:     make(chan request, requestBuffer),
		results:  make(chan Result, requestBuffer),
		done:     make(chan struct{}),
	}
}

// Start runs the fetcher's goroutine. It ends with Stop or the context.
func (f *Fetcher) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	f.cancel = cancel
	go f.run(runCtx)
}

// Stop ends the fetcher's goroutine and waits for it. It is idempotent.
func (f *Fetcher) Stop() {
	f.stopOnce.Do(func() {
		if f.cancel != nil {
			f.cancel()
		}
	})
	<-f.done
}

// RequestSessions queues a session-list read.
func (f *Fetcher) RequestSessions() {
	f.queue(request{kind: ResultSessions})
}

// RequestPage queues a transcript page read: the newest page when before is
// zero, the page strictly older than it otherwise.
func (f *Fetcher) RequestPage(session string, before int64) {
	f.queue(request{kind: ResultPage, session: session, before: before})
}

// Results receives the completed reads. The window goroutine drains it every
// frame.
func (f *Fetcher) Results() <-chan Result { return f.results }

// queue hands one request over, dropping it when the queue is full.
func (f *Fetcher) queue(r request) {
	select {
	case f.reqs <- r:
	default:
	}
}

// run serves requests until the context ends.
func (f *Fetcher) run(ctx context.Context) {
	defer close(f.done)
	for {
		select {
		case <-ctx.Done():
			return
		case r := <-f.reqs:
			c := f.clientOf()
			if c == nil {
				continue
			}
			if !f.deliver(ctx, c, r) {
				return
			}
		}
	}
}

// perform runs one read and hands its result to the window; it reports
// whether the fetcher should keep serving.
func (f *Fetcher) deliver(ctx context.Context, c *client.Client, r request) bool {
	var res Result
	switch r.kind {
	case ResultSessions:
		sessions, err := c.ListSessions(ctx)
		res = Result{Kind: ResultSessions, Sessions: sessions, Err: err}
	case ResultPage:
		items, err := c.TranscriptPage(ctx, r.session, r.before, PageLimit)
		res = Result{Kind: ResultPage, Session: r.session, Items: items, Older: r.before > 0, Err: err}
	}
	select {
	case f.results <- res:
		return true
	case <-ctx.Done():
		return false
	}
}
