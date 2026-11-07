package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

// HostProcess is a running host: the store that owns the data directory, the
// runtime the transport serves, and the supervised drain.
type HostProcess struct {
	deps        *HostDeps
	store       *storage.Store
	rt          *HostRuntime
	cancel      context.CancelFunc
	tasksMu     sync.Mutex
	tasks       []*supervise.Task
	storeClosed atomic.Bool
	closeOnce   sync.Once
	closeErr    error
}

// StartHost opens the store — the data-directory lock is the liveness
// authority — builds the runtime from the incarnation value and wires the
// drain: transport first, store and lock last. The deps are a pointer because
// the struct is large and shared by the caller's wiring.
func StartHost(ctx context.Context, deps *HostDeps) (*HostProcess, error) {
	if deps.Clock == nil {
		return nil, errcode.New(errcode.InvalidArgument, "clock is nil")
	}
	if deps.IDs == nil {
		return nil, errcode.New(errcode.InvalidArgument, "id generator is nil")
	}
	if deps.Transport == nil {
		return nil, errcode.New(errcode.InvalidArgument, "transport is nil")
	}
	cfg := deps.Config
	if len(cfg.APIVersions) == 0 {
		return nil, errcode.New(errcode.InvalidArgument, "the host serves no api versions")
	}
	cfg.APIVersions = slices.Clone(cfg.APIVersions)
	for _, v := range cfg.APIVersions {
		if _, err := parseAPIVersion(v); err != nil {
			return nil, err
		}
	}
	store, err := storage.OpenStore(ctx, config.Config{DataDir: deps.DataDir}, deps.Clock, deps.IDs)
	if err != nil {
		// OpenStore failures are already typed — the lock failure is public
		// and a second host must keep failing typed — so the pass-through
		// keeps the code and message a client already knows.
		var coded *errcode.Error
		if errors.As(err, &coded) {
			return nil, errcode.Wrap(coded.Code(), coded.Message(), err)
		}
		return nil, errcode.Wrap(errcode.Internal, "open the host store", err)
	}
	// The base context outlives StartHost; it is captured in the closures
	// below, never stored as a context field.
	base, cancel := context.WithCancel(ctx)
	hp := &HostProcess{deps: deps, store: store, cancel: cancel}
	hp.rt = newHostRuntime(cfg, store.Incarnation(), deps.Clock)
	// The host topic is the one stage-A topic this item registers: lease
	// counts and the draining transition. Later items register theirs.
	if err := hp.rt.hub.RegisterEphemeral(TopicHost, hp.rt.hostTopicSnapshot, defaultEphemeralBufferCap); err != nil {
		cancel()
		_ = store.Close()
		return nil, err
	}
	hp.rt.goTask = func(name string, fn func(context.Context) error) *supervise.Task {
		task := supervise.Go(base, name, fn)
		hp.tasksMu.Lock()
		hp.tasks = append(hp.tasks, task)
		hp.tasksMu.Unlock()
		return task
	}
	hp.rt.drain = hp.runDrain
	hp.rt.startGraceLoop()
	return hp, nil
}

// runDrain is the injected drain: the transport step, then the store and lock
// last, bounded by the configured deadline through the injected clock.
func (hp *HostProcess) runDrain(ctx context.Context, force bool, reason string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if !force && hp.rt.cfg.DrainTimeout > 0 {
		timer := hp.rt.clk.NewTimer(hp.rt.cfg.DrainTimeout)
		defer timer.Stop()
		hp.rt.goTask("app.host-drain-deadline", func(context.Context) error {
			select {
			case <-timer.C():
				cancel()
			case <-ctx.Done():
			}
			return nil
		})
	}
	derr := hp.deps.Transport.Drain(ctx, force)
	serr := hp.store.Close()
	hp.storeClosed.Store(true)
	if derr == nil && serr == nil {
		return nil
	}
	return errcode.Wrap(errcode.Internal, "the host drain did not finish cleanly", errors.Join(derr, serr))
}

// Stop begins the drain; the reason names the trigger in the log.
func (hp *HostProcess) Stop(reason string) { hp.rt.Stop(reason) }

// Close tears the host down: it cancels the base context, waits for every
// supervised task, closes the store if the drain has not already, and is
// idempotent. A drain still in progress is waited for.
func (hp *HostProcess) Close() error {
	hp.closeOnce.Do(func() {
		hp.cancel()
		hp.closeErr = errors.Join(hp.closeErr, hp.waitTasks())
		if !hp.storeClosed.Load() {
			if err := hp.store.Close(); err != nil {
				hp.closeErr = errors.Join(hp.closeErr, err)
			}
		}
		hp.rt.closeDrained()
	})
	return hp.closeErr
}

// waitTasks waits for every supervised task, re-checking for tasks spawned
// while waiting (a grace goroutine may start the drain as Close cancels).
func (hp *HostProcess) waitTasks() error {
	var waitErr error
	for {
		hp.tasksMu.Lock()
		tasks := hp.tasks
		hp.tasks = nil
		hp.tasksMu.Unlock()
		if len(tasks) == 0 {
			return waitErr
		}
		for _, task := range tasks {
			if err := task.Wait(); err != nil {
				waitErr = errors.Join(waitErr, err)
			}
		}
	}
}
