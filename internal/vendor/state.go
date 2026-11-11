package vendor

import (
	"errors"
	"fmt"
)

// SessionState is one state in the vendor session lifecycle.
type SessionState string

// Session state values describe the control lifecycle without runtime resources.
const (
	StateStarting         SessionState = "starting"
	StateReady            SessionState = "ready"
	StateRunning          SessionState = "running"
	StateAwaitingApproval SessionState = "awaiting_approval"
	StateWaiting          SessionState = "waiting"
	StateDetached         SessionState = "detached"
	StateClosed           SessionState = "closed"
)

// WaitingReason identifies why a session cannot currently accept work.
type WaitingReason string

// Waiting reasons that can suspend new session work.
const (
	WaitingVaultLocked WaitingReason = "vault_locked"
	WaitingQuota       WaitingReason = "quota"
	WaitingCapacity    WaitingReason = "capacity"
)

// State is the value owned by the pure session state machine.
type State struct {
	Kind   SessionState
	Reason WaitingReason
}

// Outcome describes the result of one vendor turn.
type Outcome string

// Turn outcomes persisted by the session actor.
const (
	OutcomeCompleted   Outcome = "completed"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeFailed      Outcome = "failed"
	OutcomeUnknown     Outcome = "unknown"
)

// FactKind identifies an input to the state machine.
type FactKind string

// Fact kinds accepted by Transition.
const (
	FactReady             FactKind = "ready"
	FactFugueSend         FactKind = "fugue_send"
	FactVendorTurn        FactKind = "vendor_turn"
	FactApprovalRequested FactKind = "approval_requested"
	FactApprovalGranted   FactKind = "approval_granted"
	FactApprovalDenied    FactKind = "approval_denied"
	FactTurnCompleted     FactKind = "turn_completed"
	FactTurnInterrupted   FactKind = "turn_interrupted"
	FactTurnFailed        FactKind = "turn_failed"
	FactTurnUnknown       FactKind = "turn_unknown"
	FactWait              FactKind = "wait"
	FactUnlocked          FactKind = "unlocked"
	FactProcessExited     FactKind = "process_exited"
	FactHostRestarted     FactKind = "host_restarted"
	FactResume            FactKind = "resume"
	FactClose             FactKind = "close"
)

// Fact is a bounded control fact; vendor text never belongs here.
type Fact struct {
	Kind   FactKind
	Reason WaitingReason
	Code   string
}

// EffectKind identifies work requested from the session actor.
type EffectKind string

// Effect kinds returned by Transition for the session actor to execute.
const (
	EffectStartTurn     EffectKind = "start_turn"
	EffectEndTurn       EffectKind = "end_turn"
	EffectInterruptTurn EffectKind = "interrupt_turn"
	EffectWait          EffectKind = "wait"
	EffectResume        EffectKind = "resume"
	EffectClose         EffectKind = "close"
)

// Effect describes work for the actor; the state machine itself performs none.
type Effect struct {
	Kind    EffectKind
	Outcome Outcome
	Code    string
	Reason  WaitingReason
}

// ErrInvalidTransition marks a malformed fact or unsupported state transition.
var ErrInvalidTransition = errors.New("invalid vendor session transition")

type transitionFunc func(State, Fact) (State, []Effect, error)

// Transition applies one fact without I/O, clocks, or mutable global state.
func Transition(current State, fact Fact) (State, []Effect, error) {
	if !validStateValue(current) || !validFact(fact) {
		return State{}, nil, fmt.Errorf("%w: malformed state or fact", ErrInvalidTransition)
	}
	handlers := map[FactKind]transitionFunc{
		FactReady: transitionReady, FactFugueSend: transitionSend, FactVendorTurn: transitionVendorTurn,
		FactApprovalRequested: transitionApprovalRequested, FactApprovalGranted: transitionApprovalResolved,
		FactApprovalDenied: transitionApprovalResolved, FactTurnCompleted: transitionTurnEnded,
		FactTurnInterrupted: transitionTurnEnded, FactTurnFailed: transitionTurnEnded, FactTurnUnknown: transitionTurnUnknown,
		FactWait: transitionWait, FactUnlocked: transitionUnlocked, FactProcessExited: transitionProcessExited,
		FactHostRestarted: transitionHostRestarted, FactResume: transitionResume, FactClose: transitionClose,
	}
	apply, ok := handlers[fact.Kind]
	if !ok {
		return invalidTransition(current, fact)
	}
	return apply(current, fact)
}

func transitionReady(current State, fact Fact) (State, []Effect, error) {
	return stateFrom(current, fact, StateStarting, State{Kind: StateReady})
}

func transitionSend(current State, fact Fact) (State, []Effect, error) {
	if current.Kind != StateReady {
		return invalidTransition(current, fact)
	}
	return State{Kind: StateRunning}, []Effect{{Kind: EffectStartTurn}}, nil
}

func transitionVendorTurn(current State, fact Fact) (State, []Effect, error) {
	if current.Kind != StateReady && current.Kind != StateRunning && current.Kind != StateAwaitingApproval {
		return invalidTransition(current, fact)
	}
	// Vendor-origin turns are transcript data, not Fugue session commands.
	return current, nil, nil
}

