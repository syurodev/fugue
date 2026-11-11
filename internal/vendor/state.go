package vendor

import (
	"errors"
	"fmt"
)

// SessionState is a session-control state. It contains no runtime resources.
type SessionState string

const (
	StateStarting         SessionState = "starting"
	StateReady            SessionState = "ready"
	StateRunning          SessionState = "running"
	StateAwaitingApproval SessionState = "awaiting_approval"
	StateWaiting          SessionState = "waiting"
	StateDetached         SessionState = "detached"
	StateClosed           SessionState = "closed"
)

type WaitingReason string

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

type Outcome string

const (
	OutcomeCompleted   Outcome = "completed"
	OutcomeInterrupted Outcome = "interrupted"
	OutcomeFailed      Outcome = "failed"
	OutcomeUnknown     Outcome = "unknown"
)

type FactKind string

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

type EffectKind string

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

var ErrInvalidTransition = errors.New("invalid vendor session transition")

// Transition applies one fact without I/O, clocks, or mutable global state.
func Transition(current State, fact Fact) (State, []Effect, error) {
	if !validStateValue(current) || !validFact(fact) {
		return State{}, nil, fmt.Errorf("%w: malformed state or fact", ErrInvalidTransition)
	}
	next := current
	var effects []Effect
	switch fact.Kind {
	case FactReady:
		if current.Kind != StateStarting {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateReady}
	case FactFugueSend:
		if current.Kind != StateReady {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateRunning}
		effects = append(effects, Effect{Kind: EffectStartTurn})
	case FactVendorTurn:
		if current.Kind != StateReady && current.Kind != StateRunning && current.Kind != StateAwaitingApproval {
			return invalidTransition(current, fact)
		}
		// A normalizer-reported vendor turn is transcript data, not a Fugue send.
	case FactApprovalRequested:
		if current.Kind != StateRunning {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateAwaitingApproval}
	case FactApprovalGranted, FactApprovalDenied:
		if current.Kind != StateAwaitingApproval {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateRunning}
	case FactTurnCompleted, FactTurnInterrupted, FactTurnFailed:
		if current.Kind != StateRunning && current.Kind != StateAwaitingApproval {
			return invalidTransition(current, fact)
		}
		outcome := map[FactKind]Outcome{
			FactTurnCompleted:   OutcomeCompleted,
			FactTurnInterrupted: OutcomeInterrupted,
			FactTurnFailed:      OutcomeFailed,
		}[fact.Kind]
		next = State{Kind: StateReady}
		effects = append(effects, Effect{Kind: EffectEndTurn, Outcome: outcome, Code: fact.Code})
	case FactTurnUnknown:
		if current.Kind != StateRunning && current.Kind != StateAwaitingApproval {
			return invalidTransition(current, fact)
		}
		if fact.Reason == WaitingVaultLocked {
			next = State{Kind: StateWaiting, Reason: WaitingVaultLocked}
		} else {
			next = State{Kind: StateDetached}
		}
		effects = append(effects, Effect{Kind: EffectEndTurn, Outcome: OutcomeUnknown})
	case FactWait:
		if current.Kind == StateClosed || current.Kind == StateDetached {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateWaiting, Reason: fact.Reason}
		if current.Kind == StateRunning || current.Kind == StateAwaitingApproval {
			effects = append(effects, Effect{Kind: EffectInterruptTurn})
		}
		effects = append(effects, Effect{Kind: EffectWait, Reason: fact.Reason})
	case FactUnlocked:
		if current.Kind != StateWaiting || current.Reason != WaitingVaultLocked {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateReady}
	case FactProcessExited:
		if current.Kind == StateClosed || current.Kind == StateDetached {
			return invalidTransition(current, fact)
		}
		if current.Kind == StateRunning || current.Kind == StateAwaitingApproval {
			effects = append(effects, Effect{Kind: EffectEndTurn, Outcome: OutcomeUnknown})
		}
		next = State{Kind: StateClosed}
		effects = append(effects, Effect{Kind: EffectClose})
	case FactHostRestarted:
		if current.Kind == StateClosed || current.Kind == StateDetached {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateDetached}
		if current.Kind == StateRunning || current.Kind == StateAwaitingApproval {
			effects = append(effects, Effect{Kind: EffectEndTurn, Outcome: OutcomeUnknown})
		}
	case FactResume:
		if current.Kind != StateDetached {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateStarting}
		effects = append(effects, Effect{Kind: EffectResume})
	case FactClose:
		if current.Kind == StateClosed {
			return invalidTransition(current, fact)
		}
		next = State{Kind: StateClosed}
		effects = append(effects, Effect{Kind: EffectClose})
	}
	return next, effects, nil
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
	case FactReady, FactFugueSend, FactVendorTurn, FactApprovalRequested, FactApprovalGranted,
		FactApprovalDenied, FactTurnCompleted, FactTurnInterrupted,
		FactUnlocked, FactProcessExited, FactHostRestarted, FactResume, FactClose:
		return fact.Reason == "" && fact.Code == ""
	case FactTurnFailed:
		return fact.Reason == "" && fact.Code != ""
	case FactTurnUnknown:
		return fact.Code == "" && (fact.Reason == "" || fact.Reason == WaitingVaultLocked)
	case FactWait:
		return fact.Code == "" && (fact.Reason == WaitingVaultLocked || fact.Reason == WaitingQuota || fact.Reason == WaitingCapacity)
	default:
		return false
	}
}
