package app

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type commandKind uint8

const (
	commandCreate commandKind = iota
	commandReady
	commandSend
	commandSteer
	commandInterrupt
	commandDetach
	commandClose
	commandResume
	commandActiveTurn
	commandRecordEvent
	commandRecordItem
)

type transcriptItemOp struct {
	prev *transcript.Item
	next *transcript.Item
}

type actorCommand struct {
	kind    commandKind
	input   vendor.Input
	reason  vendor.CloseReason
	turnOut *string
	event   *vendorstore.Event
	itemOp  *transcriptItemOp
}

type actorRequest struct {
	command actorCommand
	result  chan error
}

type vendorSessionActor struct {
	manager       *VendorSessions
	info          vendorstore.Session
	session       vendor.Session
	obs           <-chan vendor.Observation
	commands      chan actorRequest
	state         vendor.State
	sequence      int64
	turnID        string
	slashCommands []string
	turns         map[string]bool
	finished      bool
	stopProved    bool
	// attached turns on once the create or resume command has committed:
	// a real process reports observations immediately, and none may be
	// appended before the session row exists.
	attached             bool
	task                 *supervise.Task
	vaultSub             *vault.Subscription
	done                 chan struct{}
	interruptResults     chan vaultInterruptResult
	vaultInterruptTurnID string
	userInterruptTurnID  string
	closeResults         chan actorCloseResult
	detachResults        chan actorCloseResult
	touchCh              chan struct{}
	idleTimer            clock.Timer
	idleCh               <-chan time.Time
	detaching            bool
	closeOnce            sync.Once
	closeMu              sync.Mutex
	closeReason          vendor.CloseReason
}

type vaultInterruptResult struct {
	turnID string
	err    error
}

type actorCloseResult struct {
	reason vendor.CloseReason
	proof  process.StopProof
	err    error
}

func newVendorSessionActor(manager *VendorSessions, info *vendorstore.Session, session vendor.Session, obs <-chan vendor.Observation) *vendorSessionActor {
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
		interruptResults: make(chan vaultInterruptResult, 1), closeResults: make(chan actorCloseResult, 1),
		detachResults: make(chan actorCloseResult, 1), touchCh: make(chan struct{}, 1),
	}
}

func (a *vendorSessionActor) call(ctx context.Context, command *actorCommand) error {
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
	case a.commands <- actorRequest{command: *command, result: result}:
	case <-a.done:
		if command.kind == commandClose {
			return nil
		}
		return vendor.ErrSessionClosed
	case <-ctx.Done():
		return fmt.Errorf("send actor command: %w", ctx.Err())
	}
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		return fmt.Errorf("wait for actor command: %w", ctx.Err())
	case <-a.done:
		if command.kind == commandClose {
			return nil
		}
		return vendor.ErrSessionClosed
	}
}

func (a *vendorSessionActor) run(ctx context.Context) error {
	defer close(a.done)
	defer a.manager.resolveTurnWaiter(a.info.ID, errcode.New(errcode.Unavailable, "vendor session ended"))
	defer a.manager.removeActor(a.info.ID, a)
	if ended := a.manager.deps.OnSessionEnded; ended != nil {
		defer ended(a.info.ID)
	}
	defer a.vaultSub.Close()
	defer a.retirePeerOwner()
	defer a.stopIdleTimer()
	for {
		if err := a.runStep(ctx); err != nil {
			return err
		}
		if a.finished {
			return nil
		}
	}
}

func (a *vendorSessionActor) runStep(ctx context.Context) error {
	var observations <-chan vendor.Observation
	var vaultEvents <-chan vault.Snapshot
	if a.attached {
		observations, vaultEvents = a.obs, a.vaultSub.Events()
	}
	select {
	case req := <-a.commands:
		return a.handleRequest(ctx, &req)
	case observation, ok := <-observations:
		return a.failOnError(ctx, a.handleObserved(ctx, &observation, ok))
	case snapshot, ok := <-vaultEvents:
		return a.handleVaultEvent(ctx, snapshot, ok)
	case result := <-a.interruptResults:
		return a.failOnError(ctx, a.handleInterruptResult(ctx, result))
	case result := <-a.detachResults:
		a.recordStopResult(result.proof, result.err)
		return a.failOnError(ctx, a.persistDetach(ctx, result.proof, result.err))
	case result := <-a.closeResults:
		a.recordStopResult(result.proof, result.err)
		return a.failOnError(ctx, a.persistClose(ctx, result.reason, result.proof, result.err))
	case <-a.touchCh:
		a.resetIdleTimer(ctx)
	case <-a.idleCh:
		a.handleIdleTimeout(ctx)
	case <-ctx.Done():
		return a.stopAfterHostCancel(ctx)
	}
	return nil
}

