package kernel

import "slices"

func modelStructureValid(s *State) bool {
	for _, inv := range s.ModelInvocations {
		if inv == nil {
			return false
		}
		for _, a := range inv.Attempts {
			if a == nil {
				return false
			}
		}
	}
	return true
}

func toolStructureValid(s *State) bool {
	for _, t := range s.ToolInvocations {
		if t == nil {
			return false
		}
		for _, a := range t.Attempts {
			if a == nil {
				return false
			}
		}
	}
	return true
}

// blocksValid rejects a block whose wait reason is unknown (a retired reason
// such as "dependency" included) or whose budget kind is missing or misplaced.
func blocksValid(s *State) bool {
	for _, b := range s.Blocks {
		if b == nil || !slices.Contains(canonicalWaitOrder(), b.Kind) {
			return false
		}
		if b.Kind == WaitReasonBudget && !b.BudgetKind.valid() {
			return false
		}
		if b.Kind != WaitReasonBudget && b.BudgetKind != "" {
			return false
		}
	}
	return true
}

// interactionBlocksMatch requires every approval or user_input block to reference
// exactly one pending request of the matching kind, and no request twice.
func interactionBlocksMatch(s *State) bool {
	seen := make(map[string]bool)
	for _, b := range s.Blocks {
		if b == nil || (b.Kind != WaitReasonApproval && b.Kind != WaitReasonUserInput) {
			continue
		}
		if b.Reference == "" || seen[b.Reference] || !interactionRequestExists(s, b.Reference, b.Kind) {
			return false
		}
		seen[b.Reference] = true
	}
	return true
}

func interactionRequestExists(s *State, ref string, reason WaitReason) bool {
	for _, t := range s.ToolInvocations {
		if t != nil && t.Approval.OwnerRequestRef == ref && t.Approval.RequestKind.waitReason() == reason {
			return true
		}
	}
	return false
}

func approvalReferenceExists(s *State, ref string) bool {
	for _, t := range s.ToolInvocations {
		if t != nil && t.Approval.OwnerRequestRef == ref {
			return true
		}
	}
	return false
}

func modelDisposed(inv *ModelInvocation) bool {
	if inv == nil {
		return false
	}
	if inv.Completed {
		a := inv.Attempts[inv.SelectedAttemptID]
		return a != nil && a.Status == ModelAttemptStatusCompleted && a.Response.ResponseRef != ""
	}
	if !inv.Closed {
		return false
	}
	for _, a := range inv.Attempts {
		if a == nil || (a.Status != ModelAttemptStatusFailed && a.Status != ModelAttemptStatusCancelled && a.Status != ModelAttemptStatusAbandonedUnknown) {
			return false
		}
	}
	return len(inv.Attempts) > 0
}

func toolDisposed(t *ToolInvocation) bool {
	if t == nil || !t.Closed {
		return false
	}
	a := t.Attempts[t.SelectedAttemptID]
	return a != nil && a.Result.ResultRef != "" && toolTerminalStatus(a.Status)
}

func toolTerminalStatus(status ToolAttemptStatus) bool {
	switch status {
	case ToolAttemptStatusSucceeded, ToolAttemptStatusFailed, ToolAttemptStatusDenied, ToolAttemptStatusRefused, ToolAttemptStatusCancelled, ToolAttemptStatusAbandonedUnknown:
		return true
	case ToolAttemptStatusPlanned, ToolAttemptStatusChecking, ToolAttemptStatusAwaitingApproval, ToolAttemptStatusReady, ToolAttemptStatusDispatching, ToolAttemptStatusOutcomeUnknown:
		return false
	}
	return false
}

func unknownExposureRetained(s *State) bool {
	for _, t := range s.ToolInvocations {
		if t == nil {
			continue
		}
		for _, a := range t.Attempts {
			if a != nil && a.Status == ToolAttemptStatusAbandonedUnknown && !a.ExposureUnknown {
				return false
			}
		}
	}
	return true
}

func checkModelDispatch(s *State, e *ModelDispatchStartedEvent, bad map[InvariantID]bool) {
	inv := s.ModelInvocations[e.InvocationID]
	if inv == nil || inv.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	if inv.Completed || inv.Closed || inv.Attempts[e.AttemptID].Status != ModelAttemptStatusPlanned {
		bad[SM09] = true
	}
}

func checkToolDispatch(s *State, e *ToolDispatchStartedEvent, bad map[InvariantID]bool) {
	t := s.ToolInvocations[e.ToolCallID]
	if t == nil || t.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	if t.Closed || t.Attempts[e.AttemptID].Status != ToolAttemptStatusReady || t.CheckGeneration != e.CheckGeneration {
		bad[SM09] = true
	}
}

