package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

var (
	ErrVendorActorFailed     = errcode.New(errcode.Internal, "vendor session actor failed")
	ErrVendorSessionCapacity = errcode.New(errcode.FailedPrecondition, "vendor session capacity reached")
)

type VendorSessionDeps struct {
	Store       vendorSessionStore
	Transcripts *Transcripts
	Adapter     vendor.Adapter
	Vault       vendorVaultObserver
	Clock       clock.Clock
	IDs         ids.Generator
	Logger      *slog.Logger
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
	ctx               context.Context
	deps              VendorSessionDeps
	mu                sync.RWMutex
	actors            map[string]*vendorSessionActor
	closed            bool
	startReservations int
}

func NewVendorSessions(ctx context.Context, deps VendorSessionDeps) (*VendorSessions, error) {
	if ctx == nil || deps.Store == nil || deps.Transcripts == nil || deps.Adapter == nil || deps.Vault == nil || deps.Clock == nil || deps.IDs == nil {
		return nil, errcode.New(errcode.InvalidArgument, "vendor session dependencies are required")
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return &VendorSessions{ctx: ctx, deps: deps, actors: make(map[string]*vendorSessionActor)}, nil
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
	started, observations, err := m.startAdapter(ctx, spec)
	if err != nil {
		return err
	}
	setProcessIdentity(session, started)
	session.State = vendorstore.StateStarting
	session.ResumeHandle = spec.ResumeSessionID
	actor := newVendorSessionActor(m, session, started, observations)
	if err := m.installActor(session.ID, actor); err != nil {
		m.closeUntracked(ctx, started)
		return err
	}
	if err := actor.call(ctx, actorCommand{kind: commandCreate}); err != nil {
		return err
	}
	return actor.call(ctx, actorCommand{kind: commandReady})
}

func (m *VendorSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return err
	}
	return m.call(ctx, sessionID, actorCommand{kind: commandSend, input: input})
}

func (m *VendorSessions) Steer(ctx context.Context, sessionID string, input vendor.Input) error {
	if err := vendor.ValidateInput(input); err != nil {
		return err
	}
	return m.call(ctx, sessionID, actorCommand{kind: commandSteer, input: input})
}

func (m *VendorSessions) Interrupt(ctx context.Context, sessionID string) error {
	return m.call(ctx, sessionID, actorCommand{kind: commandInterrupt})
}

func (m *VendorSessions) Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error {
	return m.call(ctx, sessionID, actorCommand{kind: commandClose, reason: reason})
}

// Resume attaches a fresh adapter process to a detached session. Existing
// turns are never replayed; the caller supplies a new resume-enabled spec.
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
	started, observations, err := m.startAdapter(ctx, &resumeSpec)
	if err != nil {
		return err
	}
	setProcessIdentity(&info, started)
	actor := newVendorSessionActor(m, &info, started, observations)
	if err := m.installActor(sessionID, actor); err != nil {
		m.closeUntracked(ctx, started)
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
	if active+reserved > 2 {
		release()
		return nil, ErrVendorSessionCapacity
	}
	return release, nil
}

func (m *VendorSessions) startAdapter(ctx context.Context, spec *vendor.StartSpec) (vendor.Session, <-chan claude.Observation, error) {
	session, err := m.deps.Adapter.Start(ctx, spec)
	if err != nil {
		return nil, nil, safeActorError(err)
	}
	observations, ok := observationsOf(session)
	if !ok {
		m.closeUntracked(ctx, session)
		return nil, nil, errcode.New(errcode.FailedPrecondition, "adapter session does not expose observations")
	}
	return session, observations, nil
}

func (m *VendorSessions) closeUntracked(ctx context.Context, session vendor.Session) {
	proof, err := session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	if err != nil {
		m.deps.Logger.ErrorContext(ctx, "cleaning up an untracked vendor process failed", "proof", proof.Status)
	}
}

