package settings

import (
	"context"
	"errors"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

const workerBuffer = 16

var errWorkerQueueFull = errors.New("the settings request queue is full")

// OpKind identifies one off-thread settings Host API operation.
type OpKind uint8

// The settings worker operations.
const (
	OpLoadPrefs OpKind = iota
	OpSavePrefs
	OpLoadWorkspace
	OpSaveWorkspace
)

// Request carries one settings operation for the worker goroutine.
type Request struct {
	Kind          OpKind
	WorkspaceID   string
	RawTOML       []byte
	ExpectedHash  string
	CommandID     string
	PrefsRevision uint64
	Prefs         Preferences
}

// Result delivers one completed settings operation to the window goroutine.
type Result struct {
	Kind          OpKind
	WorkspaceID   string
	Workspace     *client.Workspace
	Effective     *client.EffectiveWorkspaceConfig
	CASConflict   bool
	PrefsRevision uint64
	PrefsRaw      []byte
	Code          string
	Err           error
}

// Client is the Host API surface called by the settings worker.
type Client interface {
	Workspace(ctx context.Context, id string) (client.Workspace, error)
	GetEffectiveConfig(ctx context.Context, workspaceID string) (client.EffectiveWorkspaceConfig, error)
	UpdateWorkspaceConfig(ctx context.Context, workspaceID string, rawTOML []byte, expectedHash string) (client.Workspace, error)
	GetAppStatePreferences(ctx context.Context) (client.AppStatePreferences, error)
	PutAppStatePreferences(ctx context.Context, in *client.PutAppStatePreferencesInput) (uint64, error)
}

// Worker runs settings Host API calls off the window goroutine.
type Worker struct {
	clientOf func() Client
	onResult func()
	reqs     chan Request
	results  chan Result
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewWorker builds a settings worker using clientOf and an optional onResult notifier.
func NewWorker(clientOf func() Client, onResult func()) *Worker {
	return &Worker{
		clientOf: clientOf,
		onResult: onResult,
		reqs:     make(chan Request, workerBuffer),
		results:  make(chan Result, workerBuffer),
		done:     make(chan struct{}),
	}
}

// Start launches the worker goroutine until Stop or ctx cancellation.
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
		w.deliver(&Result{Kind: req.Kind, WorkspaceID: req.WorkspaceID, Err: errWorkerQueueFull})
	}
}

// Results returns the channel delivering completed settings operations.
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
		w.deliver(&Result{Kind: req.Kind, WorkspaceID: req.WorkspaceID, Err: client.ErrDisconnected})
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
	switch req.Kind {
	case OpLoadPrefs:
		return executeLoadPrefs(ctx, c)
	case OpSavePrefs:
		return executeSavePrefs(ctx, c, req)
	case OpLoadWorkspace:
		return executeLoadWorkspace(ctx, c, req.WorkspaceID)
	case OpSaveWorkspace:
		return executeSaveWorkspace(ctx, c, req)
	default:
		return Result{Kind: req.Kind, WorkspaceID: req.WorkspaceID}
	}
}

func executeLoadPrefs(ctx context.Context, c Client) Result {
	res := Result{Kind: OpLoadPrefs}
	prefs, err := c.GetAppStatePreferences(ctx)
	if err != nil {
		res.Err = err
		res.Code, _ = client.AppStateCodeOf(err)
		return res
	}
	res.PrefsRevision = prefs.Revision
	res.PrefsRaw = prefs.Preferences
	return res
}

func executeSavePrefs(ctx context.Context, c Client, req *Request) Result {
	res := Result{Kind: OpSavePrefs}
	payload, err := EncodePreferences(req.Prefs)
	if err != nil {
		res.Err = err
		return res
	}
	rev, err := c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{
		CommandID:        req.CommandID,
		ExpectedRevision: req.PrefsRevision,
		Preferences:      payload,
	})
	if err != nil && client.AppStateConflictCode != "" {
		if curRev, ok := client.AppStateCurrentRevision(err); ok {
			rev, err = c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{
				CommandID:        req.CommandID + "-retry",
				ExpectedRevision: curRev,
				Preferences:      payload,
			})
		}
	}
	if err != nil {
		res.Err = err
		res.Code, _ = client.AppStateCodeOf(err)
		return res
	}
	res.PrefsRevision = rev
	res.PrefsRaw = payload
	return res
}

func executeLoadWorkspace(ctx context.Context, c Client, workspaceID string) Result {
	res := Result{Kind: OpLoadWorkspace, WorkspaceID: workspaceID}
	ws, err := c.Workspace(ctx, workspaceID)
	if err != nil {
		res.Err = err
		res.Code, _ = client.WorkspaceCodeOf(err)
		return res
	}
	eff, err := c.GetEffectiveConfig(ctx, workspaceID)
	if err != nil {
		res.Err = err
		res.Code, _ = client.WorkspaceCodeOf(err)
		return res
	}
	res.Workspace = &ws
	res.Effective = &eff
	return res
}

func executeSaveWorkspace(ctx context.Context, c Client, req *Request) Result {
	res := Result{Kind: OpSaveWorkspace, WorkspaceID: req.WorkspaceID}
	ws, err := c.UpdateWorkspaceConfig(ctx, req.WorkspaceID, req.RawTOML, req.ExpectedHash)
	if err == nil {
		eff, effErr := c.GetEffectiveConfig(ctx, req.WorkspaceID)
		if effErr != nil {
			res.Err = effErr
			res.Code, _ = client.WorkspaceCodeOf(effErr)
			return res
		}
		res.Workspace = &ws
		res.Effective = &eff
		return res
	}
	res.Err = err
	res.Code, _ = client.WorkspaceCodeOf(err)
	if errors.Is(err, client.ErrWorkspaceConfigChanged) {
		res.CASConflict = true
		reload := executeLoadWorkspace(ctx, c, req.WorkspaceID)
		res.Workspace = reload.Workspace
		res.Effective = reload.Effective
	}
	return res
}