func transitionApprovalRequested(current State, fact Fact) (State, []Effect, error) {
	return stateFrom(current, fact, StateRunning, State{Kind: StateAwaitingApproval})
}

func transitionApprovalResolved(current State, fact Fact) (State, []Effect, error) {
	return stateFrom(current, fact, StateAwaitingApproval, State{Kind: StateRunning})
}

func transitionTurnEnded(current State, fact Fact) (State, []Effect, error) {
	if !turnActive(current.Kind) {
		return invalidTransition(current, fact)
	}
	outcome := map[FactKind]Outcome{FactTurnCompleted: OutcomeCompleted, FactTurnInterrupted: OutcomeInterrupted, FactTurnFailed: OutcomeFailed}[fact.Kind]
	return State{Kind: StateReady}, []Effect{{Kind: EffectEndTurn, Outcome: outcome, Code: fact.Code}}, nil
}

func transitionTurnUnknown(current State, fact Fact) (State, []Effect, error) {
	if !turnActive(current.Kind) {
		return invalidTransition(current, fact)
	}
	next := State{Kind: StateDetached}
	if fact.Reason == WaitingVaultLocked {
		next = State{Kind: StateWaiting, Reason: WaitingVaultLocked}
	}
	return next, []Effect{{Kind: EffectEndTurn, Outcome: OutcomeUnknown}}, nil
}

func transitionWait(current State, fact Fact) (State, []Effect, error) {
	if current.Kind == StateClosed || current.Kind == StateDetached {
		return invalidTransition(current, fact)
	}
	effects := make([]Effect, 0, 2)
	if turnActive(current.Kind) {
		effects = append(effects, Effect{Kind: EffectInterruptTurn})
	}
	effects = append(effects, Effect{Kind: EffectWait, Reason: fact.Reason})
	return State{Kind: StateWaiting, Reason: fact.Reason}, effects, nil
}

func transitionUnlocked(current State, fact Fact) (State, []Effect, error) {
	if current.Kind != StateWaiting || current.Reason != WaitingVaultLocked {
		return invalidTransition(current, fact)
	}
	return State{Kind: StateReady}, nil, nil
}

func transitionProcessExited(current State, fact Fact) (State, []Effect, error) {
	if current.Kind == StateClosed || current.Kind == StateDetached {
		return invalidTransition(current, fact)
	}
	effects := endUnknownTurn(current.Kind)
	effects = append(effects, Effect{Kind: EffectClose})
	return State{Kind: StateClosed}, effects, nil
}

func transitionHostRestarted(current State, fact Fact) (State, []Effect, error) {
	if current.Kind == StateClosed || current.Kind == StateDetached {
		return invalidTransition(current, fact)
	}
	return State{Kind: StateDetached}, endUnknownTurn(current.Kind), nil
}

func transitionResume(current State, fact Fact) (State, []Effect, error) {
	return stateFrom(current, fact, StateDetached, State{Kind: StateStarting}, Effect{Kind: EffectResume})
}

func transitionClose(current State, fact Fact) (State, []Effect, error) {
	if current.Kind == StateClosed {
		return invalidTransition(current, fact)
	}
	return State{Kind: StateClosed}, []Effect{{Kind: EffectClose}}, nil
}

func stateFrom(current State, fact Fact, allowed SessionState, next State, effects ...Effect) (State, []Effect, error) {
	if current.Kind != allowed {
		return invalidTransition(current, fact)
	}
	return next, effects, nil
}

func turnActive(state SessionState) bool {
	return state == StateRunning || state == StateAwaitingApproval
}

func endUnknownTurn(state SessionState) []Effect {
	if turnActive(state) {
		return []Effect{{Kind: EffectEndTurn, Outcome: OutcomeUnknown}}
	}
	return nil
}

func invalidTransition(current State, fact Fact) (State, []Effect, error) {
	return State{}, nil, fmt.Errorf("%w: %s + %s", ErrInvalidTransition, current.Kind, fact.Kind)
}

func validStateValue(state State) bool {
	switch state.Kind {
	case StateStarting, StateReady, StateRunning, StateAwaitingApproval, StateDetached, StateClosed:
		return state.Reason == ""
	case StateWaiting:
		return state.Reason == WaitingVaultLocked || state.Reason == WaitingQuota || state.Reason == WaitingCapacity
	default:
		return false
	}
}

func validFact(fact Fact) bool {
	switch fact.Kind {
	case FactTurnFailed:
		return fact.Reason == "" && fact.Code != ""
	case FactTurnUnknown:
		return fact.Code == "" && (fact.Reason == "" || fact.Reason == WaitingVaultLocked)
	case FactWait:
		return fact.Code == "" && (fact.Reason == WaitingVaultLocked || fact.Reason == WaitingQuota || fact.Reason == WaitingCapacity)
	case FactReady, FactFugueSend, FactVendorTurn, FactApprovalRequested, FactApprovalGranted,
		FactApprovalDenied, FactTurnCompleted, FactTurnInterrupted, FactUnlocked,
		FactProcessExited, FactHostRestarted, FactResume, FactClose:
		return noFactDetails(fact)
	}
	return false
}

func noFactDetails(fact Fact) bool {
	return fact.Reason == "" && fact.Code == ""
}
