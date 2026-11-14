package app

import (
	"context"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/environments"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
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
	Subscribe(ctx context.Context, p SubscribeParams) (*Subscription, error)
	ListPendingInteractions(ctx context.Context, principal string) ([]PendingInteractionView, EventSeq, error)
	AnswerInteraction(ctx context.Context, cmd *AnswerInteractionCommand) (AnswerInteractionResult, error)
}

// StreamEnder ends every open event stream. The transport asserts it at the
// start of a graceful drain so each stream delivers its end message and
// returns before the connections close.
type StreamEnder interface {
	EndStreams()
}

// HostRuntime tracks versions, leases and the drain state of a running host.
// Its long-lived context is never stored: StartHost installs the spawn port
// with the base context captured in the closure.
type HostRuntime struct {
	cfg          HostConfig
	started      time.Time
	incarnation  storage.HostIncarnation
	clk          clock.Clock
	hub          *EventHub
	interactions *InteractionService
	workspaces   *WorkspaceService
	vendors      *VendorService
	transcripts  *Transcripts
	sessionTools *SessionToolService
	tags         *environments.Registry

	// goTask spawns a supervised goroutine on the host's base context; it is
	// installed by StartHost because the base context is never stored.
	goTask func(name string, fn func(context.Context) error) *supervise.Task
	// drain is the injected drain: transport, then store, in that order.
	drain DrainFunc

	mu       sync.Mutex
	leases   map[string]ClientKind
	draining bool
	// graceTimer and disarm are the idle-grace timer and its stop channel;
	// both are nil while no grace arming is live. graceWake wakes the single
	// long-lived grace loop that StartHost starts; the loop reads the armed
	// state under the mutex.
	graceTimer clock.Timer
	disarm     chan struct{}
	graceWake  chan struct{}

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
		hub:         NewEventHub(inc.IncarnationID),
		leases:      map[string]ClientKind{},
		drained:     make(chan struct{}),
		graceWake:   make(chan struct{}, 1),
	}
}

// Subscribe serves one event stream; the runtime delegates to its hub. A bare
// runtime without a hub (some tests) denies every topic.
func (rt *HostRuntime) Subscribe(ctx context.Context, p SubscribeParams) (*Subscription, error) {
	if rt.hub == nil {
		return nil, ErrSubscriptionDenied
	}
	return rt.hub.Subscribe(ctx, p)
}

// EndStreams ends every open event stream; the transport calls it at the start
// of a graceful drain.
func (rt *HostRuntime) EndStreams() {
	if rt.hub != nil {
		rt.hub.EndStreams()
	}
}

// startGraceLoop starts the one long-lived grace goroutine. Arming never
// spawns a task: the loop returns to its receive after every fire or disarm,
// so a host that cycles leases holds exactly one grace task for its life.
func (rt *HostRuntime) startGraceLoop() {
	if rt.goTask == nil {
		return
	}
	rt.goTask("app.host-grace", rt.graceLoop)
}

// graceLoop waits for an armed idle grace, its disarm or the host's shutdown.
// It reads the armed state under the mutex after every wake: LeaseReleased
// never blocks on the loop, so a dropped or late wake must not lose an
// arming — the state lives only under the mutex, and the wake is just a
// nudge.
func (rt *HostRuntime) graceLoop(ctx context.Context) error {
	// Cancellation is the loop's normal stop: Close cancels the host's base
	// context, so ending by ctx.Done is a clean stop, not a failure.
	for {
		rt.mu.Lock()
		timer, disarm := rt.graceTimer, rt.disarm
		rt.mu.Unlock()
		if timer == nil {
			select {
			case <-ctx.Done():
				rt.mu.Lock()
				rt.disarmGraceLocked()
				rt.mu.Unlock()
				return nil
			case <-rt.graceWake:
			}
			continue
		}
		select {
		case <-ctx.Done():
			rt.mu.Lock()
			rt.disarmGraceLocked()
			rt.mu.Unlock()
			return nil
		case <-disarm:
		case <-timer.C():
			rt.fireGrace(timer)
		case <-rt.graceWake:
		}
	}
}

// Runtime returns the host runtime the transport serves.
func (hp *HostProcess) Runtime() *HostRuntime { return hp.rt }

// DrainDone is closed when the drain has finished, or after Close when no
// drain was started.
func (hp *HostProcess) DrainDone() <-chan struct{} { return hp.rt.drained }

