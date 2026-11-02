package kernel

import "slices"

// CheckBatch checks a proposed committed batch against its preceding state and
// the host observations used to decide it. Checkpoints are checked only after the
// whole batch; transition guards use event order. Facts must come from the host,
// never the model. This audit neither grants authority nor replaces Decide/gates.
func CheckBatch(before *State, events []Event, facts *DecisionFacts) []InvariantID {
	if before == nil {
		before = NewState()
	}
	failed := make(map[InvariantID]bool)
	for _, id := range CheckInvariants(before) {
		failed[id] = true
	}
	if len(failed) > 0 {
		return invariantIDs(failed)
	}
	checkRetryBatches(events, failed)
	state := before
	for _, e := range events {
		if isNilEvent(e) {
			failed[SM09] = true
			continue
		}
		checkEventMetadata(state, e, facts, failed)
		checkFrontierTransition(state, e, failed)
		checkAttemptTransition(state, e, facts, failed)
		checkAdditionalTransition(state, e, facts, failed)
		state = Evolve(state, e)
	}
	for _, id := range CheckInvariants(state) {
		failed[id] = true
	}
	checkExposurePreserved(before, state, events, failed)
	return invariantIDs(failed)
}

func checkEventMetadata(s *State, e Event, f *DecisionFacts, bad map[InvariantID]bool) {
	env := e.EventEnvelope()
	if !batchMetadataPresent(&env) || env.Sequence != s.LastSequence+1 ||
		(s.RunID != "" && env.RunID != s.RunID) {
		bad[SM09] = true
	}
	if f == nil || env.CommandID != f.Envelope.CommandID || env.Origin != f.Envelope.Origin {
		bad[SM09] = true
	}
}

func checkFrontierTransition(s *State, e Event, bad map[InvariantID]bool) {
	switch ev := e.(type) {
	case *StepStartedEvent:
		if ev.StepIndex <= 0 || ev.StepIndex-1 != s.CurrentStepIndex {
			bad[SM07] = true
		}
		if s.StepStarted && (!s.StepCompleted && !s.StepAborted) {
			bad[SM07] = true
		}
	case *StepCompletedEvent:
		if !stepPairsMatch(s, ev) {
			bad[SM07] = true
		}
	}
	checkDispatchFrontier(s, e, bad)
}

func checkDispatchFrontier(s *State, e Event, bad map[InvariantID]bool) {
	switch ev := e.(type) {
	case *ModelDispatchStartedEvent:
		if !s.Admitted {
			bad[SM12] = true
		}
		if ev.InvocationID != s.CurrentModelInvocationID || !s.StepStarted || s.StepCompleted || s.StepAborted {
			bad[SM07] = true
		}
		if !dispatchAllowed(s) {
			bad[SM10] = true
		}
	case *ToolDispatchStartedEvent:
		if !s.Admitted {
			bad[SM12] = true
		}
		if !toolAtFrontier(s, ev.ToolCallID) {
			bad[SM07] = true
		}
		if !dispatchAllowed(s) {
			bad[SM10] = true
		}
	}
}

func dispatchAllowed(s *State) bool {
	return !s.Finished && s.StopIntent == nil && len(s.InterruptionCauses) == 0 &&
		len(s.Blocks) == 0 && len(s.QuiescingAttempts) == 0 && len(DeriveWaitReasons(s)) == 0 &&
		s.ContextRequirement == ContextRequirementNone
}

func toolAtFrontier(s *State, id string) bool {
	for _, key := range s.ToolOrder {
		t := s.ToolInvocations[key]
		if t == nil {
			return false
		}
		if !t.Closed {
			return key == id
		}
	}
	return false
}