func (m *VendorSessions) installActor(sessionID string, actor *vendorSessionActor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed || m.actors[sessionID] != nil {
		return errcode.New(errcode.FailedPrecondition, "vendor session is already active or manager is closed")
	}
	m.actors[sessionID] = actor
	actor.task = supervise.Go(m.ctx, "app.vendor-session", actor.run)
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

func (m *VendorSessions) List(ctx context.Context) ([]vendorstore.Session, error) {
	return m.deps.Store.ListSessions(ctx)
}

func (m *VendorSessions) call(ctx context.Context, sessionID string, cmd actorCommand) error {
	m.mu.RLock()
	actor := m.actors[sessionID]
	m.mu.RUnlock()
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	return actor.call(ctx, cmd)
}

func (m *VendorSessions) CloseAll(ctx context.Context) error {
	m.mu.Lock()
	m.closed = true
	actors := make([]*vendorSessionActor, 0, len(m.actors))
	for _, actor := range m.actors {
		actors = append(actors, actor)
	}
	m.mu.Unlock()
	var failures []error
	for _, actor := range actors {
		if err := actor.call(ctx, actorCommand{kind: commandClose, reason: vendor.CloseHostStop}); err != nil && !errors.Is(err, context.Canceled) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

type commandKind uint8

const (
	commandCreate commandKind = iota
	commandReady
	commandSend
	commandSteer
	commandInterrupt
	commandClose
	commandResume
)

type actorCommand struct {
	kind   commandKind
	input  vendor.Input
	reason vendor.CloseReason
}

type actorRequest struct {
	command actorCommand
	result  chan error
}

type vendorSessionActor struct {
	manager  *VendorSessions
	info     vendorstore.Session
	session  vendor.Session
	obs      <-chan claude.Observation
	commands chan actorRequest
	state    vendor.State
	sequence int64
	turnID   string
	turns    map[string]bool
	finished bool
	task     *supervise.Task
	vaultSub *vault.Subscription
	done     chan struct{}
}

func newVendorSessionActor(manager *VendorSessions, info *vendorstore.Session, session vendor.Session, obs <-chan claude.Observation) *vendorSessionActor {
	_, vaultSub := manager.deps.Vault.Observe()
	state := vendor.State{Kind: vendor.SessionState(info.State), Reason: vendor.WaitingReason(info.WaitingReason)}
	sequence := info.LastSequence
	if sequence == 0 {
		sequence = 1
	}
	return &vendorSessionActor{
		manager: manager, info: *info, session: session, obs: obs,
		commands: make(chan actorRequest), state: state,
		sequence: sequence, turns: make(map[string]bool), vaultSub: vaultSub, done: make(chan struct{}),
	}
}

func (a *vendorSessionActor) call(ctx context.Context, command actorCommand) error {
	select {
	case <-a.done:
		if command.kind == commandClose {
			return nil
		}
		return vendor.ErrSessionClosed
	default:
	}
	result := make(chan error, 1)
	select {
	case a.commands <- actorRequest{command: command, result: result}:
	case <-a.done:
		if command.kind == commandClose {
			return nil
		}
		return vendor.ErrSessionClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-a.done:
		if command.kind == commandClose {
			return nil
		}
		return vendor.ErrSessionClosed
	}
}

func (a *vendorSessionActor) run(ctx context.Context) error {
	defer close(a.done)
	defer a.vaultSub.Close()
	for {
		select {
		case req := <-a.commands:
			err := a.handleCommand(ctx, req.command)
			req.result <- err
			if a.finished {
				return nil
			}
			if errors.Is(err, ErrVendorActorFailed) {
				return a.fail(ctx, err)
			}
		case observation, ok := <-a.obs:
			if !ok {
				return a.processExited(ctx)
			}
			if err := a.handleObservation(ctx, observation); err != nil {
				return a.fail(ctx, err)
			}
		case snapshot, ok := <-a.vaultSub.Events():
			if !ok {
				return a.handleVaultState(ctx, a.manager.deps.Vault.Snapshot())
			}
			if a.vaultSub.Lagging() {
				snapshot = a.vaultSub.Resync()
			}
			if err := a.handleVaultState(ctx, snapshot); err != nil {
				return a.fail(ctx, err)
			}
		case <-ctx.Done():
			proof, err := a.session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
			if err != nil {
				a.manager.deps.Logger.ErrorContext(context.WithoutCancel(ctx), "stopping vendor session actor after host cancellation failed", "proof", proof.Status)
			}
			return nil
		}
	}
}

func (a *vendorSessionActor) handleVaultState(ctx context.Context, snapshot vault.Snapshot) error {
	if snapshot.State == vault.StateUnlocked {
		if a.state.Kind == vendor.StateWaiting && a.state.Reason == vendor.WaitingVaultLocked {
			return a.transition(ctx, vendor.Fact{Kind: vendor.FactUnlocked})
		}
		return nil
	}
	if a.state.Kind == vendor.StateWaiting && a.state.Reason == vendor.WaitingVaultLocked {
		return nil
	}
	if a.turnID != "" {
		interruptErr := a.session.Interrupt(context.WithoutCancel(ctx), vendor.TurnRef{ID: a.turnID})
		outcome := vendorstore.OutcomeInterrupted
		fact := vendor.Fact{Kind: vendor.FactTurnInterrupted}
		if interruptErr != nil {
			outcome = vendorstore.OutcomeUnknown
			fact = vendor.Fact{Kind: vendor.FactTurnUnknown, Reason: vendor.WaitingVaultLocked}
		}
		if err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: a.turnID, Outcome: outcome}); err != nil {
			return err
		}
		a.turnID = ""
		next, _, transitionErr := vendor.Transition(a.state, fact)
		if transitionErr != nil {
			return transitionErr
		}
		a.state = next
	}
	if a.state.Kind != vendor.StateWaiting {
		return a.transition(ctx, vendor.Fact{Kind: vendor.FactWait, Reason: vendor.WaitingVaultLocked})
	}
	return nil
}

func (a *vendorSessionActor) handleCommand(ctx context.Context, command actorCommand) error {
	switch command.kind {
	case commandReady:
		return a.transition(ctx, vendor.Fact{Kind: vendor.FactReady})
	case commandCreate:
		if err := a.manager.deps.Store.CreateSession(context.WithoutCancel(ctx), &a.info); err != nil {
			return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
		}
		if err := a.manager.deps.Transcripts.RegisterSession(a.info.ID); err != nil {
			return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
		}
		return nil
	case commandResume:
		next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactResume})
		if err != nil {
			return err
		}
		if err := a.append(ctx, &vendorstore.Event{
			Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateStarting,
			ProcessPID: a.info.ProcessPID, ProcessStartToken: a.info.ProcessStartToken,
			ProcessScopeUnit: a.info.ProcessScopeUnit,
		}); err != nil {
			return err
		}
		a.state = next
		return nil
	case commandSend:
		if a.state.Kind != vendor.StateReady {
			return vendor.ErrBusy
		}
		turn, err := a.session.Send(context.WithoutCancel(ctx), command.input)
		if err != nil {
			return safeActorError(err)
		}
		next, _, transitionErr := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactFugueSend})
		if transitionErr != nil {
			return transitionErr
		}
		a.turnID = turn.ID
		a.turns[a.turnID] = true
		state, waiting := stateToStore(next)
		if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: turn.ID, TurnOrigin: vendorstore.TurnOriginFugue, State: state, WaitingReason: waiting}); err != nil {
			return err
		}
		a.state = next
		itemID, idErr := a.manager.deps.IDs.NewID()
		if idErr != nil {
			return errcode.New(errcode.Internal, "could not allocate transcript item identity")
		}
		inputItem := transcript.Item{
			ID: itemID, SessionID: a.info.ID, TurnID: turn.ID, Kind: transcript.UserMessage,
			Metadata: transcript.UserMessageMetadata{Origin: "fugue"},
			Content:  transcript.UserMessageContent{Text: command.input.Text},
		}
		return a.recordItem(ctx, &inputItem)
	case commandSteer:
		if a.state.Kind != vendor.StateRunning || a.turnID == "" {
			return vendor.ErrTurnNotRunning
		}
		return safeActorError(a.session.Steer(context.WithoutCancel(ctx), vendor.TurnRef{ID: a.turnID}, command.input))
	case commandInterrupt:
		if a.state.Kind != vendor.StateRunning || a.turnID == "" {
			return vendor.ErrTurnNotRunning
		}
		err := a.session.Interrupt(context.WithoutCancel(ctx), vendor.TurnRef{ID: a.turnID})
		if err != nil {
			return a.close(ctx, vendor.CloseUserRequest)
		}
		return nil
	case commandClose:
		return a.close(ctx, command.reason)
	default:
		return errcode.New(errcode.InvalidArgument, "unknown vendor session command")
	}
}

