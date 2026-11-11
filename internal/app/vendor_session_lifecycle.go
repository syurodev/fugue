package app

import (
	"context"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

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
			return fmt.Errorf("record close turn state: %w", transitionErr)
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
		return fmt.Errorf("close vendor session state: %w", transitionErr)
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
		return fmt.Errorf("record process exit state: %w", transitionErr)
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