func stepPairsMatch(s *State, e *StepCompletedEvent) bool {
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	if !hasOpenStep(s) || inv == nil || !inv.Completed || e.StepIndex != s.CurrentStepIndex || len(e.OrderedResultRefs) != len(s.ToolOrder) {
		return false
	}
	if !selectedResponseMatches(inv, e.ResponseRef) {
		return false
	}
	for i, id := range s.ToolOrder {
		t := s.ToolInvocations[id]
		if t == nil || !t.Closed {
			return false
		}
		result := t.Attempts[t.SelectedAttemptID]
		if result == nil || result.Result.ResultRef != e.OrderedResultRefs[i] {
			return false
		}
	}
	return true
}

func checkAttemptTransition(s *State, e Event, f *DecisionFacts, bad map[InvariantID]bool) {
	switch ev := e.(type) {
	case *ModelAttemptPlannedEvent:
		checkModelRetry(s, ev, bad)
	case *ToolAttemptPlannedEvent:
		t := s.ToolInvocations[ev.ToolCallID]
		if t == nil || t.Attempts[ev.PreviousAttemptID] == nil || attemptExists(s, ev.NewAttemptID) || ev.NewAttemptID == "" {
			bad[SM08] = true
		}
	case *ModelDispatchStartedEvent:
		checkDispatchFacts(s, ev.Token, ev.ExecutorFence, f, bad)
		checkModelDispatch(s, ev, bad)
	case *ToolDispatchStartedEvent:
		checkDispatchFacts(s, ev.Token, ev.ExecutorFence, f, bad)
		checkToolDispatch(s, ev, bad)
	case *ModelResponseRecordedEvent:
		checkModelCompletion(s, ev, f, bad)
	case *ToolResultRecordedEvent:
		checkToolCompletion(s, ev, f, bad)
	}
}

func checkDispatchFacts(s *State, token, fence uint64, f *DecisionFacts, bad map[InvariantID]bool) {
	if f == nil {
		bad[SM09] = true
		bad[SM10] = true
		return
	}
	if token == 0 || token != s.AdmissionToken || f.OwnerIncarnation == 0 || f.OwnerIncarnation != s.OwnerIncarnation || fence == 0 || fence != f.ExecutorFence {
		bad[SM09] = true
	}
	if f.WorkerReservationRef == "" || f.DispatchAuthorizationRef == "" {
		bad[SM10] = true
	}
}

func checkModelRetry(s *State, e *ModelAttemptPlannedEvent, bad map[InvariantID]bool) {
	inv := s.ModelInvocations[e.InvocationID]
	if inv == nil || inv.Attempts[e.PreviousAttemptID] == nil || e.AttemptID == "" || attemptExists(s, e.AttemptID) {
		bad[SM08] = true
		return
	}
	old := inv.Attempts[e.PreviousAttemptID]
	if old.Status != ModelAttemptStatusFailed && old.Status != ModelAttemptStatusAbandonedUnknown {
		bad[SM08] = true
	}
}

func completionOwner(s *State, env *Envelope, fence uint64, f *DecisionFacts) bool {
	return f != nil && env.Token != 0 && env.Token == s.AdmissionToken &&
		f.OwnerIncarnation != 0 && f.OwnerIncarnation == s.OwnerIncarnation && fence != 0 && fence == f.ExecutorFence
}

func checkModelCompletion(s *State, e *ModelResponseRecordedEvent, f *DecisionFacts, bad map[InvariantID]bool) {
	inv := s.ModelInvocations[e.InvocationID]
	if inv == nil || inv.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	a := inv.Attempts[e.AttemptID]
	if a.OwnerIncarnation != s.OwnerIncarnation || a.Dispatch.Token != e.Envelope.Token || inv.Completed || (a.Status != ModelAttemptStatusRequesting && a.Status != ModelAttemptStatusStreaming) ||
		!completionOwner(s, &e.Envelope, a.Dispatch.ExecutorFence, f) {
		bad[SM09] = true
	}
}