func (a *vendorSessionActor) handleObservation(ctx context.Context, observation claude.Observation) error {
	if observation.Init != nil && ids.IsUUID(observation.Init.SessionID) && observation.Init.SessionID != a.info.ResumeHandle {
		if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, ResumeHandle: observation.Init.SessionID}); err != nil {
			return err
		}
		a.info.ResumeHandle = observation.Init.SessionID
	}
	for i := range observation.Records {
		item := observation.Records[i]
		if strings.HasPrefix(item.TurnID, "vendor-") && !a.turns[item.TurnID] {
			if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: item.TurnID, TurnOrigin: vendorstore.TurnOriginVendor}); err != nil {
				return err
			}
			a.turns[item.TurnID] = true
			if _, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactVendorTurn}); err != nil {
				return err
			}
		}
		if err := a.recordItem(ctx, &item); err != nil {
			return err
		}
	}
	for i := range observation.Updates {
		update := observation.Updates[i]
		if a.manager.deps.Vault.Snapshot().State != vault.StateUnlocked {
			update.After.Content = nil
			update.After.ContentTruncated = false
			update.After.ContentOriginalSize = 0
		}
		if err := a.manager.deps.Transcripts.UpdateItem(ctx, a.info.ID, a.sequence, &update.Before, &update.After, nil); err != nil {
			return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
		}
		a.sequence++
	}
	if observation.Partial != nil {
		partial := observation.Partial
		a.manager.deps.Transcripts.StreamItem(a.info.ID, partial.MessageID, 0, transcript.ViewText, partial.Fragment)
	}
	if observation.TurnEnd != nil {
		outcome := vendor.OutcomeCompleted
		fact := vendor.Fact{Kind: vendor.FactTurnCompleted}
		storeOutcome := vendorstore.OutcomeCompleted
		switch observation.TurnEnd.Outcome {
		case claude.OutcomeCompleted:
		case claude.OutcomeFailed:
			fact = vendor.Fact{Kind: vendor.FactTurnFailed, Code: "vendor_failure"}
			outcome = vendor.OutcomeFailed
			storeOutcome = vendorstore.OutcomeFailed
		default:
			fact = vendor.Fact{Kind: vendor.FactTurnFailed, Code: "vendor_failure"}
			outcome = vendor.OutcomeFailed
			storeOutcome = vendorstore.OutcomeFailed
		}
		if strings.HasPrefix(observation.TurnEnd.TurnID, "vendor-") {
			if a.turns[observation.TurnEnd.TurnID] {
				if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: observation.TurnEnd.TurnID, Outcome: storeOutcome, FailureCode: failureCode(outcome)}); err != nil {
					return err
				}
				delete(a.turns, observation.TurnEnd.TurnID)
			}
			return nil
		}
		turnID := a.turnID
		if turnID == "" {
			return nil
		}
		next, _, transitionErr := vendor.Transition(a.state, fact)
		if transitionErr != nil {
			return transitionErr
		}
		if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: storeOutcome, FailureCode: failureCode(outcome), State: vendorstore.StateReady}); err != nil {
			return err
		}
		a.state = next
		delete(a.turns, turnID)
		a.turnID = ""
	}
	return nil
}

