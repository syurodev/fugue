package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

var (
	// ErrVendorActorFailed reports a safe, public actor failure.
	ErrVendorActorFailed = errcode.New(errcode.Internal, "vendor session actor failed")
	// ErrVendorSessionCapacity reports exhaustion of the pre-scheduler cap.
	ErrVendorSessionCapacity = errcode.New(errcode.FailedPrecondition, "vendor session capacity reached")
)

// maxVendorSessionsPerHost is the vendor-runtime §13 concurrent-session cap:
// a scheduler-owned value (orchestration §7) that has no scheduler yet, so it
// is the fixed pre-scheduler default of two active processes (Q-55 A). A start
// beyond the cap fails typed with ErrVendorSessionCapacity (vendor.capacity).
const maxVendorSessionsPerHost = 2

// VendorSessionDeps contains the runtime dependencies for vendor sessions.
type VendorSessionDeps struct {
	Store       vendorSessionStore
	Transcripts *Transcripts
	Adapter     vendor.Adapter
	Vault       vendorVaultObserver
	Clock       clock.Clock
	IDs         ids.Generator
	Logger      *slog.Logger
	// OnSessionEnded runs once when a session's actor stops, however it
	// stopped; the host revokes the session token there.
	OnSessionEnded func(sessionID string)
	// TestDelaySpawnRegistration runs inside OnSpawned before owner registration.
	TestDelaySpawnRegistration func(context.Context)
	// TestOnWaitInflightSpawn runs when OwnsPeer begins waiting on in-flight spawns.
	TestOnWaitInflightSpawn func()
	// TestAfterPeerSnapshot runs between snapshotting owners and checking live actors.
	TestAfterPeerSnapshot func()
}

type vendorVaultObserver interface {
	Snapshot() vault.Snapshot
	Observe() (vault.Snapshot, *vault.Subscription)
}

type vendorSessionStore interface {
	CreateSession(context.Context, *vendorstore.Session) error
	Append(context.Context, string, int64, *vendorstore.Event) (int64, error)
	ListSessions(context.Context) ([]vendorstore.Session, error)
	Snapshot(context.Context, string) (vendorstore.Session, error)
}

// VendorSessions owns one supervised actor for every live vendor session.
// It is the in-process service boundary used by the later Host API layer.
type VendorSessions struct {
	deps              VendorSessionDeps
	spawn             func(string, func(context.Context) error) *supervise.Task
	mu                sync.RWMutex
	actors            map[string]*vendorSessionActor
	pendingOwners     map[*pendingOwner]struct{}
	retainedOwners    map[*retainedOwner]struct{}
	closed            bool
	startReservations int
	inflightSpawns    int
	spawnSettled      chan struct{}
}

type pendingOwner struct {
	owner vendor.PeerOwner
}

type retainedOwner struct {
	owner vendor.PeerOwner
}

// NewVendorSessions creates the in-process manager and binds actor lifetime to ctx.
func NewVendorSessions(ctx context.Context, deps *VendorSessionDeps) (*VendorSessions, error) {
	if ctx == nil || deps == nil || deps.Store == nil || deps.Transcripts == nil || deps.Adapter == nil || deps.Vault == nil || deps.Clock == nil || deps.IDs == nil {
		return nil, errcode.New(errcode.InvalidArgument, "vendor session dependencies are required")
	}
	copyDeps := *deps
	if copyDeps.Logger == nil {
		copyDeps.Logger = slog.Default()
	}
	return &VendorSessions{
		deps: copyDeps,
		spawn: func(name string, fn func(context.Context) error) *supervise.Task {
			return supervise.Go(ctx, name, fn)
		},
		actors:         make(map[string]*vendorSessionActor),
		pendingOwners:  make(map[*pendingOwner]struct{}),
		retainedOwners: make(map[*retainedOwner]struct{}),
	}, nil
}

// Start creates a Claude process and hands its observations to a single actor.
// The supplied session ID is also the adapter StartSpec session ID.
func (m *VendorSessions) Start(ctx context.Context, session *vendorstore.Session, spec *vendor.StartSpec) error {
	if session == nil || spec == nil || session.ID == "" || spec.SessionID != session.ID {
		return errcode.New(errcode.InvalidArgument, "session identity is required")
	}
	if m.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.FailedPrecondition, "vault must be unlocked to start a vendor session")
	}
	release, capacityErr := m.reserveSlot(ctx, session.ID)
	if capacityErr != nil {
		return capacityErr
	}
	defer release()
	started, observations, pending, err := m.startAdapter(ctx, spec)
	if err != nil {
		return err
	}
	setProcessIdentity(session, started)
	session.State = vendorstore.StateStarting
	session.ResumeHandle = spec.ResumeSessionID
	actor := newVendorSessionActor(m, session, started, observations)
	if err := m.installActor(session.ID, actor, pending); err != nil {
		m.closeAndSettlePending(ctx, started, pending)
		return err
	}
	if err := actor.call(ctx, actorCommand{kind: commandCreate}); err != nil {
		return err
	}
	return actor.call(ctx, actorCommand{kind: commandReady})
}

