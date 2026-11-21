package app

import (
	"context"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// awaitSessionEndWork holds the actor's final close writes until the
// session-end work (the pending team proposal's cancel) has settled. The
// store refuses transcript writes once the session row is closed, so that
// work's item flip must land first; the actor keeps serving commands while it
// waits, because the flip arrives as one.
func (a *vendorSessionActor) awaitSessionEndWork(ctx context.Context) error {
	wait := a.manager.deps.SessionEndSettled
	if wait == nil {
		return nil
	}
	settled := wait(a.info.ID)
	if settled == nil {
		return nil
	}
	for {
		select {
		case <-settled:
			return nil
		case req := <-a.commands:
			if err := a.handleRequest(ctx, &req); err != nil {
				return err
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *vendorSessionActor) persistClose(ctx context.Context, reason vendor.CloseReason, proof process.StopProof, closeErr error) error {
	if err := a.awaitSessionEndWork(ctx); err != nil {
		return err
	}
	status := string(proof.Status)
	if status != string(process.StopProofProved) || closeErr != nil {
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
			return fmt.Errorf("record close turn state: %w", transitionErr)
		}
		state, waiting := stateToStore(next)
		if err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: a.turnID, Outcome: outcome, State: state, WaitingReason: waiting}); err != nil {
			return err
		}
		delete(a.turns, a.turnID)
		a.state = next
		a.turnID = ""
	}
	closedState, _, transitionErr := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactClose})
	if transitionErr != nil {
		return fmt.Errorf("close vendor session state: %w", transitionErr)
	}
	if err := a.append(context.WithoutCancel(ctx), &vendorstore.Event{
		Type: vendorstore.VendorSessionClosed, CloseReason: string(reason), StopProof: status,
		State: vendorstore.StateClosed,
	}); err != nil {
		return err
	}
	a.state = closedState
	a.finished = true
	return nil
}

func (a *vendorSessionActor) processExited(ctx context.Context) error {
	if a.detaching {
		a.attached = false
		return nil
	}
	if err := a.awaitSessionEndWork(ctx); err != nil {
		return err
	}
	next := vendor.State{Kind: vendor.StateClosed}
	if a.state.Kind != vendor.StateDetached {
		var transitionErr error
		next, _, transitionErr = vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactProcessExited})
		if transitionErr != nil {
			return fmt.Errorf("record process exit state: %w", transitionErr)
		}
	}
	if a.turnID != "" {
		if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorTurnEnded, TurnID: a.turnID, Outcome: vendorstore.OutcomeUnknown}); err != nil {
			return err
		}
		a.turnID = ""
	}
	reason := a.requestedCloseReason()
	if reason == "" {
		reason = vendor.CloseHostStop
	}
	proof, stopErr := a.session.Close(context.WithoutCancel(ctx), reason)
	a.recordStopResult(proof, stopErr)
	status := string(proof.Status)
	if stopErr != nil || status != string(process.StopProofProved) {
		status = string(process.StopProofUnknown)
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorProcessExited, StopProof: status}); err != nil {
		return err
	}
	if err := a.append(ctx, &vendorstore.Event{Type: vendorstore.VendorSessionClosed, StopProof: status, State: vendorstore.StateClosed, CloseReason: string(reason)}); err != nil {
		return err
	}
	a.finished = true
	a.state = next
	return nil
}

func (a *vendorSessionActor) fail(ctx context.Context, cause error) error {
	a.manager.deps.Logger.ErrorContext(ctx, "vendor session actor stopped after a persistence failure", "error_code", string(errcode.CodeOf(cause)))
	proof, closeErr := a.session.Close(context.WithoutCancel(ctx), vendor.CloseHostStop)
	a.recordStopResult(proof, closeErr)
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

func (a *vendorSessionActor) handleVaultState(ctx context.Context, snapshot vault.Snapshot) error {
	if snapshot.State == vault.StateUnlocked {
		return a.handleVaultUnlocked(ctx)
	}
	if a.state.Kind == vendor.StateWaiting && a.state.Reason == vendor.WaitingVaultLocked {
		return nil
	}
	if a.turnID != "" {
		a.startVaultInterrupt(ctx)
		return nil
	}
	if a.state.Kind != vendor.StateWaiting {
		a.stopIdleTimer()
		return a.transition(ctx, vendor.Fact{Kind: vendor.FactWait, Reason: vendor.WaitingVaultLocked})
	}
	return nil
}

func (a *vendorSessionActor) handleVaultUnlocked(ctx context.Context) error {
	if a.state.Kind != vendor.StateWaiting || a.state.Reason != vendor.WaitingVaultLocked {
		return nil
	}
	if err := a.transition(ctx, vendor.Fact{Kind: vendor.FactUnlocked}); err != nil {
		return err
	}
	a.resetIdleTimer(ctx)
	return nil
}

func (a *vendorSessionActor) startVaultInterrupt(ctx context.Context) {
	if a.vaultInterruptTurnID == a.turnID {
		return
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
	a.stopIdleTimer()
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
	a.cancelTurnInteractions(turnID)
	a.resetIdleTimer(ctx)
	a.manager.notifyWake(a.info.ID)
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
	a.cancelTurnInteractions(turnID)
	a.stopIdleTimer()
	return nil
}

func (m *VendorSessions) removeActor(sessionID string, actor *vendorSessionActor) {
	m.mu.Lock()
	if m.actors[sessionID] == actor {
		delete(m.actors, sessionID)
	}
	m.mu.Unlock()
}

// CloseAll asks every live actor to stop during host shutdown.
func (m *VendorSessions) CloseAll(ctx context.Context) error {
	m.CancelPendingSends()
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
