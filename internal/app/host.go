package app

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
)

// ClientKind says what kind of client owns a lease (host-api §8). It is
// informational only: authorization is the transport's uid check, never the
// client's claim.
type ClientKind string

// The client kinds.
const (
	KindTUI           ClientKind = "tui"
	KindCLI           ClientKind = "cli"
	KindApp           ClientKind = "app"
	KindAppBackground ClientKind = "app-background"
)

func (k ClientKind) valid() bool {
	switch k {
	case KindTUI, KindCLI, KindApp, KindAppBackground:
		return true
	default:
		return false
	}
}

// HostConfig configures the host runtime.
type HostConfig struct {
	// HostVersion is the running binary's version, informational.
	HostVersion string
	// APIVersions are the Host API versions the host serves, ascending, most
	// preferred last.
	APIVersions []string
	// IdleGrace is how long the host waits after the last lease drops before
	// it drains itself; 0 never self-exits (Q-25 A).
	IdleGrace time.Duration
	// DrainTimeout bounds the graceful drain; the deadline cancels the drain
	// context through the injected clock. 0 waits unbounded.
	DrainTimeout time.Duration
}

// HelloParams are the Hello request's domain fields.
type HelloParams struct {
	Kind                 ClientKind
	Name                 string
	ClientVersion        string
	SupportedAPIVersions []string
}

// HelloResult is the Hello reply's domain fields.
type HelloResult struct {
	APIVersion  string
	HostVersion string
}

// ClientCounts reports the live leases per kind.
type ClientCounts struct {
	TUI           uint32
	CLI           uint32
	App           uint32
	AppBackground uint32
	Total         uint32
}

// HostStatusView is the status snapshot of a running host.
type HostStatusView struct {
	HostVersion          string
	SupportedAPIVersions []string
	Incarnation          storage.HostIncarnation
	StartedAt            time.Time
	Clients              ClientCounts
}

// ShutdownCommand is the Shutdown request's domain fields. ID is a
// client-generated command id and must be non-empty (host-api §5).
type ShutdownCommand struct {
	ID    string
	Force bool
}

// HostTransport is the drain's first step: the transport that serves the Host
// API. *hostapi.Server satisfies it; app never imports hostapi.
type HostTransport interface {
	Drain(ctx context.Context, force bool) error
}

// DrainFunc runs the drain: transport, then store, in that order.
type DrainFunc func(ctx context.Context, force bool, reason string) error

// Backend is the host-side service the transport serves. *HostRuntime
// implements it.
type Backend interface {
	Hello(ctx context.Context, p HelloParams) (HelloResult, error)
	LeaseAcquired(ctx context.Context, connID string, kind ClientKind) error
	LeaseReleased(ctx context.Context, connID string)
	Status(ctx context.Context) (HostStatusView, error)
	Shutdown(ctx context.Context, cmd ShutdownCommand) error
}

// HostDeps are the ports StartHost needs.
type HostDeps struct {
	DataDir   string
	Config    HostConfig
	Clock     clock.Clock
	IDs       ids.Generator
	Transport HostTransport
}

// HostRuntime tracks versions, leases and the drain state of a running host.
// Its long-lived context is never stored: StartHost installs the spawn port
// with the base context captured in the closure.
type HostRuntime struct {
	cfg         HostConfig
	started     time.Time
	incarnation storage.HostIncarnation
	clk         clock.Clock

	// goTask spawns a supervised goroutine on the host's base context; it is
	// installed by StartHost because the base context is never stored.
	goTask func(name string, fn func(context.Context) error) *supervise.Task
	// drain is the injected drain: transport, then store, in that order.
	drain DrainFunc

	mu       sync.Mutex
	leases   map[string]ClientKind
	draining bool
	// graceTimer and disarm are the idle-grace timer and its stop channel;
	// both are nil while no grace goroutine is live.
	graceTimer clock.Timer
	disarm     chan struct{}

	drainOnce sync.Once
	drained   chan struct{}
}

var _ Backend = (*HostRuntime)(nil)

func newHostRuntime(cfg HostConfig, inc storage.HostIncarnation, clk clock.Clock) *HostRuntime {
	return &HostRuntime{
		cfg:         cfg,
		started:     inc.AcquiredAt,
		incarnation: inc,
		clk:         clk,
		leases:      map[string]ClientKind{},
		drained:     make(chan struct{}),
	}
}

