package kernel

import "math"

// DecisionFacts are host-bound observations, never model/client authority claims.
// The coordinator must validate these against owners and reserve a worker before
// dispatch; execute still revalidates through the owning gateway. Plans contain
// protected refs only. Their envelopes are replaced with this batch's metadata.
// Durable command-ID receipts belong to codec/execstore (F2.3/F2.4), not this reducer.
type DecisionFacts struct {
	Envelope                 Envelope
	OwnerIncarnation         uint64
	ExecutorFence            uint64
	WorkerReservationRef     string
	BudgetReservationRef     string
	DispatchAuthorizationRef string
	ModelPlan                *ModelRequestPlannedEvent
	ToolPlans                []*ToolCallPlannedEvent
	ModelDispatch            *ModelDispatchStartedEvent
	Usage                    *BudgetObservedEvent
	ContextRefs              []string
}

type decision struct {
	state *State
	facts *DecisionFacts
	next  Envelope
}

// Decide returns a whole transaction batch, a nil batch for an already-observed
// transition, or a rejection. It neither mutates inputs nor dispatches effects.
// Only Trace A commands are supported here; recovery/stop/retry belong to F3.
func Decide(s *State, command Command, facts *DecisionFacts) ([]Event, error) {
	if facts == nil {
		return nil, Reject(RejectionCodeCommandRejected, "missing facts")
	}
	if s == nil {
		s = NewState()
	}
	d := &decision{state: s, facts: facts, next: facts.Envelope}
	if err := d.validateEnvelope(); err != nil {
		return nil, err
	}
	return d.command(command)
}

func (d *decision) validateEnvelope() error {
	e := d.next
	if e.RunID == "" || e.CommandID == "" || e.Origin == "" || e.CausationID == "" || e.Timestamp.IsZero() {
		return Reject(RejectionCodeCommandRejected, "missing command metadata")
	}
	// Reserve enough sequence space for any supported batch before adding events.
	if d.state.LastSequence > math.MaxUint64-4 ||
		uint64(len(d.facts.ToolPlans)) > math.MaxUint64-d.state.LastSequence-2 || e.Sequence != d.state.LastSequence+1 {
		return Reject(RejectionCodeInvalidState, "invalid batch sequence")
	}
	if d.state.RunID != "" && e.RunID != d.state.RunID {
		return Reject(RejectionCodeCommandRejected, "wrong run")
	}
	return nil
}

func (d *decision) envelope() Envelope {
	e := d.next
	d.next.Sequence++
	return e
}

func (d *decision) command(command Command) ([]Event, error) {
	switch c := command.(type) {
	case *AcceptRunCommand:
		return d.accept(c)
	case *AdmitRunCommand:
		return d.admit(c)
	case *AdvanceRunCommand:
		return d.advance(c)
	case *PrepareDispatchCommand:
		return d.prepare(c)
	case *ObserveModelStreamCommand:
		return d.stream(c)
	case *ObserveModelResponseCommand:
		return d.response(c)
	case *ObserveToolCheckCommand:
		return d.check(c)
	case *ObserveToolResultCommand:
		return d.result(c)
	}
	return nil, Reject(RejectionCodeUnrecognizedCommand, "unsupported or nil command")
}

func (d *decision) target(id, run, origin string, token uint64) error {
	e := d.next
	if id != e.CommandID || run != e.RunID || origin != e.Origin {
		return Reject(RejectionCodeCommandRejected, "command metadata mismatch")
	}
	if !d.state.Admitted {
		return Reject(RejectionCodeRunNotAdmitted, "run not admitted")
	}
	if token == 0 || token != d.state.AdmissionToken || e.Token != token ||
		d.facts.OwnerIncarnation == 0 || d.facts.OwnerIncarnation != d.state.OwnerIncarnation {
		return Reject(RejectionCodeStaleToken, "stale ownership")
	}
	return nil
}

func (d *decision) runnable() error {
	s := d.state
	if s.Finished {
		return Reject(RejectionCodeRunAlreadyFinished, "run finished")
	}
	if s.StopIntent != nil {
		return Reject(RejectionCodeStopRequested, "stop pending")
	}
	// Unknown/nil dependencies fail closed as well; derived display reasons are
	// deliberately insufficient for an authorization decision.
	if len(s.Blocks) != 0 || len(s.ActiveTimers) != 0 || len(s.InterruptionCauses) != 0 ||
		len(s.QuiescingAttempts) != 0 || s.ContextRequirement != ContextRequirementNone {
		return Reject(RejectionCodeWaitDependencyActive, "unresolved dependency")
	}
	return nil
}

func (d *decision) activeStep() error {
	s := d.state
	if !s.StepStarted || s.StepCompleted || s.StepAborted {
		return Reject(RejectionCodeStepNotActive, "no open step")
	}
	return nil
}
