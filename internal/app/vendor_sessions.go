package app

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

var (
	// ErrVendorActorFailed reports a safe, public actor failure.
	ErrVendorActorFailed = errcode.New(errcode.Internal, "vendor session actor failed")
	// ErrVendorSessionCapacity reports exhaustion of the per-account or machine-wide session tier.
	ErrVendorSessionCapacity = errcode.New(errcode.FailedPrecondition, "vendor session capacity reached")
)

type slotAccountKey struct {
	vendor  string
	account string
}

// VendorSessionDeps contains the runtime dependencies for vendor sessions.
type VendorSessionDeps struct {
	Store              vendorSessionStore
	Transcripts        *Transcripts
	Adapter            vendor.Adapter
	Adapters           map[string]vendor.Adapter
	Relay              serverRequestRelayer
	HostResources      HostResources
	HostConfig         HostConfigReader
	TeamsConfig        func(ctx context.Context, workspaceID string) (workspace.TeamsConfig, error)
	HasPendingApproval func(sessionID string) bool
	PromptPacingJitter func(span time.Duration) time.Duration
	// Accounts receives each session's quota windows and classified failures.
	Accounts *vendor.Accounts
	Vault    vendorVaultObserver
	Clock    clock.Clock
	IDs      ids.Generator
	Logger   *slog.Logger
	// TurnSnapshots snapshots the working directory around every turn for the
	// turn_diff item; nil disables turn snapshots (git-guard §3).
	TurnSnapshots TurnSnapshotter
	// TurnScanBytes bounds the added content the end-of-turn secret scan reads;
	// zero means TurnScanCap (git-guard §6).
	TurnScanBytes int64
	// OnSessionEnded runs once when a session's actor stops, however it
	// stopped; the host revokes the session token there.
	OnSessionEnded func(sessionID string)
	// SessionEndSettled returns a channel that closes when the work started
	// by OnSessionEnded has finished, or nil when none is running. The actor
	// holds its close until then, still serving commands, so that work can
	// write to the session's transcript before the store closes it.
	SessionEndSettled func(sessionID string) <-chan struct{}
	// TestDelaySpawnRegistration runs inside OnSpawned before owner registration.
	TestDelaySpawnRegistration func(context.Context)
	// TestOnWaitInflightSpawn runs when OwnsPeer begins waiting on in-flight spawns.
	TestOnWaitInflightSpawn func()
	// TestAfterPeerSnapshot runs between snapshotting owners and checking live actors.
	TestAfterPeerSnapshot func()
	// CrashPoint is the optional named Stage A crash point armed via internal/config.
	CrashPoint string
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
	wakeHook          func(sessionID string)
	turnStartedHook   func(sessionID string)
	observationHook   func(sessionID string)
	turnCompletedHook func(sessionID string)
	turnFailedHook    func(sessionID, failureCode string)
	sessionDiedHook   func(sessionID string)
	closed            bool
	pacer             *vendorPacer
	capMu             sync.Mutex
	capabilities      map[string]vendor.Capability
	slotMu            sync.Mutex
	reservations      map[string]slotAccountKey
	inflightSpawns    int
	spawnSettled      chan struct{}
	waiterMu          sync.Mutex
	turnWaiters       map[string]chan error
	overlaps          *turnOverlaps
	snapshotWg        sync.WaitGroup
}

type pendingOwner struct {
	owner vendor.PeerOwner
}

type retainedOwner struct {
	owner vendor.PeerOwner
}

// NewVendorSessions creates the in-process manager and binds actor lifetime to ctx.
func NewVendorSessions(ctx context.Context, deps *VendorSessionDeps) (*VendorSessions, error) {
	if ctx == nil || deps == nil || deps.Store == nil || deps.Transcripts == nil || (deps.Adapter == nil && len(deps.Adapters) == 0) || deps.Vault == nil || deps.Clock == nil || deps.IDs == nil {
		return nil, errcode.New(errcode.InvalidArgument, "vendor session dependencies are required")
	}
	copyDeps := *deps
	if copyDeps.Logger == nil {
		copyDeps.Logger = slog.Default()
	}
	spawn := func(name string, fn func(context.Context) error) *supervise.Task {
		return supervise.Go(ctx, name, fn)
	}
	return &VendorSessions{
		deps:           copyDeps,
		spawn:          spawn,
		actors:         make(map[string]*vendorSessionActor),
		pendingOwners:  make(map[*pendingOwner]struct{}),
		retainedOwners: make(map[*retainedOwner]struct{}),
		capabilities:   make(map[string]vendor.Capability),
		reservations:   make(map[string]slotAccountKey),
		turnWaiters:    make(map[string]chan error),
		overlaps:       newTurnOverlaps(),
		pacer:          newVendorPacer(copyDeps.Clock, spawn, copyDeps.HostConfig, copyDeps.PromptPacingJitter),
	}, nil
}

// SetRelay binds the server-request relay after SessionToolService is constructed.
func (m *VendorSessions) SetRelay(relay serverRequestRelayer) {
	m.deps.Relay = relay
}

// Start creates a vendor process and hands its observations to a single actor.
// The supplied session ID is also the adapter StartSpec session ID.
func (m *VendorSessions) Start(ctx context.Context, session *vendorstore.Session, spec *vendor.StartSpec) error {
	return m.startSession(ctx, session, spec, false)
}

// StartWithReservation starts a vendor session using a slot already reserved
// via ReserveSlot; the caller's release callback cleans up the reservation.
func (m *VendorSessions) StartWithReservation(ctx context.Context, session *vendorstore.Session, spec *vendor.StartSpec) error {
	return m.startSession(ctx, session, spec, true)
}

