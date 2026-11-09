package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/interaction"
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
	if err := validateHostDeps(deps); err != nil {
		return nil, err
	}
	cfg := deps.Config
	cfg.APIVersions = slices.Clone(cfg.APIVersions)
	store, err := openHostStore(ctx, deps)
	if err != nil {
		return nil, err
	}
	// The base context outlives StartHost; it is captured in the closures
	// below, never stored as a context field.
	base, cancel := context.WithCancel(ctx)
	hp := &HostProcess{deps: deps, store: store, cancel: cancel}
	hp.rt = newHostRuntime(cfg, store.Incarnation(), deps.Clock)
	if err := initHostTopics(hp, store, deps); err != nil {
		cancel()
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

func validateHostDeps(deps *HostDeps) error {
	if deps.Clock == nil {
		return errcode.New(errcode.InvalidArgument, "clock is nil")
	}
	if deps.IDs == nil {
		return errcode.New(errcode.InvalidArgument, "id generator is nil")
	}
	if deps.Transport == nil {
		return errcode.New(errcode.InvalidArgument, "transport is nil")
	}
	if len(deps.Config.APIVersions) == 0 {
		return errcode.New(errcode.InvalidArgument, "the host serves no api versions")
	}
	for _, v := range deps.Config.APIVersions {
		if _, err := parseAPIVersion(v); err != nil {
			return err
		}
	}
	return nil
}

func openHostStore(ctx context.Context, deps *HostDeps) (*storage.Store, error) {
	store, err := storage.OpenStore(ctx, config.Config{DataDir: deps.DataDir}, deps.Clock, deps.IDs)
	if err != nil {
		var coded *errcode.Error
		if errors.As(err, &coded) {
			return nil, errcode.Wrap(coded.Code(), coded.Message(), err)
		}
		return nil, errcode.Wrap(errcode.Internal, "open the host store", err)
	}
	return store, nil
}

func initHostTopics(hp *HostProcess, store *storage.Store, deps *HostDeps) error {
	if terr := hp.rt.hub.RegisterEphemeral(TopicHost, hp.rt.hostTopicSnapshot, defaultEphemeralBufferCap); terr != nil {
		if serr := store.Close(); serr != nil {
			return errcode.Wrap(errcode.Internal, "register the host topic", errors.Join(terr, serr))
		}
		return terr
	}
	interactions, ierr := initInteractions(deps, hp.rt.hub)
	if ierr != nil {
		if serr := store.Close(); serr != nil {
			return errcode.Wrap(errcode.Internal, "create interaction service", errors.Join(ierr, serr))
		}
		return ierr
	}
	hp.rt.interactions = interactions
	return nil
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

func initInteractions(deps *HostDeps, hub *EventHub) (*InteractionService, error) {
	owner, err := interaction.NewOwner(interaction.Config{
		Clock:     deps.Clock,
		IDs:       deps.IDs,
		Responder: nil,
		Headless:  false,
		// TODO(F9.6): wire the host vault lock state to the interaction owner once vault is added to HostDeps.
		VaultLocked: func() bool { return false },
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "create interaction owner", err)
	}
	return NewInteractionService(owner, hub, deps.Clock)
}
