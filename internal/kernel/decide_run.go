package kernel

import (
	"math"
	"slices"
)

func (d *decision) accept(c *AcceptRunCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.validateAcceptance(c); err != nil {
		return nil, err
	}
	if d.state.Accepted || d.state.Admitted || d.state.Finished {
		return nil, Reject(RejectionCodeInvalidState, "run already exists")
	}
	return []Event{&RunAcceptedEvent{
		Envelope: d.envelope(), InputRef: c.InputRef, SessionID: c.SessionID,
		QueueItemRef: c.QueueItemRef, ProtocolBinding: c.ProtocolBinding,
		Budgets: c.Budgets, PriorityClass: c.PriorityClass,
		PeerOutboxRef: c.PeerOutboxRef, GoalID: c.GoalID, GoalRound: c.GoalRound,
	}}, nil
}

func (d *decision) admit(c *AdmitRunCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.validateAdmission(c); err != nil {
		return nil, err
	}
	s := d.state
	if !s.Accepted || s.Admitted || s.Finished || s.StepStarted || hasActiveAttempt(s) {
		return nil, Reject(RejectionCodeInvalidState, "run cannot be admitted")
	}
	if err := d.runnable(); err != nil {
		return nil, err
	}
	admission := &RunAdmittedEvent{
		Envelope: d.envelope(), Token: c.Token, OwnerIncarnation: c.OwnerIncarnation,
		QueueDecisionRef: c.QueueDecisionRef, ContextEpoch: c.ContextEpoch,
		StartRunID: c.StartRunID, InitialContextRevision: c.InitialContextRevision,
	}
	plan, err := d.planStep(1, nil)
	if err != nil {
		return nil, err
	}
	return append([]Event{admission}, plan...), nil
}

func (d *decision) advance(c *AdvanceRunCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	if err := d.runnable(); err != nil {
		return nil, err
	}
	s := d.state
	if !s.StepCompleted || s.StepAborted || s.StepContinuation != StepContinuationContinue || hasActiveAttempt(s) ||
		c.StepIndex <= 0 || c.StepIndex-1 != s.CurrentStepIndex {
		return nil, Reject(RejectionCodeInvalidTransition, "step cannot advance")
	}
	if err := d.closedStep(); err != nil {
		return nil, err
	}
	return d.planStep(c.StepIndex, d.continuationInjection())
}

// continuationInjection returns the labelled "continue where you stopped" message
// the step after an automatic length continuation must receive, or nil. The
// host owns the text; the kernel records only that it was injected.
func (d *decision) continuationInjection() *ContextInjectedEvent {
	if d.state.StepReason != ContinuationReasonTruncatedContinue {
		return nil
	}
	return &ContextInjectedEvent{
		Envelope: Envelope{}, Tag: "continue", Source: string(ContinuationReasonTruncatedContinue),
		PayloadRef: d.facts.ContinuationPromptRef,
	}
}

func validTurnPlan(p *ModelRequestPlannedEvent) bool {
	return p != nil && p.InvocationID != "" && p.FirstAttemptID != "" && p.Purpose == "turn" &&
		p.ModelRef != "" && p.ContextRef != "" && p.EstimatedBudgetTokens >= 0
}

func (d *decision) planStep(index int, injected *ContextInjectedEvent) ([]Event, error) {
	p := d.facts.ModelPlan
	if index <= 0 || !validTurnPlan(p) {
		return nil, Reject(RejectionCodeCommandRejected, "invalid model plan")
	}
	if d.invocationExists(p.InvocationID) || attemptExists(d.state, p.FirstAttemptID) {
		return nil, Reject(RejectionCodeDuplicateAttempt, "reused model identity")
	}
	if injected != nil && injected.PayloadRef == "" {
		return nil, Reject(RejectionCodeCommandRejected, "missing continuation prompt")
	}
	plan := *p
	step := &StepStartedEvent{Envelope: d.envelope(), StepIndex: index, ContextRefs: slices.Clone(d.facts.ContextRefs)}
	events := []Event{step}
	if injected != nil {
		injection := *injected
		injection.Envelope = d.envelope()
		events = append(events, &injection)
	}
	plan.Envelope = d.envelope()
	plan.ToolDeclarationRefs = slices.Clone(p.ToolDeclarationRefs)
	return append(events, &plan), nil
}