// Runtime returns the host runtime the transport serves.
func (hp *HostProcess) Runtime() *HostRuntime { return hp.rt }

// DrainDone is closed when the drain has finished, or after Close when no
// drain was started.
func (hp *HostProcess) DrainDone() <-chan struct{} { return hp.rt.drained }

// Stop begins the drain for signal-style teardown: the same entry the
// Shutdown RPC and the idle grace converge on.
func (rt *HostRuntime) Stop(reason string) { rt.beginDrain(false, reason) }

// Hello negotiates the connection's API version. The lease itself is created
// by LeaseAcquired after a successful Hello, so a failed registration leaves
// the connection lease-less and free to retry.
func (rt *HostRuntime) Hello(_ context.Context, p HelloParams) (HelloResult, error) {
	if !p.Kind.valid() {
		return HelloResult{}, errcode.New(errcode.InvalidArgument, "client_kind is missing or unknown")
	}
	parsed := make([]int, 0, len(p.SupportedAPIVersions))
	for _, v := range p.SupportedAPIVersions {
		n, err := parseAPIVersion(v)
		if err != nil {
			return HelloResult{}, err
		}
		parsed = append(parsed, n)
	}
	rt.mu.Lock()
	draining := rt.draining
	rt.mu.Unlock()
	if draining {
		return HelloResult{}, ErrHostStopping
	}
	// The client's list is ascending, most preferred last, so the highest
	// common version is the last entry the host also serves.
	for i := len(parsed) - 1; i >= 0; i-- {
		for _, hostVersion := range rt.cfg.APIVersions {
			if n, err := parseAPIVersion(hostVersion); err == nil && n == parsed[i] {
				return HelloResult{APIVersion: hostVersion, HostVersion: rt.cfg.HostVersion}, nil
			}
		}
	}
	return HelloResult{}, errcode.Wrap(
		errcode.UnsupportedVersion,
		"the host supports "+strings.Join(rt.cfg.APIVersions, ", "),
		ErrNoCommonAPIVersion,
	)
}

// LeaseAcquired registers the connection's lease under its negotiated kind.
func (rt *HostRuntime) LeaseAcquired(_ context.Context, connID string, kind ClientKind) error {
	if connID == "" {
		return errcode.New(errcode.InvalidArgument, "connection id is required")
	}
	if !kind.valid() {
		return errcode.New(errcode.InvalidArgument, "client kind is missing or unknown")
	}
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.draining {
		return ErrHostStopping
	}
	rt.leases[connID] = kind
	rt.disarmGraceLocked()
	return nil
}

// LeaseReleased drops the lease. A lease released while draining never re-arms
// the grace, and the last lease of an idle host arms it exactly once.
func (rt *HostRuntime) LeaseReleased(_ context.Context, connID string) {
	rt.mu.Lock()
	delete(rt.leases, connID)
	if rt.draining || rt.cfg.IdleGrace <= 0 || len(rt.leases) > 0 || rt.graceTimer != nil {
		rt.mu.Unlock()
		return
	}
	// The timer and the stop channel are created under the mutex; the
	// goroutine starts outside it.
	timer := rt.clk.NewTimer(rt.cfg.IdleGrace)
	disarm := make(chan struct{}, 1)
	rt.graceTimer = timer
	rt.disarm = disarm
	goTask := rt.goTask
	rt.mu.Unlock()
	if goTask == nil {
		return
	}
	goTask("app.host-grace", func(ctx context.Context) error {
		select {
		case <-timer.C():
			rt.beginDrain(false, "idle_grace")
		case <-disarm:
		case <-ctx.Done():
		}
		return nil
	})
}

// Status snapshots the host's versions, incarnation and lease counts.
func (rt *HostRuntime) Status(_ context.Context) (HostStatusView, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	counts := ClientCounts{}
	for _, kind := range rt.leases {
		switch kind {
		case KindTUI:
			counts.TUI++
		case KindCLI:
			counts.CLI++
		case KindApp:
			counts.App++
		case KindAppBackground:
			counts.AppBackground++
		}
		counts.Total++
	}
	return HostStatusView{
		HostVersion:          rt.cfg.HostVersion,
		SupportedAPIVersions: slices.Clone(rt.cfg.APIVersions),
		Incarnation:          rt.incarnation,
		StartedAt:            rt.started,
		Clients:              counts,
	}, nil
}

