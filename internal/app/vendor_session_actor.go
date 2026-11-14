package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
)

type commandKind uint8

const (
	commandCreate commandKind = iota
	commandReady
	commandSend
	commandSteer
	commandInterrupt
	commandClose
	commandResume
	commandActiveTurn
	commandRecordEvent
)

type actorCommand struct {
	kind    commandKind
	input   vendor.Input
	reason  vendor.CloseReason
	turnOut *string
	event   *vendorstore.Event
}

type actorRequest struct {
	command actorCommand
	result  chan error
}

type vendorSessionActor struct {
	manager       *VendorSessions
	info          vendorstore.Session
	session       vendor.Session
	obs           <-chan claude.Observation
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
		interruptResults: make(chan vaultInterruptResult, 1), closeResults: make(chan actorCloseResult, 1),
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
	if ended := a.manager.deps.OnSessionEnded; ended != nil {
		defer ended(a.info.ID)
	}
	defer a.vaultSub.Close()
	defer a.retirePeerOwner()
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
	var observations <-chan claude.Observation
	var vaultEvents <-chan vault.Snapshot
	if a.attached {
		observations, vaultEvents = a.obs, a.vaultSub.Events()
	}
	select {
	case req := <-a.commands:
		return a.handleRequest(ctx, req)
	case observation, ok := <-observations:
		if err := a.handleObserved(ctx, &observation, ok); err != nil {
			return a.fail(ctx, err)
		}
	case snapshot, ok := <-vaultEvents:
		return a.handleVaultEvent(ctx, snapshot, ok)
	case result := <-a.interruptResults:
		if err := a.handleInterruptResult(ctx, result); err != nil {
			return a.fail(ctx, err)
		}
	case result := <-a.closeResults:
		a.recordStopResult(result.proof, result.err)
		if err := a.persistClose(ctx, result.reason, result.proof, result.err); err != nil {
			return a.fail(ctx, err)
		}
	case <-ctx.Done():
		return a.stopAfterHostCancel(ctx)
	}
	return nil
}

func (a *vendorSessionActor) handleRequest(ctx context.Context, req actorRequest) error {
	err := a.handleCommand(ctx, req.command)
	req.result <- err
	if a.finished {
		return nil
	}
	if errors.Is(err, ErrVendorActorFailed) {
		return a.fail(ctx, err)
	}
	return nil
}

func (a *vendorSessionActor) handleObserved(ctx context.Context, observation *claude.Observation, ok bool) error {
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
		if a.vaultInterruptTurnID == a.turnID {
			return nil
		}
		turnID := a.turnID
		a.vaultInterruptTurnID = turnID
		supervise.Go(ctx, "vault-interrupt-"+a.info.ID, func(taskCtx context.Context) error {
			err := a.session.Interrupt(taskCtx, vendor.TurnRef{ID: turnID})
			select {
			case a.interruptResults <- vaultInterruptResult{turnID: turnID, err: err}:
			case <-taskCtx.Done():
			}
			return nil
		})
		return nil
	}
	if a.state.Kind != vendor.StateWaiting {
		return a.transition(ctx, vendor.Fact{Kind: vendor.FactWait, Reason: vendor.WaitingVaultLocked})
	}
	return nil
}

func (a *vendorSessionActor) handleVaultInterruptResult(ctx context.Context, result vaultInterruptResult) error {
	if result.turnID == "" || result.turnID != a.vaultInterruptTurnID || a.turnID != result.turnID || result.err == nil {
		return nil
	}
	return a.endVaultLockedTurn(ctx, vendorstore.OutcomeUnknown, vendor.Fact{Kind: vendor.FactTurnUnknown, Reason: vendor.WaitingVaultLocked})
}

func (a *vendorSessionActor) handleInterruptResult(ctx context.Context, result vaultInterruptResult) error {
	if result.turnID == a.vaultInterruptTurnID {
		return a.handleVaultInterruptResult(ctx, result)
	}
	if result.turnID == "" || result.turnID != a.userInterruptTurnID || a.turnID != result.turnID || result.err == nil {
		return nil
	}
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactTurnUnknown})
	if err != nil {
		return fmt.Errorf("record interrupted turn state: %w", err)
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: result.turnID, Outcome: vendorstore.OutcomeUnknown, State: vendorstore.StateDetached}); err != nil {
		return err
	}
	a.state, a.turnID, a.userInterruptTurnID = next, "", ""
	delete(a.turns, result.turnID)
	return nil
}

func (a *vendorSessionActor) endInterruptedTurn(ctx context.Context, turnID, outcome string, fact vendor.Fact) error {
	next, _, err := vendor.Transition(a.state, fact)
	if err != nil {
		return fmt.Errorf("record interrupted turn state: %w", err)
	}
	state, waiting := stateToStore(next)
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: outcome, State: state, WaitingReason: waiting}); err != nil {
		return err
	}
	a.state, a.turnID, a.userInterruptTurnID = next, "", ""
	delete(a.turns, turnID)
	return nil
}

func (a *vendorSessionActor) endVaultLockedTurn(ctx context.Context, outcome string, fact vendor.Fact) error {
	next, _, err := vendor.Transition(a.state, fact)
	if err != nil {
		return fmt.Errorf("record vault interruption state: %w", err)
	}
	if next.Kind != vendor.StateWaiting || next.Reason != vendor.WaitingVaultLocked {
		next, _, err = vendor.Transition(next, vendor.Fact{Kind: vendor.FactWait, Reason: vendor.WaitingVaultLocked})
		if err != nil {
			return fmt.Errorf("wait for vault unlock after interrupt: %w", err)
		}
	}
	state, reason := stateToStore(next)
	turnID := a.turnID
	if err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: turnID, Outcome: outcome, State: state, WaitingReason: reason}); err != nil {
		return err
	}
	a.state = next
	a.turnID = ""
	a.vaultInterruptTurnID = ""
	a.userInterruptTurnID = ""
	delete(a.turns, turnID)
	return nil
}

func (a *vendorSessionActor) handleCommand(ctx context.Context, command actorCommand) error {
	switch command.kind {
	case commandReady:
		return a.transition(ctx, vendor.Fact{Kind: vendor.FactReady})
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
	case commandClose:
		return a.requestClose(command.reason)
	case commandActiveTurn:
		turnID := a.activeTurnID()
		if turnID == "" {
			return errcode.New(errcode.FailedPrecondition, "no active vendor turn")
		}
		*command.turnOut = turnID
		return nil
	case commandRecordEvent:
		return a.append(ctx, command.event)
	default:
		return errcode.New(errcode.InvalidArgument, "unknown vendor session command")
	}
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
	a.turnID, a.turns[turn.ID] = turn.ID, true
	a.state = next
	itemID, err := a.manager.deps.IDs.NewID()
	if err != nil {
		return errcode.New(errcode.Internal, "could not allocate transcript item identity")
	}
	item := transcript.Item{
		ID: itemID, SessionID: a.info.ID, TurnID: turn.ID, Kind: transcript.UserMessage,
		Metadata: transcript.UserMessageMetadata{Origin: "fugue"},
		Content:  transcript.UserMessageContent{Text: input.Text},
	}
	return a.recordItem(ctx, &item)
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