// StoreClosed reports whether the drain has released the store and the data
// directory lock. The store closes only after the transport drain returned.
func (hp *HostProcess) StoreClosed() bool { return hp.storeClosed.Load() }

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
	if rt.draining {
		rt.mu.Unlock()
		return ErrHostStopping
	}
	rt.leases[connID] = kind
	rt.disarmGraceLocked()
	rt.mu.Unlock()
	rt.publishHostClients()
	return nil
}

// LeaseReleased drops the lease. A lease released while draining never re-arms
// the grace, and the last lease of an idle host arms it exactly once.
func (rt *HostRuntime) LeaseReleased(_ context.Context, connID string) {
	rt.mu.Lock()
	delete(rt.leases, connID)
	if rt.draining || rt.cfg.IdleGrace <= 0 || len(rt.leases) > 0 || rt.graceTimer != nil {
		rt.mu.Unlock()
		rt.publishHostClients()
		return
	}
	// The timer and the stop channel are created under the mutex; the wake is
	// a non-blocking nudge after unlocking. A dropped wake can only happen
	// while the loop is already waiting on this arming, and every pass
	// re-reads the armed state under the mutex.
	timer := rt.clk.NewTimer(rt.cfg.IdleGrace)
	disarm := make(chan struct{}, 1)
	rt.graceTimer = timer
	rt.disarm = disarm
	rt.mu.Unlock()
	rt.publishHostClients()
	if rt.graceWake == nil {
		return
	}
	select {
	case rt.graceWake <- struct{}{}:
	default:
	}
}

// ArmInitialIdleGrace starts the idle timer when an app-started host has no lease yet.
func (rt *HostRuntime) ArmInitialIdleGrace(ctx context.Context) {
	rt.LeaseReleased(ctx, "")
}

// fireGrace is the grace timer's fire path: the staleness check — this timer
// is still the armed one, no lease is held, the runtime is not draining — and
// the draining transition share one critical section, so a lease that wins
// the race against the fire makes the fire stale instead of riding a draining
// host.
func (rt *HostRuntime) fireGrace(timer clock.Timer) bool {
	rt.mu.Lock()
	if timer != rt.graceTimer || rt.draining || len(rt.leases) > 0 {
		rt.mu.Unlock()
		return false
	}
	spawn := rt.beginDrainLocked(false, "idle_grace")
	rt.mu.Unlock()
	if spawn != nil {
		spawn()
		return true
	}
	return false
}

// Status snapshots the host's versions, incarnation and lease counts.
func (rt *HostRuntime) Status(_ context.Context) (HostStatusView, error) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return HostStatusView{
		HostVersion:          rt.cfg.HostVersion,
		SupportedAPIVersions: slices.Clone(rt.cfg.APIVersions),
		Incarnation:          rt.incarnation,
		StartedAt:            rt.started,
		Clients:              rt.countsLocked(),
	}, nil
}

// countsLocked counts the live leases per kind; the caller holds the mutex.
func (rt *HostRuntime) countsLocked() ClientCounts {
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
	return counts
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
// signal path all converge here. The draining transition happens under the
// mutex and the drain goroutine starts after unlocking — the drain closes
// connections whose close hooks re-enter LeaseReleased.
func (rt *HostRuntime) beginDrain(force bool, reason string) {
	rt.mu.Lock()
	spawn := rt.beginDrainLocked(force, reason)
	rt.mu.Unlock()
	if spawn != nil {
		spawn()
	}
}

// beginDrainLocked marks the runtime draining and stops the grace while the
// mutex is held, then returns the goroutine start for the caller to run after
// unlocking. It returns nil when a drain is already under way or when nothing
// spawns (bare runtimes in tests). fireGrace shares this critical section:
// the staleness check and this transition are one decision, so a lease
// acquired in between wins the host and the fire becomes stale instead.
func (rt *HostRuntime) beginDrainLocked(force bool, reason string) func() {
	if rt.draining {
		return nil
	}
	rt.draining = true
	rt.disarmGraceLocked()
	if rt.goTask == nil || rt.drain == nil {
		return nil
	}
	return func() {
		slog.Info("host drain started", "reason", reason)
		rt.publishHostDraining(reason)
		rt.goTask("app.host-drain", func(ctx context.Context) error {
			defer rt.closeDrained()
			return rt.drain(ctx, force, reason)
		})
	}
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