func checkAdditionalTransition(s *State, e Event, f *DecisionFacts, bad map[InvariantID]bool) {
	switch ev := e.(type) {
	case *ModelRequestPlannedEvent:
		checkModelPlan(s, ev, bad)
	case *ToolCallPlannedEvent:
		checkToolPlan(s, ev, bad)
	case *ToolApprovalRequestedEvent:
		checkApprovalRequest(s, ev, bad)
	case *ToolApprovalResolvedEvent:
		checkApprovalResolution(s, ev, bad)
	case *ToolReadyRecordedEvent:
		checkReadyEvidence(s, ev, bad)
	case *ModelStreamStartedEvent:
		checkStreamOwner(s, ev, f, bad)
	case *ModelRequestFailedEvent:
		checkFailureOwner(s, ev, f, bad)
	}
}

func checkApprovalRequest(s *State, e *ToolApprovalRequestedEvent, bad map[InvariantID]bool) {
	t := s.ToolInvocations[e.ToolCallID]
	if t == nil || t.Approval.ToolCallID != "" || t.CheckGeneration != e.CheckGeneration {
		bad[SM02] = true
	}
	if approvalReferenceExists(s, e.OwnerRequestRef) {
		bad[SM02] = true
	}
}

func checkStreamOwner(s *State, e *ModelStreamStartedEvent, f *DecisionFacts, bad map[InvariantID]bool) {
	inv := s.ModelInvocations[e.InvocationID]
	if inv == nil || inv.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	a := inv.Attempts[e.AttemptID]
	if a.OwnerIncarnation != s.OwnerIncarnation || a.Dispatch.Token != e.Envelope.Token || a.Status != ModelAttemptStatusRequesting || !completionOwner(s, &e.Envelope, a.Dispatch.ExecutorFence, f) {
		bad[SM09] = true
	}
}

func checkFailureOwner(s *State, e *ModelRequestFailedEvent, f *DecisionFacts, bad map[InvariantID]bool) {
	inv := s.ModelInvocations[e.InvocationID]
	if inv == nil || inv.Attempts[e.AttemptID] == nil {
		bad[SM09] = true
		return
	}
	a := inv.Attempts[e.AttemptID]
	if e.Stage == "pre_dispatch" && a.Status == ModelAttemptStatusPlanned {
		if !currentObservationOwner(s, &e.Envelope, f) {
			bad[SM09] = true
		}
		return
	}
	if a.Status != ModelAttemptStatusRequesting && a.Status != ModelAttemptStatusStreaming {
		bad[SM09] = true
	}
	if a.OwnerIncarnation != s.OwnerIncarnation || a.Dispatch.Token != e.Envelope.Token || !completionOwner(s, &e.Envelope, a.Dispatch.ExecutorFence, f) {
		bad[SM09] = true
	}
}

func checkApprovalResolution(s *State, e *ToolApprovalResolvedEvent, bad map[InvariantID]bool) {
	t := s.ToolInvocations[e.ToolCallID]
	if t == nil || t.Approval.ToolCallID == "" || t.Approval.CheckGeneration != e.CheckGeneration || e.OwnerResolutionEvidence == "" {
		bad[SM02] = true
	}
}

func checkReadyEvidence(s *State, e *ToolReadyRecordedEvent, bad map[InvariantID]bool) {
	t := s.ToolInvocations[e.ToolCallID]
	if t == nil || t.CheckGeneration != e.CheckGeneration || e.ValidatedArgsRef == "" {
		bad[SM02] = true
		return
	}
	q := t.LastApproval
	if q.ToolCallID != "" && q.CheckGeneration == e.CheckGeneration && !resolvedApprovalMatches(t, e) {
		bad[SM02] = true
	}
}

func resolvedApprovalMatches(t *ToolInvocation, e *ToolReadyRecordedEvent) bool {
	r := t.LastResolution
	return r.CheckGeneration == e.CheckGeneration && r.Resolution == "approved" && r.OwnerResolutionEvidence != "" &&
		e.OneUseApprovalReceiptRef != "" && e.ValidatedArgsRef == t.LastApproval.ExactArgsRef
}

func checkModelPlan(s *State, e *ModelRequestPlannedEvent, bad map[InvariantID]bool) {
	exists := invariantInvocationExists(s, e.InvocationID)
	if exists || attemptExists(s, e.FirstAttemptID) {
		bad[SM08] = true
	}
	if !hasOpenStep(s) || s.CurrentModelInvocationID != "" {
		bad[SM07] = true
	}
}

func checkToolPlan(s *State, e *ToolCallPlannedEvent, bad map[InvariantID]bool) {
	if invariantInvocationExists(s, e.ToolCallID) || attemptExists(s, e.FirstAttemptID) {
		bad[SM08] = true
	}
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	if inv == nil || !inv.Completed || !hasOpenStep(s) || e.SourcePosition != len(s.ToolOrder) {
		bad[SM07] = true
	}
}

func invariantInvocationExists(s *State, id string) bool {
	_, model := s.ModelInvocations[id]
	_, tool := s.ToolInvocations[id]
	return model || tool
}
