package kernel

func (d *decision) tool(id, attempt string) (*ToolInvocation, *ToolAttempt, error) {
	t := d.state.ToolInvocations[id]
	if id == "" || attempt == "" || t == nil || t.Attempts[attempt] == nil {
		return nil, nil, Reject(RejectionCodeAttemptNotActive, "unknown tool attempt")
	}
	return t, t.Attempts[attempt], nil
}

func (d *decision) frontier(id string) error {
	inv := d.state.ModelInvocations[d.state.CurrentModelInvocationID]
	if inv == nil || !inv.Completed {
		return Reject(RejectionCodeInvalidTransition, "no selected response")
	}
	for _, toolID := range d.state.ToolOrder {
		t := d.state.ToolInvocations[toolID]
		if t == nil {
			return Reject(RejectionCodeInvalidState, "missing tool")
		}
		if !t.Closed {
			if toolID == id {
				return nil
			}
			break
		}
	}
	return Reject(RejectionCodeInvalidTransition, "tool outside frontier")
}

func (d *decision) prepareTool(c *PrepareDispatchCommand) ([]Event, error) {
	t, att, err := d.tool(c.InvocationID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if err = d.frontier(c.InvocationID); err != nil {
		return nil, err
	}
	if t.Closed || t.EffectClass != EffectClassPureRead || t.CheckGeneration == 0 {
		return nil, Reject(RejectionCodeInvalidTransition, "tool unavailable")
	}
	switch att.Status {
	case ToolAttemptStatusPlanned:
		if hasActiveAttempt(d.state) {
			return nil, Reject(RejectionCodeInvalidTransition, "worker already active")
		}
		return []Event{&ToolCheckStartedEvent{
			Envelope: d.envelope(), ToolCallID: c.InvocationID,
			AttemptID: c.AttemptID, CheckGeneration: t.CheckGeneration, FrontierCursor: t.SourcePosition,
		}}, nil
	case ToolAttemptStatusReady:
		return d.dispatchTool(c, t)
	case ToolAttemptStatusChecking, ToolAttemptStatusAwaitingApproval, ToolAttemptStatusDispatching,
		ToolAttemptStatusSucceeded, ToolAttemptStatusFailed, ToolAttemptStatusDenied, ToolAttemptStatusRefused,
		ToolAttemptStatusCancelled, ToolAttemptStatusOutcomeUnknown, ToolAttemptStatusAbandonedUnknown:
		return nil, Reject(RejectionCodeInvalidTransition, "tool cannot prepare")
	}
	return nil, Reject(RejectionCodeInvalidState, "unknown tool status")
}

func (d *decision) dispatchTool(c *PrepareDispatchCommand, t *ToolInvocation) ([]Event, error) {
	if hasActiveAttempt(d.state) {
		return nil, Reject(RejectionCodeInvalidTransition, "worker already active")
	}
	if d.budgetExhausted(BudgetKindToolCalls) {
		// The tools of the step stay Ready or Planned; an extension resumes them in this step.
		return []Event{d.budgetBlock(BudgetKindToolCalls)}, nil
	}
	if err := d.dispatchEvidence(); err != nil {
		return nil, err
	}
	return []Event{&ToolDispatchStartedEvent{
		Envelope: d.envelope(), ToolCallID: c.InvocationID,
		AttemptID: c.AttemptID, CheckGeneration: t.CheckGeneration, ExecutorFence: d.facts.ExecutorFence,
		Token: c.Token, ReceiptRef: d.facts.DispatchAuthorizationRef, BudgetReservation: d.facts.BudgetReservationRef,
	}}, nil
}

func (d *decision) check(c *ObserveToolCheckCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	t, att, err := d.tool(c.ToolCallID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if err = validateToolCheck(c, t); err != nil {
		return nil, err
	}
	e := &ToolReadyRecordedEvent{
		Envelope: t.Ready.Envelope, ToolCallID: c.ToolCallID,
		CheckGeneration: c.CheckGeneration, ValidatedArgsRef: c.ValidatedArgsRef,
		GateObservation: c.GateObservation, OneUseApprovalReceiptRef: c.ApprovalReceiptRef,
	}
	if att.Status == ToolAttemptStatusReady && *e == t.Ready {
		return nil, nil
	}
	if err = d.runnable(); err != nil {
		return nil, err
	}
	if err = d.frontier(c.ToolCallID); err != nil {
		return nil, err
	}
	if att.Status != ToolAttemptStatusChecking {
		return nil, Reject(RejectionCodeInvalidTransition, "tool not checking")
	}
	e.Envelope = d.envelope()
	return []Event{e}, nil
}

func (d *decision) result(c *ObserveToolResultCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	t, att, err := d.tool(c.ToolCallID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if d.facts.ExecutorFence == 0 || d.facts.ExecutorFence != att.Dispatch.ExecutorFence {
		return nil, Reject(RejectionCodeStaleToken, "stale tool executor")
	}
	e := &ToolResultRecordedEvent{
		Envelope: att.Result.Envelope, ToolCallID: c.ToolCallID,
		AttemptID: c.AttemptID, Outcome: ToolResultOutcome(c.Outcome), ResultRef: c.ResultRef,
		Code: c.Code, EffectEvidence: c.EffectEvidence, SourcePosition: c.SourcePosition,
	}
	if t.Closed && *e == att.Result {
		return nil, nil
	}
	if err = d.runnable(); err != nil {
		return nil, err
	}
	if err = d.frontier(c.ToolCallID); err != nil {
		return nil, err
	}
	if err = d.validateToolResult(c, t, att); err != nil {
		return nil, err
	}
	return d.recordResult(c, t, e)
}

// recordResult commits a validated result, followed by a loop reminder when one
// is due, and closes the step when the result is the last of the batch.
func (d *decision) recordResult(c *ObserveToolResultCommand, t *ToolInvocation, e *ToolResultRecordedEvent) ([]Event, error) {
	e.Envelope = d.envelope()
	reminder, err := d.loopReminder(c.ToolCallID)
	if err != nil {
		return nil, err
	}
	batch := append([]Event{e}, reminder...)
	if t.SourcePosition+1 < len(d.state.ToolOrder) {
		return batch, nil
	}
	return d.finishToolBatch(c, batch)
}

func (d *decision) finishToolBatch(c *ObserveToolResultCommand, batch []Event) ([]Event, error) {
	refs, err := d.completedToolRefs(c.ToolCallID, c.ResultRef)
	if err != nil {
		return nil, err
	}
	inv := d.state.ModelInvocations[d.state.CurrentModelInvocationID]
	response := inv.Attempts[inv.SelectedAttemptID]
	if response == nil {
		return nil, Reject(RejectionCodeInvalidState, "missing selected response")
	}
	dec := AfterStep(d.stepOutcome(response.Response.FinishReason, d.recordedPlans()))
	if dec.Continue && d.budgetExhausted(BudgetKindTokens) {
		// No AdvanceRun separates this step from the next, so the step boundary is checked here.
		dec = StepDecision{Reason: ContinuationReasonBudget, BudgetKind: BudgetKindTokens}
	}
	batch = append(batch, d.closeStep(&dec, response.Response.ResponseRef, refs)...)
	if !dec.Continue {
		return batch, nil
	}
	index, err := d.nextStepIndex()
	if err != nil {
		return nil, err
	}
	plan, err := d.planStep(index, nil)
	if err != nil {
		return nil, err
	}
	return append(batch, plan...), nil
}

func validateToolCheck(c *ObserveToolCheckCommand, t *ToolInvocation) error {
	if c.CheckGeneration == 0 || c.CheckGeneration != t.CheckGeneration {
		return Reject(RejectionCodeStaleToken, "stale tool check")
	}
	if c.ValidatedArgsRef == "" || c.GateObservation != "allowed" || c.ApprovalReceiptRef != "" {
		return Reject(RejectionCodeCommandRejected, "Trace A requires an allowed check without approval")
	}
	return nil
}

func (d *decision) validateToolResult(c *ObserveToolResultCommand, t *ToolInvocation, att *ToolAttempt) error {
	if t.SourcePosition < 0 || t.SourcePosition >= len(d.state.ToolOrder) || d.state.ToolOrder[t.SourcePosition] != c.ToolCallID {
		return Reject(RejectionCodeInvalidState, "invalid tool cursor")
	}
	if att.Status != ToolAttemptStatusDispatching || c.SourcePosition != t.SourcePosition ||
		c.ResultRef == "" || c.EffectEvidence == "" || (ToolResultOutcome(c.Outcome) != ToolResultOutcomeSucceeded && ToolResultOutcome(c.Outcome) != ToolResultOutcomeFailed) {
		return Reject(RejectionCodeInvalidTransition, "invalid tool completion")
	}
	return nil
}