func (a *vendorSessionActor) failOnError(ctx context.Context, err error) error {
	if err != nil {
		return a.fail(ctx, err)
	}
	return nil
}

func (a *vendorSessionActor) handleRequest(ctx context.Context, req *actorRequest) error {
	err := a.handleCommand(ctx, &req.command)
	req.result <- err
	if a.finished {
		return nil
	}
	if errors.Is(err, ErrVendorActorFailed) {
		return a.fail(ctx, err)
	}
	return nil
}

func (a *vendorSessionActor) handleObserved(ctx context.Context, observation *vendor.Observation, ok bool) error {
	if !ok {
		return a.processExited(ctx)
	}
	return a.handleObservation(ctx, observation)
}

func (a *vendorSessionActor) handleVaultEvent(ctx context.Context, snapshot vault.Snapshot, ok bool) error {
	if !ok {
		snapshot = a.manager.deps.Vault.Snapshot()
	} else if a.vaultSub.Lagging() {
		snapshot = a.vaultSub.Resync()
	}
	if err := a.handleVaultState(ctx, snapshot); err != nil {
		return err
	}
	return nil
}

func (a *vendorSessionActor) stopAfterHostCancel(ctx context.Context) error {
	proof, err := a.session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	a.recordStopResult(proof, err)
	if err != nil {
		a.manager.deps.Logger.ErrorContext(context.WithoutCancel(ctx), "stopping vendor session actor after host cancellation failed", "proof", proof.Status)
	}
	a.finished = true
	return nil
}

func (a *vendorSessionActor) recordStopResult(proof process.StopProof, err error) {
	a.stopProved = err == nil && proof.Status == process.StopProofProved
}

func (a *vendorSessionActor) retirePeerOwner() {
	if a.stopProved {
		return
	}
	owner, ok := a.session.(vendor.PeerOwner)
	if !ok {
		owner = missingPeerOwner{}
	}
	a.manager.retainOwner(owner)
}

func (a *vendorSessionActor) handleCommand(ctx context.Context, command *actorCommand) error {
	switch command.kind {
	case commandReady:
		return a.markReady(ctx)
	case commandCreate:
		return a.createSession(ctx)
	case commandResume:
		return a.resumeSession(ctx)
	case commandSend:
		return a.send(ctx, command.input)
	case commandSteer:
		return a.steer(ctx, command.input)
	case commandInterrupt:
		return a.interrupt(ctx)
	case commandDetach:
		return a.startDetach()
	case commandClose:
		return a.requestClose(command.reason)
	case commandActiveTurn, commandRecordEvent, commandRecordItem:
		return a.handleRecordCommand(ctx, command)
	default:
		return errcode.New(errcode.InvalidArgument, "unknown vendor session command")
	}
}

func (a *vendorSessionActor) handleRecordCommand(ctx context.Context, command *actorCommand) error {
	if command.kind == commandActiveTurn {
		return a.readActiveTurn(command.turnOut)
	}
	if command.kind == commandRecordEvent {
		return a.append(ctx, command.event)
	}
	if command.itemOp.prev != nil {
		return a.updateItem(ctx, command.itemOp.prev, command.itemOp.next)
	}
	return a.recordItem(ctx, command.itemOp.next)
}

func (a *vendorSessionActor) markReady(ctx context.Context) error {
	if err := a.transition(ctx, vendor.Fact{Kind: vendor.FactReady}); err != nil {
		return err
	}
	a.resetIdleTimer(ctx)
	return nil
}

func (a *vendorSessionActor) readActiveTurn(out *string) error {
	turnID := a.activeTurnID()
	if turnID == "" {
		return errcode.New(errcode.FailedPrecondition, "no active vendor turn")
	}
	*out = turnID
	return nil
}

func (a *vendorSessionActor) activeTurnID() string {
	if a.turnID != "" {
		return a.turnID
	}
	if len(a.turns) != 1 {
		return ""
	}
	for id := range a.turns {
		return id
	}
	return ""
}

