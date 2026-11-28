package memory

import (
	"context"
	"errors"
	"path/filepath"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/reqworker"
)

var errWorkerQueueFull = errors.New("the memory request queue is full")

// OpKind identifies one workspace memory Host API operation executed off the window goroutine.
type OpKind uint8

// The workspace memory worker operations.
const (
	OpListEntries OpKind = iota
	OpSearchEntries
	OpGetEntry
	OpGetEntryHistory
	OpWriteEntry
	OpVerifyEntry
	OpRestoreEntry
	OpExportToFile
	OpPreviewImportFile
	OpPreviewImportBytes
	OpApplyImport
)

// Request carries one workspace memory operation.
type Request struct {
	Kind            OpKind
	WorkspaceID     string
	List            client.ListMemoryInput
	Search          client.SearchMemoryInput
	EntryID         string
	Label           string
	Write           client.WriteMemoryInput
	Revision        uint64
	RestoreRevision uint64
	FilePath        string
	ImportBytes     []byte
	Apply           client.ApplyMemoryImportInput
}

// Result delivers one completed workspace memory operation to the window goroutine.
type Result struct {
	Kind        OpKind
	WorkspaceID string
	Entries     []client.MemoryEntry
	Search      client.MemorySearchResponse
	Entry       client.MemoryEntry
	History     []client.MemoryHistoryEntry
	Preview     client.MemoryImportPreview
	ImportBytes []byte
	ImportFile  string
	ApplyResult client.MemoryImportResult
	FilePath    string
	Err         error
}

// Client is the Host API surface the memory worker calls.
type Client interface {
	ListMemoryEntries(ctx context.Context, in client.ListMemoryInput) ([]client.MemoryEntry, error)
	SearchMemoryEntries(ctx context.Context, in client.SearchMemoryInput) (client.MemorySearchResponse, error)
	GetMemoryEntry(ctx context.Context, workspaceID, id, label string) (client.MemoryEntry, error)
	GetMemoryEntryHistory(ctx context.Context, workspaceID, entryID string) ([]client.MemoryHistoryEntry, error)
	WriteMemoryEntry(ctx context.Context, in *client.WriteMemoryInput) (client.MemoryEntry, error)
	VerifyMemoryEntry(ctx context.Context, workspaceID, id string, expectedRevision uint64) (client.MemoryEntry, error)
	RestoreMemoryEntry(ctx context.Context, workspaceID, id string, restoreRevision, expectedRevision uint64) (client.MemoryEntry, error)
	ExportMemory(ctx context.Context, workspaceID string) ([]byte, error)
	PreviewMemoryImport(ctx context.Context, workspaceID string, content []byte) (client.MemoryImportPreview, error)
	ApplyMemoryImport(ctx context.Context, in *client.ApplyMemoryImportInput) (client.MemoryImportResult, error)
}

// Worker runs workspace memory Host API calls off the window goroutine.
type Worker struct {
	clientOf func() Client
	core     *reqworker.Worker[Request, Result]
}

// NewWorker builds a workspace memory worker using clientOf and optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	w := &Worker{clientOf: clientOf}
	w.core = reqworker.New(
		reqworker.DefaultBuffer,
		w.handle,
		func(q Request) Result {
			return Result{Kind: q.Kind, WorkspaceID: q.WorkspaceID, Err: errWorkerQueueFull}
		},
		onResult,
	)
	return w
}

// Start runs the worker goroutine until Stop or ctx cancellation.
func (w *Worker) Start(ctx context.Context) { w.core.Start(ctx) }

// Stop stops the worker goroutine and waits for it to exit.
func (w *Worker) Stop() { w.core.Stop() }

// Do queues req for off-thread execution.
func (w *Worker) Do(req *Request) { w.core.Do(*req) }

// Results returns the channel delivering completed workspace memory operations.
func (w *Worker) Results() <-chan Result { return w.core.Results() }

func (w *Worker) handle(ctx context.Context, req Request) Result {
	c := w.clientOf()
	if c == nil {
		return Result{Kind: req.Kind, WorkspaceID: req.WorkspaceID, Err: client.ErrDisconnected}
	}
	return w.execute(ctx, c, &req)
}

func (w *Worker) execute(ctx context.Context, c Client, req *Request) Result {
	res := Result{Kind: req.Kind, WorkspaceID: req.WorkspaceID, FilePath: req.FilePath}
	switch req.Kind {
	case OpListEntries:
		res.Entries, res.Err = c.ListMemoryEntries(ctx, req.List)
	case OpSearchEntries:
		res.Search, res.Err = c.SearchMemoryEntries(ctx, req.Search)
	case OpGetEntry:
		res.Entry, res.Err = c.GetMemoryEntry(ctx, req.WorkspaceID, req.EntryID, req.Label)
	case OpGetEntryHistory:
		res.History, res.Err = c.GetMemoryEntryHistory(ctx, req.WorkspaceID, req.EntryID)
	case OpWriteEntry:
		res.Entry, res.Err = c.WriteMemoryEntry(ctx, &req.Write)
	case OpVerifyEntry:
		res.Entry, res.Err = c.VerifyMemoryEntry(ctx, req.WorkspaceID, req.EntryID, req.Revision)
	case OpRestoreEntry:
		res.Entry, res.Err = c.RestoreMemoryEntry(ctx, req.WorkspaceID, req.EntryID, req.RestoreRevision, req.Revision)
	case OpExportToFile:
		data, err := c.ExportMemory(ctx, req.WorkspaceID)
		if err != nil {
			res.Err = err
			return res
		}
		res.Err = client.WriteMemoryExportFile(req.FilePath, data)
	case OpPreviewImportFile:
		data, err := client.ReadMemoryImportFile(req.FilePath, client.MemoryImportMaxBytes)
		if err != nil {
			res.Err = err
			return res
		}
		res.ImportBytes = data
		res.ImportFile = filepath.Base(req.FilePath)
		res.Preview, res.Err = c.PreviewMemoryImport(ctx, req.WorkspaceID, data)
	case OpPreviewImportBytes:
		res.ImportBytes = req.ImportBytes
		res.ImportFile = req.FilePath
		res.Preview, res.Err = c.PreviewMemoryImport(ctx, req.WorkspaceID, req.ImportBytes)
	case OpApplyImport:
		res.ApplyResult, res.Err = c.ApplyMemoryImport(ctx, &req.Apply)
	}
	return res
}