func checkToolCompletion(s *State, e *ToolResultRecordedEvent, f *DecisionFacts, bad map[InvariantID]bool) {
	t := s.ToolInvocations[e.ToolCallID]
	if t == nil || t.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	a := t.Attempts[e.AttemptID]
	if t.Closed {
		bad[SM09] = true
	}
	if !currentObservationOwner(s, &e.Envelope, f) {
		bad[SM09] = true
	}
	if needsWorkerEvidence(a, e) && !rawToolResultMatches(s, a, e, f) && !reconciledToolResultMatches(s, a, e) {
		bad[SM09] = true
	}
	if e.Outcome == ToolResultOutcomeAbandonedUnknown && !a.ExposureUnknown {
		bad[SM11] = true
	}
}

func checkExposurePreserved(before, after *State, events []Event, bad map[InvariantID]bool) {
	for id, t := range before.ToolInvocations {
		if t == nil {
			continue
		}
		for attempt, a := range t.Attempts {
			if a == nil || !a.ExposureUnknown || reconciledIn(events, id, attempt) {
				continue
			}
			next := after.ToolInvocations[id]
			if next == nil || next.Attempts[attempt] == nil || !next.Attempts[attempt].ExposureUnknown {
				bad[SM11] = true
			}
		}
	}
}

func reconciledIn(events []Event, id, attempt string) bool {
	return slices.ContainsFunc(events, func(e Event) bool {
		r, ok := e.(*ToolReconciledEvent)
		return ok && r != nil && r.ToolCallID == id && r.AttemptID == attempt && r.OperationReceipt != ""
	})
}

func batchMetadataPresent(e *Envelope) bool {
	return e.Sequence != 0 && e.RunID != "" && e.CommandID != "" && e.Origin != "" && e.CausationID != "" && !e.Timestamp.IsZero()
}

func currentObservationOwner(s *State, e *Envelope, f *DecisionFacts) bool {
	return f != nil && f.OwnerIncarnation != 0 && f.OwnerIncarnation == s.OwnerIncarnation && e.Token != 0 && e.Token == s.AdmissionToken
}

func needsWorkerEvidence(a *ToolAttempt, e *ToolResultRecordedEvent) bool {
	return a.Status == ToolAttemptStatusDispatching || e.Outcome == ToolResultOutcomeSucceeded || e.Outcome == ToolResultOutcomeFailed
}

func rawToolResultMatches(s *State, a *ToolAttempt, e *ToolResultRecordedEvent, f *DecisionFacts) bool {
	return a.OwnerIncarnation == s.OwnerIncarnation && a.Dispatch.Token == e.Envelope.Token &&
		a.Status == ToolAttemptStatusDispatching && completionOwner(s, &e.Envelope, a.Dispatch.ExecutorFence, f)
}

func reconciledToolResultMatches(s *State, a *ToolAttempt, e *ToolResultRecordedEvent) bool {
	r := a.Reconciliation
	if r.OperationReceipt == "" || r.Envelope.Token != s.AdmissionToken {
		return false
	}
	return (a.Status == ToolAttemptStatusSucceeded && e.Outcome == ToolResultOutcomeSucceeded) ||
		(a.Status == ToolAttemptStatusFailed && e.Outcome == ToolResultOutcomeFailed)
}

func hasOpenStep(s *State) bool {
	return s.StepStarted && !s.StepCompleted && !s.StepAborted
}

// InvariantBatch carries a committed transaction and the host observations used
// to validate its provenance. It must not be reconstructed from model output.
type InvariantBatch struct {
	Events []Event
	Facts  DecisionFacts
}

// CheckHistory validates every batch from the empty state. A snapshot alone
// cannot prove historical invariants. It stops before evolving an invalid batch.
func CheckHistory(history []InvariantBatch) []InvariantID {
	s := NewState()
	for i := range history {
		b := &history[i]
		if ids := CheckBatch(s, b.Events, &b.Facts); len(ids) > 0 {
			return ids
		}
		for _, e := range b.Events {
			s = Evolve(s, e)
		}
	}
	return nil
}

func selectedResponseMatches(inv *ModelInvocation, ref string) bool {
	a := inv.Attempts[inv.SelectedAttemptID]
	return a != nil && a.Response.ResponseRef == ref
}
