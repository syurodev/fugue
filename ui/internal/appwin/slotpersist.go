package appwin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/pkg/client"
)

// The layout's persistence side (host-api §5.2, F10.20a): the stored
// document arrives on the durable appstate topic — while the vault is still
// locked — and every local edit saves back through PutWindow under CAS. The
// RPCs run off the window goroutine in a small worker, like the settings
// worker does.

// primaryWindowID is the initial window's identity when no layouts exist.
// It also identifies the layout saved by single-window builds.
const primaryWindowID = "6f1c9d3e-2a4b-4c8e-9d5f-0a7b8c6d5e4f"

const layoutSaveDelay = 500 * time.Millisecond

var (
	errLayoutQueueFull     = errors.New("the layout request queue is full")
	errLayoutWorkerStopped = errors.New("the layout worker stopped before completing the request")
	errLayoutOperation     = errors.New("unknown layout worker operation")
)

// layoutOp identifies one layout worker operation.
type layoutOp uint8

// The layout worker's operations.
const (
	opPutWindow layoutOp = iota
	opDeleteWindow
)

// layoutRequest carries one worker operation.
type layoutRequest struct {
	kind     layoutOp
	windowID string
	doc      LayoutDoc
	rev      uint64
	cmdSeq   uint64
}

// layoutResult delivers one finished operation to the window goroutine.
type layoutResult struct {
	rev         uint64
	conflict    bool
	current     uint64
	unavailable bool
	err         error
}

// layoutClient is the Host API surface the worker calls.
type layoutClient interface {
	PutAppStateWindow(ctx context.Context, in *client.PutAppStateWindowInput) (uint64, error)
	DeleteAppStateWindow(ctx context.Context, commandID, windowID string, expectedRevision uint64) error
}

// layoutWorker runs the layout's writes off the window goroutine.
type layoutWorker struct {
	clientOf     func() layoutClient
	onResult     func()
	commandScope string
	reqs         chan layoutRequest
	results      chan layoutResult
	cancel       context.CancelFunc
	done         chan struct{}
	stopOnce     sync.Once
}

// newLayoutWorker builds the worker over clientOf and an optional notify.
func newLayoutWorker(clientOf func() layoutClient, onResult func()) *layoutWorker {
	return &layoutWorker{
		clientOf: clientOf,
		onResult: onResult,
		// The host remembers recent command IDs across UI launches, so each
		// worker scopes its IDs to avoid replaying one with a new payload.
		commandScope: rand.Text(),
		reqs:         make(chan layoutRequest, 8),
		results:      make(chan layoutResult, 8),
		done:         make(chan struct{}),
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
			w.finish(ctx, w.handle(ctx, &req))
		}
	}
}

func (w *layoutWorker) handle(ctx context.Context, req *layoutRequest) layoutResult {
	c := w.clientOf()
	if c == nil {
		return layoutResult{unavailable: true, err: client.ErrDisconnected}
	}
	cmd := "layout-" + w.commandScope + "-" + req.windowID + "-" + strconv.FormatUint(req.cmdSeq, 10)
	switch req.kind {
	case opPutWindow:
		return putLayout(ctx, c, cmd, req)
	case opDeleteWindow:
		return deleteLayout(ctx, c, cmd, req)
	}
	return layoutResult{err: errLayoutOperation}
}

func putLayout(ctx context.Context, c layoutClient, cmd string, req *layoutRequest) layoutResult {
	raw, err := EncodeLayout(req.doc)
	if err != nil {
		return layoutResult{err: err}
	}
	in := client.PutAppStateWindowInput{
		CommandID: cmd, WindowID: req.windowID, ExpectedRevision: req.rev, Layout: raw,
	}
	rev, err := c.PutAppStateWindow(ctx, &in)
	if cur, conflict := client.AppStateCurrentRevision(err); conflict {
		// A CAS conflict confirms the original write did not take effect.
		in.CommandID, in.ExpectedRevision = cmd+"-retry", cur
		rev, err = c.PutAppStateWindow(ctx, &in)
	}
	return layoutRPCResult(rev, err)
}

func deleteLayout(ctx context.Context, c layoutClient, cmd string, req *layoutRequest) layoutResult {
	err := c.DeleteAppStateWindow(ctx, cmd, req.windowID, req.rev)
	if cur, conflict := client.AppStateCurrentRevision(err); conflict && cur != 0 {
		err = c.DeleteAppStateWindow(ctx, cmd+"-retry", req.windowID, cur)
	}
	if cur, conflict := client.AppStateCurrentRevision(err); conflict && cur == 0 {
		return layoutResult{}
	}
	return layoutRPCResult(0, err)
}