// Shutdown validates the command and begins the drain. Every later call gets
// the same reply from the draining flag; no receipt is kept. An empty
// command_id is invalid even while draining.
func (rt *HostRuntime) Shutdown(_ context.Context, cmd ShutdownCommand) error {
	if cmd.ID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	rt.beginDrain(cmd.Force, "shutdown")
	return nil
}

// beginDrain is the one drain entry: the Shutdown RPC, the idle grace and the
// signal path all converge here. It marks the runtime draining, stops the
// grace, and spawns the drain outside the mutex — the drain closes
// connections whose close hooks re-enter LeaseReleased.
func (rt *HostRuntime) beginDrain(force bool, reason string) {
	rt.mu.Lock()
	if rt.draining {
		rt.mu.Unlock()
		return
	}
	rt.draining = true
	rt.disarmGraceLocked()
	goTask := rt.goTask
	rt.mu.Unlock()
	slog.Info("host drain started", "reason", reason)
	if goTask == nil || rt.drain == nil {
		return
	}
	goTask("app.host-drain", func(ctx context.Context) error {
		defer rt.closeDrained()
		return rt.drain(ctx, force, reason)
	})
}

// disarmGraceLocked stops a pending grace timer, if any; the caller holds the
// mutex and at most one grace goroutine is live.
func (rt *HostRuntime) disarmGraceLocked() {
	if rt.graceTimer == nil {
		return
	}
	rt.graceTimer.Stop()
	close(rt.disarm)
	rt.graceTimer = nil
	rt.disarm = nil
}

func (rt *HostRuntime) closeDrained() {
	rt.drainOnce.Do(func() { close(rt.drained) })
}

// parseAPIVersion parses "v<N>" numerically; anything else is invalid.
func parseAPIVersion(v string) (int, error) {
	const invalid = "an api version must be \"v\" followed by digits"
	if len(v) < 2 || v[0] != 'v' {
		return 0, errcode.New(errcode.InvalidArgument, invalid)
	}
	for _, r := range v[1:] {
		if r < '0' || r > '9' {
			return 0, errcode.New(errcode.InvalidArgument, invalid)
		}
	}
	n, err := strconv.Atoi(v[1:])
	if err != nil || n <= 0 {
		return 0, errcode.New(errcode.InvalidArgument, invalid)
	}
	return n, nil
}

// HostProcess is a running host: the store that owns the data directory, the
// runtime the transport serves, and the supervised drain.
type HostProcess struct {
	deps        HostDeps
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
// drain: transport first, store and lock last.
func StartHost(ctx context.Context, deps HostDeps) (*HostProcess, error) {
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
		return nil, err
	}
	// The base context outlives StartHost; it is captured in the closures
	// below, never stored as a context field.
	base, cancel := context.WithCancel(ctx)
	hp := &HostProcess{deps: deps, store: store, cancel: cancel}
	hp.rt = newHostRuntime(cfg, store.Incarnation(), deps.Clock)
	hp.rt.goTask = func(name string, fn func(context.Context) error) *supervise.Task {
		task := supervise.Go(base, name, fn)
		hp.tasksMu.Lock()
		hp.tasks = append(hp.tasks, task)
		hp.tasksMu.Unlock()
		return task
	}
	hp.rt.drain = hp.runDrain
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
		for {
			hp.tasksMu.Lock()
			tasks := hp.tasks
			hp.tasks = nil
			hp.tasksMu.Unlock()
			if len(tasks) == 0 {
				break
			}
			for _, task := range tasks {
				if err := task.Wait(); err != nil {
					hp.closeErr = errors.Join(hp.closeErr, err)
				}
			}
		}
		if !hp.storeClosed.Load() {
			if err := hp.store.Close(); err != nil {
				hp.closeErr = errors.Join(hp.closeErr, err)
			}
		}
		hp.rt.closeDrained()
	})
	return hp.closeErr
}