// Send starts a Fugue-origin vendor turn.
func (m *VendorSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return fmt.Errorf("validate vendor input: %w", err)
	}
	return m.call(ctx, sessionID, actorCommand{kind: commandSend, input: input})
}

// Steer adds input to the active vendor turn.
func (m *VendorSessions) Steer(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return fmt.Errorf("validate vendor input: %w", err)
	}
	return m.call(ctx, sessionID, actorCommand{kind: commandSteer, input: input})
}

// Interrupt asks the active vendor turn to stop.
func (m *VendorSessions) Interrupt(ctx context.Context, sessionID string) error {
	return m.call(ctx, sessionID, actorCommand{kind: commandInterrupt})
}

// Close stops a vendor session and persists its stop proof.
func (m *VendorSessions) Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error {
	actor := m.actor(sessionID)
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	if err := actor.requestClose(reason); err != nil {
		return err
	}
	select {
	case <-actor.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for vendor session close: %w", ctx.Err())
	}
}

// Resume attaches a fresh adapter process to a detached session. Existing
// turns are never replayed; the caller supplies a new resume-enabled spec.
// Resume starts a new process for a detached session using its stored handle.
func (m *VendorSessions) Resume(ctx context.Context, sessionID string, spec *vendor.StartSpec) error {
	if spec == nil || spec.SessionID != sessionID {
		return errcode.New(errcode.InvalidArgument, "resume session identity is required")
	}
	if m.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.FailedPrecondition, "vault must be unlocked to resume a vendor session")
	}
	release, capacityErr := m.reserveSlot(ctx, sessionID)
	if capacityErr != nil {
		return capacityErr
	}
	defer release()
	info, err := m.deps.Store.Snapshot(ctx, sessionID)
	if err != nil {
		return safeActorError(err)
	}
	if info.State != vendorstore.StateDetached || info.ResumeHandle == "" {
		return errcode.New(errcode.FailedPrecondition, "vendor session is not resumable")
	}
	resumeSpec := *spec
	resumeSpec.ResumeSessionID = info.ResumeHandle
	started, observations, pending, err := m.startAdapter(ctx, &resumeSpec)
	if err != nil {
		return err
	}
	setProcessIdentity(&info, started)
	actor := newVendorSessionActor(m, &info, started, observations)
	if err := m.installActor(sessionID, actor, pending); err != nil {
		m.closeAndSettlePending(ctx, started, pending)
		return err
	}
	if err := actor.call(ctx, actorCommand{kind: commandResume}); err != nil {
		return err
	}
	return actor.call(ctx, actorCommand{kind: commandReady})
}

func (m *VendorSessions) reserveSlot(ctx context.Context, sessionID string) (func(), error) {
	m.mu.Lock()
	if m.closed || m.actors[sessionID] != nil {
		m.mu.Unlock()
		return nil, errcode.New(errcode.FailedPrecondition, "vendor session is already active or manager is closed")
	}
	m.startReservations++
	m.mu.Unlock()
	release := func() {
		m.mu.Lock()
		m.startReservations--
		m.mu.Unlock()
	}
	sessions, err := m.deps.Store.ListSessions(ctx)
	if err != nil {
		release()
		return nil, safeActorError(err)
	}
	active := 0
	for i := range sessions {
		if sessions[i].State != vendorstore.StateClosed && sessions[i].State != vendorstore.StateDetached {
			active++
		}
	}
	m.mu.RLock()
	reserved := m.startReservations
	m.mu.RUnlock()
	if active+reserved > maxVendorSessionsPerHost {
		release()
		return nil, ErrVendorSessionCapacity
	}
	return release, nil
}

func (m *VendorSessions) startAdapter(ctx context.Context, spec *vendor.StartSpec) (vendor.Session, <-chan claude.Observation, *pendingOwner, error) {
	finishSpawn := m.beginInflightSpawn()
	defer finishSpawn()
	var pending *pendingOwner
	specCopy := *spec
	origOnSpawned := spec.OnSpawned
	specCopy.OnSpawned = func(owner vendor.PeerOwner) {
		if m.deps.TestDelaySpawnRegistration != nil {
			m.deps.TestDelaySpawnRegistration(ctx)
		}
		pending = m.addPendingOwner(owner)
		finishSpawn()
		if origOnSpawned != nil {
			origOnSpawned(owner)
		}
	}
	session, err := m.deps.Adapter.Start(ctx, &specCopy)
	if err != nil {
		finishSpawn()
		m.retirePendingOwner(pending)
		return nil, nil, nil, safeActorError(err)
	}
	finishSpawn()
	observations, ok := observationsOf(session)
	if !ok {
		m.closeAndSettlePending(ctx, session, pending)
		return nil, nil, nil, errcode.New(errcode.FailedPrecondition, "adapter session does not expose observations")
	}
	return session, observations, pending, nil
}