func layoutRPCResult(rev uint64, err error) layoutResult {
	if err == nil {
		return layoutResult{rev: rev}
	}
	cur, conflict := client.AppStateCurrentRevision(err)
	return layoutResult{conflict: conflict, current: cur, err: fmt.Errorf("persist the window layout: %w", err)}
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

// saveLayoutFrame debounces changes against Gio's frame clock. A close or
// new-window creation can bypass the delay by calling maybeSave directly.
func (g *slotGrid) saveLayoutFrame(gtx *layout.Context, w *layoutWorker) {
	if w == nil || !g.dirty || g.higher {
		return
	}
	if g.layoutDragging() {
		g.saveAt = time.Time{}
		return
	}
	if g.saveAt.IsZero() {
		g.saveAt = gtx.Now.Add(layoutSaveDelay)
	}
	if gtx.Now.Before(g.saveAt) {
		gtx.Execute(op.InvalidateCmd{At: g.saveAt})
		return
	}
	if !g.saving && w.clientOf() != nil {
		g.maybeSave(w)
	}
}

func (g *slotGrid) layoutDragging() bool {
	if g.drag.active {
		return true
	}
	for _, state := range g.splitC {
		if state.Dragging() {
			return true
		}
	}
	for _, state := range g.splitR {
		if state.Dragging() {
			return true
		}
	}
	return false
}

// maybeSave ships one pending edit at a time. Clearing the current dirty
// mark here preserves later edits when this save completes.
func (g *slotGrid) maybeSave(w *layoutWorker) {
	if w == nil || !g.dirty || g.saving || g.higher {
		return
	}
	g.saving, g.dirty = true, false
	w.Do(g.layoutRequest(opPutWindow))
}

func (g *slotGrid) layoutRequest(kind layoutOp) layoutRequest {
	windowID := g.windowID
	if windowID == "" {
		windowID = primaryWindowID
	}
	req := layoutRequest{kind: kind, windowID: windowID, rev: g.rev}
	if kind == opPutWindow {
		// Split handles mutate their weights in place while the RPC runs.
		req.doc = cloneLayout(g.doc)
	}
	g.cmdSeq++
	req.cmdSeq = g.cmdSeq
	return req
}

// finishWindowLayout waits for the window's earlier write, then either
// deletes the stored window or flushes its latest dirty layout before the
// window worker stops.
func (s *screens) finishWindowLayout(ctx context.Context, remove bool) error {
	g, w := s.grid, s.layoutWorker
	if w == nil {
		return nil
	}
	var previousErr error
	if g.saving {
		res := w.await(ctx)
		s.applyLayoutResult(res)
		previousErr = res.err
	}
	if ctx.Err() != nil {
		return fmt.Errorf("finish the window layout: %w", ctx.Err())
	}
	if remove {
		w.Do(g.layoutRequest(opDeleteWindow))
		return w.await(ctx).err
	}
	g.maybeSave(w)
	if !g.saving {
		return previousErr
	}
	res := w.await(ctx)
	s.applyLayoutResult(res)
	return res.err
}

func (w *layoutWorker) await(ctx context.Context) layoutResult {
	select {
	case res := <-w.Results():
		return res
	case <-w.done:
		return layoutResult{err: errLayoutWorkerStopped}
	case <-ctx.Done():
		return layoutResult{err: fmt.Errorf("wait for the window layout: %w", ctx.Err())}
	}
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
	g.docGen++
	g.resetShellMotion()
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

// applyLayoutSnapshot restores the default window outside the multi-window
// coordinator. A running window applies a startup layout only once.
func (s *screens) applyLayoutSnapshot(snap *client.AppStateSnapshot) {
	if snap == nil || s.grid == nil || s.grid.loaded {
		return
	}
	sorted := sortAppStateWindows(snap.Windows)
	if len(sorted) == 0 {
		s.applyWindowRecord(nil)
		return
	}
	s.applyWindowRecord(&sorted[0])
}

// sortAppStateWindows orders stored windows deterministically with
// primaryWindowID first (if present), then the remaining windows by WindowID.
func sortAppStateWindows(windows []client.AppStateWindow) []client.AppStateWindow {
	if len(windows) == 0 {
		return nil
	}
	out := slices.Clone(windows)
	slices.SortFunc(out, func(a, b client.AppStateWindow) int {
		if a.WindowID == primaryWindowID && b.WindowID != primaryWindowID {
			return -1
		}
		if b.WindowID == primaryWindowID && a.WindowID != primaryWindowID {
			return 1
		}
		return strings.Compare(a.WindowID, b.WindowID)
	})
	return out
}

// applyWindowRecord restores the window's document and identity before
// the vault opens (desktop-app §5.3).
func (s *screens) applyWindowRecord(record *client.AppStateWindow) {
	g := s.grid
	g.loaded = true
	if record == nil {
		g.windowID = primaryWindowID
		return
	}
	g.windowID, g.rev = record.WindowID, record.Revision
	res, err := DecodeLayout(record.Layout)
	if err != nil {
		s.fail(err)
		return
	}
	g.applyStored(res, record.Revision)
}

func (s *screens) applyLayoutResults(results []layoutResult) {
	for _, res := range results {
		s.applyLayoutResult(res)
	}
}

func (s *screens) applyLayoutResult(res layoutResult) {
	g := s.grid
	g.saving = false
	switch {
	case res.err == nil:
		g.rev = res.rev
	case res.conflict:
		// The worker already retried; keep the current CAS token for the
		// next edit without looping on a competing writer.
		g.rev = res.current
	case res.unavailable:
		// The worker never obtained a client for this request.
		g.dirty = true
	default:
		s.fail(res.err)
	}
}
