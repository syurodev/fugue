package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
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
	defer a.vaultSub.Close()
	for {
		select {
		case req := <-a.commands:
			if err := a.handleRequest(ctx, req); err != nil {
				return err
			}
		case observation, ok := <-a.obs:
			if err := a.handleObserved(ctx, &observation, ok); err != nil {
				return a.fail(ctx, err)
			}
		case snapshot, ok := <-a.vaultSub.Events():
			if err := a.handleVaultEvent(ctx, snapshot, ok); err != nil {
				return err
			}
		case <-ctx.Done():
			return a.stopAfterHostCancel(ctx)
		}
		if a.finished {
			return nil
		}
	}
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
	if err != nil {
		a.manager.deps.Logger.ErrorContext(context.WithoutCancel(ctx), "stopping vendor session actor after host cancellation failed", "proof", proof.Status)
	}
	return nil
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
			return fmt.Errorf("record vault interruption state: %w", transitionErr)
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
		return a.close(ctx, command.reason)
	default:
		return errcode.New(errcode.InvalidArgument, "unknown vendor session command")
	}
}

func (a *vendorSessionActor) createSession(ctx context.Context) error {
	if err := a.manager.deps.Store.CreateSession(context.WithoutCancel(ctx), &a.info); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	if err := a.manager.deps.Transcripts.RegisterSession(a.info.ID); err != nil {
		return fmt.Errorf("%w: %w", ErrVendorActorFailed, safeActorError(err))
	}
	return nil
}

func (a *vendorSessionActor) resumeSession(ctx context.Context) error {
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactResume})
	if err != nil {
		return fmt.Errorf("resume vendor session state: %w", err)
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
}

func (a *vendorSessionActor) send(ctx context.Context, input vendor.Input) error {
	if a.state.Kind != vendor.StateReady {
		return vendor.ErrBusy
	}
	turn, err := a.session.Send(context.WithoutCancel(ctx), input)
	if err != nil {
		return safeActorError(err)
	}
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactFugueSend})
	if err != nil {
		return fmt.Errorf("start Fugue turn state: %w", err)
	}
	a.turnID, a.turns[turn.ID] = turn.ID, true
	state, waiting := stateToStore(next)
	if appendErr := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnStarted, TurnID: turn.ID, TurnOrigin: vendorstore.TurnOriginFugue, State: state, WaitingReason: waiting}); appendErr != nil {
		return appendErr
	}
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
	err := a.session.Steer(context.WithoutCancel(ctx), vendor.TurnRef{ID: a.turnID}, input)
	return safeActorError(err)
}

func (a *vendorSessionActor) interrupt(ctx context.Context) error {
	if a.state.Kind != vendor.StateRunning || a.turnID == "" {
		return vendor.ErrTurnNotRunning
	}
	err := a.session.Interrupt(context.WithoutCancel(ctx), vendor.TurnRef{ID: a.turnID})
	if err != nil {
		return a.close(ctx, vendor.CloseUserRequest)
	}
	return nil
}