func (m *VendorSessions) addPendingOwner(owner vendor.PeerOwner) *pendingOwner {
	if owner == nil {
		return nil
	}
	p := &pendingOwner{owner: owner}
	m.mu.Lock()
	m.pendingOwners[p] = struct{}{}
	m.mu.Unlock()
	return p
}

func (m *VendorSessions) removePendingOwner(p *pendingOwner) {
	if p == nil {
		return
	}
	m.mu.Lock()
	delete(m.pendingOwners, p)
	m.mu.Unlock()
}

func (m *VendorSessions) retainOwner(owner vendor.PeerOwner) {
	if owner == nil {
		owner = missingPeerOwner{}
	}
	m.mu.Lock()
	m.retainedOwners[&retainedOwner{owner: owner}] = struct{}{}
	m.mu.Unlock()
}

func (m *VendorSessions) retirePendingOwner(p *pendingOwner) {
	if p == nil {
		return
	}
	m.mu.Lock()
	delete(m.pendingOwners, p)
	if p.owner != nil {
		m.retainedOwners[&retainedOwner{owner: p.owner}] = struct{}{}
	}
	m.mu.Unlock()
}

func (m *VendorSessions) closeAndSettlePending(ctx context.Context, session vendor.Session, pending *pendingOwner) {
	proof, err := session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	if err != nil {
		m.deps.Logger.ErrorContext(ctx, "cleaning up an untracked vendor process failed", "proof", proof.Status)
	}
	if err == nil && proof.Status == process.StopProofProved {
		m.removePendingOwner(pending)
		return
	}
	if pending != nil {
		m.retirePendingOwner(pending)
		return
	}
	if owner, ok := session.(vendor.PeerOwner); ok {
		m.retainOwner(owner)
		return
	}
	m.retainOwner(missingPeerOwner{})
}

func (m *VendorSessions) installActor(sessionID string, actor *vendorSessionActor, pending *pendingOwner) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.actors[sessionID] != nil {
		return errcode.New(errcode.FailedPrecondition, "vendor session is already active or manager is closed")
	}
	if pending != nil {
		delete(m.pendingOwners, pending)
	}
	m.actors[sessionID] = actor
	actor.task = m.spawn("app.vendor-session", actor.run)
	return nil
}

func observationsOf(session vendor.Session) (<-chan claude.Observation, bool) {
	observed, ok := session.(interface {
		Observations() <-chan claude.Observation
	})
	if !ok {
		return nil, false
	}
	return observed.Observations(), true
}

func setProcessIdentity(info *vendorstore.Session, session vendor.Session) {
	identified, ok := session.(interface {
		Identity() process.Identity
		PID() int
	})
	if !ok {
		return
	}
	identity := identified.Identity()
	info.ProcessPID, info.ProcessStartToken, info.ProcessScopeUnit = int64(identified.PID()), identity.StartToken, identity.ScopeUnit
}

// List returns the durable vendor session records.
func (m *VendorSessions) List(ctx context.Context) ([]vendorstore.Session, error) {
	sessions, err := m.deps.Store.ListSessions(ctx)
	if err != nil {
		return nil, safeActorError(err)
	}
	return sessions, nil
}

// SlashCommands returns the live actor's latest vendor command list.
func (m *VendorSessions) SlashCommands(ctx context.Context, sessionID string) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("check slash command context: %w", err)
	}
	m.mu.RLock()
	actor := m.actors[sessionID]
	m.mu.RUnlock()
	if actor == nil {
		return nil, errcode.New(errcode.VendorSessionNotFound, "the vendor session does not exist or is not live")
	}
	provider, ok := actor.session.(interface{ SlashCommands() []string })
	if !ok {
		return nil, errcode.New(errcode.VendorUnsupported, "the vendor does not publish slash commands")
	}
	return provider.SlashCommands(), nil
}

func (m *VendorSessions) call(ctx context.Context, sessionID string, cmd actorCommand) error {
	actor := m.actor(sessionID)
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	return actor.call(ctx, cmd)
}

func (m *VendorSessions) actor(sessionID string) *vendorSessionActor {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.actors[sessionID]
}

// CloseAll asks every live actor to stop during host shutdown.
func (m *VendorSessions) CloseAll(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	actors := make([]*vendorSessionActor, 0, len(m.actors))
	for _, actor := range m.actors {
		actors = append(actors, actor)
	}
	m.mu.Unlock()
	for _, actor := range actors {
		if err := actor.requestClose(vendor.CloseHostStop); err != nil {
			return err
		}
	}
	var failures []error
	for _, actor := range actors {
		select {
		case <-actor.done:
		case <-ctx.Done():
			failures = append(failures, ctx.Err())
		}
	}
	return errors.Join(failures...)
}