func (a *vendorSessionActor) createSession(ctx context.Context) error {
	if err := a.manager.deps.Store.CreateSession(context.WithoutCancel(ctx), &a.info); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	if err := a.manager.deps.Transcripts.RegisterSession(a.info.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	a.attached = true
	return nil
}

func (a *vendorSessionActor) resumeSession(ctx context.Context) error {
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactResume})
	if err != nil {
		return fmt.Errorf("resume vendor session state: %w", err)
	}
	if err := a.manager.deps.Transcripts.RegisterSession(a.info.ID); err != nil {
		return fmt.Errorf("register resumed transcript topics: %w", err)
	}
	if err := a.append(ctx, &vendorstore.Event{
		Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateStarting,
		ProcessPID: a.info.ProcessPID, ProcessStartToken: a.info.ProcessStartToken,
		ProcessScopeUnit: a.info.ProcessScopeUnit,
	}); err != nil {
		return err
	}
	a.state, a.attached = next, true
	return nil
}

func (a *vendorSessionActor) send(ctx context.Context, input vendor.Input) error {
	if a.detaching {
		return vendor.ErrSessionClosed
	}
	if a.state.Kind != vendor.StateReady {
		return vendor.ErrBusy
	}
	turn, err := a.session.Send(ctx, input)
	if err != nil {
		if errors.Is(err, vendor.ErrSessionClosed) {
			return vendor.ErrSessionClosed
		}
		return safeActorError(err)
	}
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactFugueSend})
	if err != nil {
		return fmt.Errorf("start Fugue turn state: %w", err)
	}
	state, waiting := stateToStore(next)
	if appendErr := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: turn.ID, TurnOrigin: vendorstore.TurnOriginFugue, State: state, WaitingReason: waiting}); appendErr != nil {
		if closeErr := a.requestClose(vendor.CloseActorFailure); closeErr != nil {
			return closeErr
		}
		return errcode.New(errcode.Unavailable, "vendor turn could not be persisted; session is closing")
	}
	a.stopIdleTimer()
	a.turnID, a.turns[turn.ID] = turn.ID, true
	a.state = next
	a.manager.notifyTurnStarted(a.info.ID)
	itemID, err := a.manager.deps.IDs.NewID()
	if err != nil {
		return errcode.New(errcode.Internal, "could not allocate transcript item identity")
	}
	origin := string(input.Origin)
	if origin == "" {
		origin = string(vendor.OriginUser)
	}
	item := transcript.Item{
		ID: itemID, SessionID: a.info.ID, TurnID: turn.ID, Kind: transcript.UserMessage,
		Metadata: transcript.UserMessageMetadata{Origin: origin},
		Content:  transcript.UserMessageContent{Text: input.Text},
	}
	if err := a.recordItem(ctx, &item); err != nil {
		return err
	}
	config.TriggerCrashPoint(a.manager.deps.CrashPoint, config.CrashPointVendorTurnActive)
	return nil
}

func (a *vendorSessionActor) steer(ctx context.Context, input vendor.Input) error {
	if a.state.Kind != vendor.StateRunning || a.turnID == "" {
		return vendor.ErrTurnNotRunning
	}
	err := a.session.Steer(ctx, vendor.TurnRef{ID: a.turnID}, input)
	if errors.Is(err, vendor.ErrSessionClosed) {
		return nil
	}
	return safeActorError(err)
}

func (a *vendorSessionActor) interrupt(ctx context.Context) error {
	if a.state.Kind != vendor.StateRunning || a.turnID == "" {
		return vendor.ErrTurnNotRunning
	}
	turnID := a.turnID
	a.userInterruptTurnID = turnID
	if a.manager.deps.Relay != nil {
		a.manager.deps.Relay.CancelTurn(a.info.ID, turnID)
	}
	supervise.Go(ctx, "user-interrupt-"+a.info.ID, func(taskCtx context.Context) error {
		err := a.session.Interrupt(taskCtx, vendor.TurnRef{ID: turnID})
		select {
		case a.interruptResults <- vaultInterruptResult{turnID: turnID, err: err}:
		case <-taskCtx.Done():
		}
		return nil
	})
	return nil
}

func (a *vendorSessionActor) requestClose(reason vendor.CloseReason) error {
	a.closeOnce.Do(func() {
		a.closeMu.Lock()
		a.closeReason = reason
		a.closeMu.Unlock()
		if ended := a.manager.deps.OnSessionEnded; ended != nil {
			ended(a.info.ID)
		}
		a.manager.spawn("close-vendor-session-"+a.info.ID, func(ctx context.Context) error {
			proof, err := a.session.Close(context.WithoutCancel(ctx), reason)
			select {
			case a.closeResults <- actorCloseResult{reason: reason, proof: proof, err: err}:
			case <-a.done:
			}
			return nil
		})
	})
	return nil
}

func (a *vendorSessionActor) requestedCloseReason() vendor.CloseReason {
	a.closeMu.Lock()
	defer a.closeMu.Unlock()
	return a.closeReason
}