func (a *vendorSessionActor) recordItem(ctx context.Context, item *transcript.Item) error {
	if a.manager.deps.Vault.Snapshot().State != vault.StateUnlocked {
		item.Content = nil
		item.ContentTruncated = false
		item.ContentOriginalSize = 0
		item.RawTruncated = false
		item.RawOriginalSize = 0
	}
	if err := a.manager.deps.Transcripts.RecordItem(ctx, a.info.ID, a.sequence, item, nil); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	a.sequence++
	return nil
}

func (a *vendorSessionActor) transition(ctx context.Context, fact vendor.Fact) error {
	next, _, err := vendor.Transition(a.state, fact)
	if err != nil {
		return err
	}
	state, reason := stateToStore(next)
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, State: state, WaitingReason: reason}); err != nil {
		return err
	}
	a.state = next
	return nil
}

func (a *vendorSessionActor) append(ctx context.Context, event *vendorstore.Event) error {
	next, err := a.manager.deps.Store.Append(ctx, a.info.ID, a.sequence, event)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	if next != a.sequence+1 {
		return fmt.Errorf("%w: non-contiguous append result", ErrVendorActorFailed)
	}
	a.sequence = next
	return nil
}

func (a *vendorSessionActor) close(ctx context.Context, reason vendor.CloseReason) error {
	proof, closeErr := a.session.Close(context.WithoutCancel(ctx), reason)
	status := string(proof.Status)
	if status != string(process.StopProofProved) {
		status = string(process.StopProofUnknown)
	}
	if closeErr != nil {
		status = string(process.StopProofUnknown)
	}
	if a.turnID != "" {
		outcome := vendorstore.OutcomeUnknown
		fact := vendor.Fact{Kind: vendor.FactTurnUnknown}
		if status == string(process.StopProofProved) {
			outcome = vendorstore.OutcomeInterrupted
			fact = vendor.Fact{Kind: vendor.FactTurnInterrupted}
		}
		next, _, transitionErr := vendor.Transition(a.state, fact)
		if transitionErr != nil {
			return transitionErr
		}
		state, waiting := stateToStore(next)
		if err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: a.turnID, Outcome: outcome, State: state, WaitingReason: waiting}); err != nil {
			return err
		}
		a.state = next
		a.turnID = ""
	}
	closedState, _, transitionErr := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactClose})
	if transitionErr != nil {
		return transitionErr
	}
	err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{
		Type: vendorstore.VendorSessionClosed, CloseReason: string(reason), StopProof: status,
		State: vendorstore.StateClosed,
	})
	if err != nil {
		return err
	}
	a.state = closedState
	a.finished = true
	return nil
}

