package team

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/pkg/client"
)

// Operation selects one Host API call.
type Operation uint8

// Team read and write operations.
const (
	List Operation = iota
	Read
	Page
	Post
	Recover
	Diff
	Catalog
)

// Request is copied into the worker queue.
type Request struct {
	Op                                 Operation
	Team, Task, Thread, Text, Decision string
	Before                             int64
	Epoch                              uint64
	// Ref is an opaque caller token echoed back on the Result so a Post can be
	// matched to the composer that sent it.
	Ref uint64
}

// Result returns the response together with the request's routing metadata.
type Result struct {
	Op         Operation
	Team, Task string
	Before     int64
	Epoch      uint64
	Ref        uint64
	Summaries  []client.TeamSummary
	View       client.TeamView
	Lines      []client.ChannelLine
	Roles      []Role
	Err        error
}

// Client is the team worker's transport port.
type Client interface {
	GetEffectiveConfig(context.Context, string) (client.EffectiveWorkspaceConfig, error)
	ListTeams(context.Context, *client.ListTeamsInput) ([]client.TeamSummary, error)
	GetTeam(context.Context, *client.GetTeamInput) (client.TeamView, error)
	PageChannel(context.Context, *client.PageChannelInput) ([]client.ChannelLine, error)
	PostChannelMessage(context.Context, *client.PostChannelMessageInput) (int64, error)
	RecoverTask(context.Context, *client.RecoverTaskInput) (client.TaskNode, error)
}

// Worker serializes host requests and hands results back without waiting for a frame.
type Worker struct {
	source  func() Client
	wake    func()
	newID   func() string
	queue   chan Request
	done    chan struct{}
	cancel  context.CancelFunc
	mu      sync.Mutex
	results []Result
}

// Start ties the worker's lifetime to the application window.
func Start(ctx context.Context, source func() Client, wake func(), newID func() string) *Worker {
	ctx, cancel := context.WithCancel(ctx)
	w := &Worker{source: source, wake: wake, newID: newID, queue: make(chan Request, 32), done: make(chan struct{}), cancel: cancel}
	go w.run(ctx)
	return w
}

// Stop cancels the active request and waits for the worker.
func (w *Worker) Stop() {
	w.cancel()
	<-w.done
}

// Submit reports whether the bounded queue accepted the request.
func (w *Worker) Submit(req *Request) bool {
	select {
	case w.queue <- *req:
		return true
	default:
		return false
	}
}

// Drain transfers ownership of completed results to the window.
func (w *Worker) Drain() []Result {
	w.mu.Lock()
	defer w.mu.Unlock()
	results := w.results
	w.results = nil
	return results
}

func (w *Worker) run(ctx context.Context) {
	defer close(w.done)
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case req := <-w.queue:
			res := execute(ctx, w.source(), &req, w.newID)
			w.mu.Lock()
			w.results = append(w.results, res)
			w.mu.Unlock()
			w.wake()
		}
	}
}

func execute(ctx context.Context, host Client, req *Request, newID func() string) Result {
	res := Result{Op: req.Op, Team: req.Team, Task: req.Task, Before: req.Before, Epoch: req.Epoch, Ref: req.Ref}
	if host == nil {
		res.Err = client.ErrDisconnected
		return res
	}
	switch req.Op {
	case List:
		res.Summaries, res.Err = host.ListTeams(ctx, nil)
	case Read, Diff:
		res.View, res.Err = host.GetTeam(ctx, &client.GetTeamInput{TeamID: req.Team, DiffTaskID: req.Task})
	case Catalog:
		config, err := host.GetEffectiveConfig(ctx, req.Team)
		res.Roles, res.Err = readRoles(config.Values), err
	case Page:
		res.Lines, res.Err = host.PageChannel(ctx, &client.PageChannelInput{TeamID: req.Team, Before: req.Before, Limit: 100})
	case Post:
		_, res.Err = host.PostChannelMessage(ctx, &client.PostChannelMessageInput{
			CommandID: "ui-team-" + newID(), TeamID: req.Team, ThreadTaskID: req.Thread, ToTaskID: req.Task, Text: req.Text,
		})
	case Recover:
		_, res.Err = host.RecoverTask(ctx, &client.RecoverTaskInput{
			CommandID: "ui-team-" + newID(), TeamID: req.Team, TaskID: req.Task, Decision: req.Decision,
		})
	}
	return res
}