func (m *VendorSessions) startSession(ctx context.Context, session *vendorstore.Session, spec *vendor.StartSpec, preReserved bool) error {
	if session == nil || spec == nil || session.ID == "" || spec.SessionID != session.ID {
		return errcode.New(errcode.InvalidArgument, "session identity is required")
	}
	if m.deps.Vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.FailedPrecondition, "vault must be unlocked to start a vendor session")
	}
	if !preReserved {
		release, capacityErr := m.reserveSlot(ctx, session.ID, session.Vendor, session.AccountID)
		if capacityErr != nil {
			return capacityErr
		}
		defer release()
	} else if !m.hasReservation(session.ID) {
		return errcode.New(errcode.FailedPrecondition, "missing slot reservation for vendor session")
	}
	started, observations, pending, err := m.startAdapter(ctx, session.Vendor, spec)
	if err != nil {
		return err
	}
	setProcessIdentity(session, started)
	session.State = vendorstore.StateStarting
	session.ResumeHandle = spec.ResumeSessionID
	session.Model = spec.Model
	session.Effort = spec.Effort
	actor := newVendorSessionActor(m, session, started, observations)
	actor.cwd = spec.Cwd
	if err := m.installActor(session.ID, actor, pending); err != nil {
		m.closeAndSettlePending(ctx, started, pending)
		return err
	}
	if err := actor.call(ctx, &actorCommand{kind: commandCreate}); err != nil {
		return err
	}
	return actor.call(ctx, &actorCommand{kind: commandReady})
}

// Send starts a vendor turn, pacing origin=fugue prompts per vendor while
// letting origin=user prompts bypass immediately.
func (m *VendorSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return fmt.Errorf("validate vendor input: %w", err)
	}
	actor := m.actor(sessionID)
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	if input.Origin == vendor.OriginFugue {
		return m.sendPacedSync(ctx, actor, input)
	}
	return actor.call(ctx, &actorCommand{kind: commandSend, input: input})
}

// Steer adds input to the active vendor turn.
func (m *VendorSessions) Steer(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return fmt.Errorf("validate vendor input: %w", err)
	}
	return m.call(ctx, sessionID, &actorCommand{kind: commandSteer, input: input})
}

// Interrupt asks the active vendor turn to stop.
func (m *VendorSessions) Interrupt(ctx context.Context, sessionID string) error {
	return m.call(ctx, sessionID, &actorCommand{kind: commandInterrupt})
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
	info, err := m.deps.Store.Snapshot(ctx, sessionID)
	if err != nil {
		return safeActorError(err)
	}
	if info.State != vendorstore.StateDetached || info.ResumeHandle == "" {
		return errcode.New(errcode.FailedPrecondition, "vendor session is not resumable")
	}
	release, capacityErr := m.reserveSlot(ctx, sessionID, info.Vendor, info.AccountID)
	if capacityErr != nil {
		return capacityErr
	}
	defer release()
	resumeSpec := *spec
	resumeSpec.ResumeSessionID = info.ResumeHandle
	started, observations, pending, err := m.startAdapter(ctx, info.Vendor, &resumeSpec)
	if err != nil {
		return err
	}
	setProcessIdentity(&info, started)
	actor := newVendorSessionActor(m, &info, started, observations)
	actor.cwd = spec.Cwd
	if err := m.installActor(sessionID, actor, pending); err != nil {
		m.closeAndSettlePending(ctx, started, pending)
		return err
	}
	if err := actor.call(ctx, &actorCommand{kind: commandResume}); err != nil {
		return err
	}
	return actor.call(ctx, &actorCommand{kind: commandReady})
}

func (m *VendorSessions) adapterFor(vendorName string) (vendor.Adapter, error) {
	if m.deps.Adapters != nil {
		if ad, ok := m.deps.Adapters[vendorName]; ok && ad != nil {
			return ad, nil
		}
	}
	if (vendorName == "" || vendorName == VendorClaude) && m.deps.Adapter != nil {
		return m.deps.Adapter, nil
	}
	return nil, errcode.New(errcode.VendorUnsupported, "the vendor is not supported yet")
}

func (m *VendorSessions) startAdapter(ctx context.Context, vendorName string, spec *vendor.StartSpec) (vendor.Session, <-chan vendor.Observation, *pendingOwner, error) {
	adapter, err := m.adapterFor(vendorName)
	if err != nil {
		return nil, nil, nil, err
	}
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
	session, err := adapter.Start(ctx, &specCopy)
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

func observationsOf(session vendor.Session) (<-chan vendor.Observation, bool) {
	observed, ok := session.(interface {
		Observations() <-chan vendor.Observation
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

// ActiveTurnID returns the session actor's active turn ID.
func (m *VendorSessions) ActiveTurnID(ctx context.Context, sessionID string) (string, error) {
	var turnID string
	if err := m.call(ctx, sessionID, &actorCommand{kind: commandActiveTurn, turnOut: &turnID}); err != nil {
		return "", err
	}
	return turnID, nil
}

// RecordEvent appends a domain event through the session's single-writer actor.
func (m *VendorSessions) RecordEvent(ctx context.Context, sessionID string, event *vendorstore.Event) error {
	return m.call(ctx, sessionID, &actorCommand{kind: commandRecordEvent, event: event})
}

func (m *VendorSessions) call(ctx context.Context, sessionID string, cmd *actorCommand) error {
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