func (a *vendorSessionActor) processExited(ctx context.Context) error {
	next, _, transitionErr := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactProcessExited})
	if transitionErr != nil {
		return transitionErr
	}
	if a.turnID != "" {
		if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: a.turnID, Outcome: vendorstore.OutcomeUnknown}); err != nil {
			return err
		}
		a.turnID = ""
	}
	proof, stopErr := a.session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	status := string(proof.Status)
	if stopErr != nil || status != string(process.StopProofProved) {
		status = string(process.StopProofUnknown)
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorProcessExited, StopProof: status}); err != nil {
		return err
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionClosed, StopProof: status, State: vendorstore.StateClosed, CloseReason: string(vendor.CloseHostStop)}); err != nil {
		return err
	}
	a.finished = true
	a.state = next
	return nil
}

func (a *vendorSessionActor) fail(ctx context.Context, cause error) error {
	a.manager.deps.Logger.ErrorContext(ctx, "vendor session actor stopped after a persistence failure", "error_code", string(errcode.CodeOf(cause)))
	_, closeErr := a.session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	if closeErr != nil {
		a.manager.deps.Logger.ErrorContext(ctx, "vendor process cleanup after actor failure failed")
	}
	a.finished = true
	return ErrVendorActorFailed
}

func safeActorError(err error) error {
	if err == nil {
		return nil
	}
	// Adapter and store errors can contain wire output. Expose only typed codes.
	return errcode.New(errcode.CodeOf(err), "vendor session operation failed")
}

func stateToStore(state vendor.State) (string, string) {
	return string(state.Kind), string(state.Reason)
}

func failureCode(outcome vendor.Outcome) string {
	if outcome == vendor.OutcomeFailed {
		return "vendor_failure"
	}
	return ""
}
