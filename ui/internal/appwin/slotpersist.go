package appwin

import (
	"context"
	"errors"
	"strconv"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// The layout's persistence side (host-api §5.2, F10.20a): the stored
// document arrives on the durable appstate topic — while the vault is still
// locked — and every local edit saves back through PutWindow under CAS. The
// RPCs run off the window goroutine in a small worker, like the settings
// worker does.

// primaryWindowID is the app's one window identity in the single-window
// scope of F10.20b: the same window every start, so its layout restores.
// The multi-window item (F10.21) generates an id per window.
const primaryWindowID = "6f1c9d3e-2a4b-4c8e-9d5f-0a7b8c6d5e4f"

var errLayoutQueueFull = errors.New("the layout request queue is full")

// layoutOp identifies one layout worker operation.
type layoutOp uint8

// The layout worker's operations.
const (
	opPutWindow layoutOp = iota
)

// layoutRequest carries one worker operation.
type layoutRequest struct {
	kind   layoutOp
	doc    LayoutDoc
	rev    uint64
	cmdSeq uint64
}

// layoutResult delivers one finished operation to the window goroutine.
type layoutResult struct {
	rev      uint64
	raw      []byte
	conflict bool
	current  uint64
	err      error
}

// layoutClient is the Host API surface the worker calls.
type layoutClient interface {
	PutAppStateWindow(ctx context.Context, in *client.PutAppStateWindowInput) (uint64, error)
}

// layoutWorker runs the layout's writes off the window goroutine.
type layoutWorker struct {
	clientOf func() layoutClient
	onResult func()
	reqs     chan layoutRequest
	results  chan layoutResult
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// newLayoutWorker builds the worker over clientOf and an optional notify.
func newLayoutWorker(clientOf func() layoutClient, onResult func()) *layoutWorker {
	return &layoutWorker{
		clientOf: clientOf,
		onResult: onResult,
		reqs:     make(chan layoutRequest, 8),
		results:  make(chan layoutResult, 8),
		done:     make(chan struct{}),
	}
}

// Start launches the worker goroutine until Stop.
func (w *layoutWorker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
}

// Stop stops the worker and waits for it to exit.
func (w *layoutWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	<-w.done
}

// Do queues one write; a full queue reports back on the results channel.
func (w *layoutWorker) Do(req layoutRequest) {
	select {
	case w.reqs <- req:
	default:
		w.deliver(layoutResult{err: errLayoutQueueFull})
	}
}

// Results returns the finished-operations channel.
func (w *layoutWorker) Results() <-chan layoutResult { return w.results }

func (w *layoutWorker) run(ctx context.Context) {
	defer close(w.done)
	for {
		select {
		case <-ctx.Done():
			return
		case req := <-w.reqs:
			w.handle(ctx, req)
		}
	}
}

func (w *layoutWorker) handle(ctx context.Context, req layoutRequest) {
	c := w.clientOf()
	if c == nil {
		w.deliver(layoutResult{err: client.ErrDisconnected})
		return
	}
	raw, err := EncodeLayout(req.doc)
	res := layoutResult{}
	if err != nil {
		res.err = err
		w.finish(ctx, res)
		return
	}
	res.raw = raw
	cmd := "layout-" + primaryWindowID + "-" + strconv.FormatUint(req.cmdSeq, 10)
	rev, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID:        cmd,
		WindowID:         primaryWindowID,
		ExpectedRevision: req.rev,
		Layout:           raw,
	})
	if err != nil {
		if cur, conflict := client.AppStateCurrentRevision(err); conflict {
			// One retry against the stored revision, the same policy the
			// preferences save follows (host-api §5.2).
			res.conflict, res.current = true, cur
			rev, err = c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
				CommandID:        cmd + "-retry",
				WindowID:         primaryWindowID,
				ExpectedRevision: cur,
				Layout:           raw,
			})
		}
	}
	if err != nil {
		res.err = err
		w.finish(ctx, res)
		return
	}
	res.rev = rev
	w.finish(ctx, res)
}

// finish delivers a result unless the run context ended first.
func (w *layoutWorker) finish(ctx context.Context, res layoutResult) {
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

func (w *layoutWorker) deliver(res layoutResult) {
	select {
	case w.results <- res:
		if w.onResult != nil {
			w.onResult()
		}
	default:
	}
}

// maybeSave ships one pending layout edit to the worker, one in flight at a
// time; the finished result clears the marks.
func (g *slotGrid) maybeSave(w *layoutWorker) {
	if w == nil || !g.dirty || g.saving || g.higher {
		return
	}
	g.saving = true
	g.cmdSeq++
	w.Do(layoutRequest{kind: opPutWindow, doc: g.doc, rev: g.rev, cmdSeq: g.cmdSeq})
}

// applyStored applies a stored layout document at rev: a newer schema runs
// on the default without overwriting (host-api §5.2). The first application
// seeds the rail from the main slot.
func (g *slotGrid) applyStored(res DecodeLayoutResult, rev uint64) {
	g.rev, g.loaded = rev, true
	if res.HigherSchema {
		g.higher = true
		return
	}
	g.doc = res.Doc
	g.syncFromDoc()
	if g.applyGeometry != nil && res.Doc.Window.WidthDp > 0 && res.Doc.Window.HeightDp > 0 {
		g.applyGeometry(res.Doc.Window.WidthDp, res.Doc.Window.HeightDp)
	}
}

// drainLayoutResults collects the worker's finished results for one frame.
func drainLayoutResults(w *layoutWorker) []layoutResult {
	var out []layoutResult
	for {
		select {
		case r := <-w.Results():
			out = append(out, r)
		default:
			return out
		}
	}
}

// applyLayoutSnapshot folds the appstate topic's snapshot into the grid:
// the stored document of this window restores the shell before the vault
// opens (desktop-app §5.3).
func (s *screens) applyLayoutSnapshot(snap *client.AppStateSnapshot) {
	if snap == nil || s.grid == nil {
		return
	}
	for _, w := range snap.Windows {
		if w.WindowID != primaryWindowID {
			continue
		}
		res, err := DecodeLayout(w.Layout)
		if err != nil {
			s.fail(err)
			continue
		}
		s.grid.applyStored(res, w.Revision)
	}
	s.grid.loaded = true
}

// applyLayoutResults folds the worker's finished writes into the grid.
func (s *screens) applyLayoutResults(results []layoutResult) {
	for _, res := range results {
		s.grid.saving = false
		if res.err != nil {
			if res.conflict {
				// The worker already retried; a second conflict leaves the
				// edit unsaved and the user's next edit tries again.
				s.grid.rev = res.current
				continue
			}
			s.fail(res.err)
			continue
		}
		s.grid.rev = res.rev
		s.grid.dirty = false
	}
}
