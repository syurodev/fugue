package appwin

import (
	"context"
	"errors"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const fileViewWorkerBuffer = 16

var (
	errFileViewQueueFull = errors.New("the file viewer request queue is full")
	errFileViewNoClient  = errors.New("the host connection is unavailable")
)

// fileOp identifies one off-thread workspace file read (host-api v0.18 §5.4).
type fileOp uint8

// The file viewer worker operations.
const (
	fileOpRead fileOp = iota
	fileOpList
)

// fileRequest carries one read for the worker goroutine.
type fileRequest struct {
	Op                    fileOp
	WorkspaceID, RootName string
	Path                  string
	StartLine, EndLine    int64
}

// fileResult delivers one finished read to the window goroutine. Path echoes
// the request so a stale answer for a closed target is dropped.
type fileResult struct {
	Op      fileOp
	Path    string
	File    client.WorkspaceFile
	Entries []client.WorkspaceDirEntry
	Err     error
}

// fileViewClient is the Host API surface the file viewer worker calls.
type fileViewClient interface {
	ReadWorkspaceFile(ctx context.Context, workspaceID, rootName, path string, startLine, endLine int64) (client.WorkspaceFile, error)
	ListWorkspaceDir(ctx context.Context, workspaceID, rootName, path string) ([]client.WorkspaceDirEntry, error)
}

// fileViewWorker runs the viewer's Host API reads off the window goroutine.
type fileViewWorker struct {
	clientOf func() fileViewClient
	reqs     chan fileRequest
	results  chan fileResult
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// newFileViewWorker builds the worker over clientOf.
func newFileViewWorker(clientOf func() fileViewClient) *fileViewWorker {
	return &fileViewWorker{
		clientOf: clientOf,
		reqs:     make(chan fileRequest, fileViewWorkerBuffer),
		results:  make(chan fileResult, fileViewWorkerBuffer),
		done:     make(chan struct{}),
	}
}

// Start launches the worker goroutine until Stop or ctx cancellation.
func (w *fileViewWorker) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	w.cancel = cancel
	go w.run(runCtx)
}

// Stop stops the worker goroutine and waits for it to exit.
func (w *fileViewWorker) Stop() {
	w.stopOnce.Do(func() {
		if w.cancel != nil {
			w.cancel()
		}
	})
	<-w.done
}

// Do queues req for off-thread execution.
func (w *fileViewWorker) Do(req fileRequest) {
	select {
	case w.reqs <- req:
	default:
		w.deliver(&fileResult{Op: req.Op, Path: req.Path, Err: errFileViewQueueFull})
	}
}

// Results returns the channel delivering completed reads.
func (w *fileViewWorker) Results() <-chan fileResult { return w.results }

func (w *fileViewWorker) deliver(res *fileResult) {
	select {
	case w.results <- *res:
	default:
	}
}

func (w *fileViewWorker) run(ctx context.Context) {
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

func (w *fileViewWorker) handle(ctx context.Context, req *fileRequest) {
	c := w.clientOf()
	if c == nil {
		w.deliver(&fileResult{Op: req.Op, Path: req.Path, Err: errFileViewNoClient})
		return
	}
	switch req.Op {
	case fileOpRead:
		file, err := c.ReadWorkspaceFile(ctx, req.WorkspaceID, req.RootName, req.Path, req.StartLine, req.EndLine)
		w.deliver(&fileResult{Op: req.Op, Path: req.Path, File: file, Err: err})
	case fileOpList:
		entries, err := c.ListWorkspaceDir(ctx, req.WorkspaceID, req.RootName, req.Path)
		w.deliver(&fileResult{Op: req.Op, Path: req.Path, Entries: entries, Err: err})
	}
}