// stepOutcome gathers the recorded facts AfterStep decides on. The caller passes
// what only the current response or tool batch knows.
func (d *decision) stepOutcome(finish FinishReason, toolCalls int) StepOutcome {
	allTerminate := toolCalls > 0
	for _, id := range d.state.ToolOrder {
		if t := d.state.ToolInvocations[id]; t == nil || !t.Plan.Terminates {
			allTerminate = false
		}
	}
	return StepOutcome{
		FinishReason: finish, ToolCalls: toolCalls, AllTerminate: allTerminate,
		SteerPending: d.facts.SteerPending, StopIntent: false, AutoContinued: d.state.AutoContinued,
		StepsUsed: d.state.StepsUsed, StepsLimit: d.state.BudgetLimit(BudgetKindSteps),
	}
}

// closeStep records the step completion and what the decision implies: the
// run finishing, or the run waiting on the step budget. A continuing step
// records nothing more; the caller plans the next step.
func (d *decision) closeStep(dec *StepDecision, responseRef string, resultRefs []string) []Event {
	continuation := StepContinuationEnd
	if dec.Continue || dec.Reason == ContinuationReasonBudget {
		continuation = StepContinuationContinue
	}
	events := []Event{&StepCompletedEvent{
		Envelope: d.envelope(), StepIndex: d.state.CurrentStepIndex,
		ResponseRef: responseRef, OrderedResultRefs: resultRefs, Continuation: continuation,
		Reason: dec.Reason, BudgetKind: dec.BudgetKind,
	}}
	switch {
	case dec.Reason == ContinuationReasonBudget:
		return append(events, d.budgetBlock(dec.BudgetKind))
	case dec.Outcome != "":
		return append(events, &RunFinishedEvent{
			Envelope: d.envelope(), Outcome: dec.Outcome, Truncated: dec.Truncated, FailureCode: dec.FailureCode,
			PrimaryStopRef: "", SecondaryStopRefs: nil, UnresolvedExposureRefs: nil,
		})
	}
	return events
}

func (d *decision) validateAdmission(c *AdmitRunCommand) error {
	e := d.next
	if c.CommandID != e.CommandID || c.RunID != e.RunID || c.Origin != e.Origin ||
		c.Token == 0 || c.Token != e.Token {
		return Reject(RejectionCodeCommandRejected, "admission identity mismatch")
	}
	if c.OwnerIncarnation == 0 || c.OwnerIncarnation != d.facts.OwnerIncarnation ||
		c.QueueDecisionRef == "" || c.StartRunID == "" || c.ContextEpoch == 0 || c.InitialContextRevision == 0 {
		return Reject(RejectionCodeCommandRejected, "invalid admission evidence")
	}
	return nil
}

func (d *decision) validateAcceptance(c *AcceptRunCommand) error {
	e := d.next
	if c.CommandID != e.CommandID || c.RunID != e.RunID || c.Origin != e.Origin || e.Token != 0 ||
		c.InputRef == "" || c.SessionID == "" || c.QueueItemRef == "" || c.ProtocolBinding == "" || !c.Budgets.valid() {
		return Reject(RejectionCodeCommandRejected, "invalid acceptance")
	}
	return nil
}

func (d *decision) closedStep() error {
	inv := d.state.ModelInvocations[d.state.CurrentModelInvocationID]
	if inv == nil || !inv.Completed || inv.Attempts[inv.SelectedAttemptID] == nil {
		return Reject(RejectionCodeInvalidState, "step has no selected response")
	}
	_, err := d.completedToolRefs("", "")
	return err
}

func (d *decision) nextStepIndex() (int, error) {
	index := d.state.CurrentStepIndex
	if index < 1 || index == math.MaxInt {
		return 0, Reject(RejectionCodeInvalidState, "invalid step cursor")
	}
	return index + 1, nil
}

func (d *decision) invocationExists(id string) bool {
	_, model := d.state.ModelInvocations[id]
	_, tool := d.state.ToolInvocations[id]
	return model || tool
}
