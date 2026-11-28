package workspace

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// The static causes behind a dropped request, an unknown kind and a missing
// compare-and-swap hash; they are wrapped, never constructed, at the call site.
var (
	errMutatorQueueFull  = errors.New("the mutation queue is full")
	errUnknownMutation   = errors.New("unknown mutation kind")
	errEmptyExpectedHash = errors.New("expected hash is required")
)

// MutKind names what one mutation request does.
type MutKind uint8

// The mutation kinds.
const (
	MutCreate MutKind = iota
	MutAddRoot
	MutRemoveRoot
	MutUpdateRoot
	MutDelete
	MutConfirmPolicy
	MutRejectPolicy
)

// Mutation is one queued workspace change. WorkspaceID names the workspace
// every change but a create targets; Name/NewName/Path/Access carry a root
// change's values; ExpectedHash is the compare-and-swap hash the detail read
// delivered; ConfirmName repeats the workspace's name for a delete.
type Mutation struct {
	Kind                       MutKind
	WorkspaceID, Name, NewName string
	Path, Access, ExpectedHash string
	ConfirmName, CreateName    string
}

// MutResult is one completed mutation, delivered to the window goroutine.
// Err is the host's typed answer, whose workspace code client.WorkspaceCodeOf
// reads. On a successful create, NewID and NewHash are the new workspace's
// identity and compare-and-swap hash; on a successful add, NewHash is the
// hash after the add — the source-folder chain (FUG-115) posts each next
// step against the hash the step before returned.
type MutResult struct {
	Mutation Mutation
	Err      error
	NewID    string
	NewHash  string
}

// mutBuffer bounds the mutation queue. Mutations are user-driven and one
// dialog submits at a time; a request dropped because the queue is full is a
// failure the dialog reports rather than a silent loss.
const mutBuffer = 8

// MutatorClient is the write side the mutator posts through: the pkg/client
// workspace mutations. A create or add answers the workspace as the host now
// holds it, so a chain of steps CASes each one on the hash the step before
// returned (FUG-115). The interface exists for the tests' recording fake;
// production hands over the manager's *client.Client.
type MutatorClient interface {
	CreateWorkspace(ctx context.Context, name string) (client.Workspace, error)
	AddRoot(ctx context.Context, workspaceID, name, path, access, expectedHash string) (client.Workspace, error)
	RemoveRoot(ctx context.Context, workspaceID, name, expectedHash string) error
	UpdateRoot(ctx context.Context, workspaceID, name, newName, path, access, expectedHash string) error
	DeleteWorkspace(ctx context.Context, workspaceID, expectedHash, confirmName string) error
	ConfirmWorkspaceConfig(ctx context.Context, workspaceID, expectedHash string) error
	RejectWorkspaceConfig(ctx context.Context, workspaceID, expectedHash string) error
}

// Mutator performs the workspace mutations off the window goroutine, in the
// shape of the session Fetcher: one goroutine takes requests, runs the
// blocking pkg/client calls against the manager's current connection, and
// hands the results back. Requests while the host is unreachable are dropped;
// the window re-arms them when their dialog resubmits.
type Mutator struct {
	clientOf func() MutatorClient
	reqs     chan Mutation
	results  chan MutResult
	cancel   context.CancelFunc
	done     chan struct{}
	stopOnce sync.Once
}

// NewMutator builds a mutator that posts through clientOf, which returns the
// manager's current connection or nil while disconnected.
func NewMutator(clientOf func() MutatorClient) *Mutator {
	return &Mutator{
		clientOf: clientOf,
		reqs:     make(chan Mutation, mutBuffer),
		results:  make(chan MutResult, mutBuffer),
		done:     make(chan struct{}),
	}
}

// Start runs the mutator's goroutine. It ends with Stop or the context.
func (m *Mutator) Start(ctx context.Context) {
	runCtx, cancel := context.WithCancel(ctx)
	m.cancel = cancel
	go m.run(runCtx)
}

// Stop ends the mutator's goroutine and waits for it. It is idempotent.
func (m *Mutator) Stop() {
	m.stopOnce.Do(func() {
		if m.cancel != nil {
			m.cancel()
		}
	})
	<-m.done
}

// Do queues one workspace mutation. A request the full queue drops is
// reported as a failed result, never silently lost.
func (m *Mutator) Do(mut *Mutation) {
	select {
	case m.reqs <- *mut:
	default:
		m.results <- MutResult{Mutation: *mut, Err: errMutatorQueueFull}
	}
}

// Results receives the completed mutations. The window goroutine drains it
// every frame.
func (m *Mutator) Results() <-chan MutResult { return m.results }

// run serves requests until the context ends.
func (m *Mutator) run(ctx context.Context) {
	defer close(m.done)
	for {
		select {
		case <-ctx.Done():
			return
		case mut := <-m.reqs:
			c := m.clientOf()
			if c == nil {
				continue
			}
			m.deliver(ctx, c, &mut)
		}
	}
}

// deliver posts one mutation and hands its result to the window. A post the
// context killed is a dropped request, not a result: the host never applied
// an RPC the caller cancelled, so the dialog resubmits.
func (m *Mutator) deliver(ctx context.Context, c MutatorClient, mut *Mutation) {
	res := mut.post(ctx, c)
	if ctx.Err() != nil {
		return
	}
	res.Mutation = *mut
	select {
	case m.results <- res:
	case <-ctx.Done():
	}
}

// post runs the mutation's host call. Every mutation except Create requires a
// non-empty compare-and-swap hash from the workspace detail. The client's
// typed answer is wrapped once here, so the dialog shows one message no
// matter which kind failed, and a create or add that answered carries the
// workspace's new identity and hash for the next chained step.
func (mut *Mutation) post(ctx context.Context, c MutatorClient) MutResult {
	if mut.Kind != MutCreate && strings.TrimSpace(mut.ExpectedHash) == "" {
		return MutResult{Err: fmt.Errorf("the workspace change failed: %w", errEmptyExpectedHash)}
	}
	var res MutResult
	var err error
	switch mut.Kind {
	case MutCreate:
		var ws client.Workspace
		ws, err = c.CreateWorkspace(ctx, mut.CreateName)
		res.NewID, res.NewHash = ws.ID, ws.ConfigHash
	case MutAddRoot:
		var ws client.Workspace
		ws, err = c.AddRoot(ctx, mut.WorkspaceID, mut.Name, mut.Path, mut.Access, mut.ExpectedHash)
		res.NewHash = ws.ConfigHash
	case MutRemoveRoot:
		err = c.RemoveRoot(ctx, mut.WorkspaceID, mut.Name, mut.ExpectedHash)
	case MutUpdateRoot:
		err = c.UpdateRoot(ctx, mut.WorkspaceID, mut.Name, mut.NewName, mut.Path, mut.Access, mut.ExpectedHash)
	case MutDelete:
		err = c.DeleteWorkspace(ctx, mut.WorkspaceID, mut.ExpectedHash, mut.ConfirmName)
	case MutConfirmPolicy:
		err = c.ConfirmWorkspaceConfig(ctx, mut.WorkspaceID, mut.ExpectedHash)
	case MutRejectPolicy:
		err = c.RejectWorkspaceConfig(ctx, mut.WorkspaceID, mut.ExpectedHash)
	default:
		err = errUnknownMutation
	}
	if err != nil {
		res.Err = fmt.Errorf("the workspace change failed: %w", err)
		return res
	}
	return res
}
