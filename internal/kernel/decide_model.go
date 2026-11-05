package kernel

import "slices"

func (d *decision) model(invocation, attempt string) (*ModelInvocation, *ModelAttempt, error) {
	inv := d.state.ModelInvocations[invocation]
	if invocation == "" || attempt == "" || inv == nil || inv.Attempts[attempt] == nil {
		return nil, nil, Reject(RejectionCodeAttemptNotActive, "unknown model attempt")
	}
	return inv, inv.Attempts[attempt], nil
}

func (d *decision) prepare(c *PrepareDispatchCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	if err := d.runnable(); err != nil {
		return nil, err
	}
	if err := d.activeStep(); err != nil {
		return nil, err
	}
	if d.state.CurrentModelInvocationID == c.InvocationID {
		return d.dispatchModel(c)
	}
	return d.prepareTool(c)
}

func (d *decision) dispatchModel(c *PrepareDispatchCommand) ([]Event, error) {
	inv, att, err := d.model(c.InvocationID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if err = d.validateModelDispatch(c); err != nil {
		return nil, err
	}
	p := d.facts.ModelDispatch
	copyEvent := *p
	copyEvent.Envelope = att.Dispatch.Envelope
	if att.Status == ModelAttemptStatusRequesting && copyEvent == att.Dispatch {
		return nil, nil
	}
	if inv.Completed || inv.Closed || att.Status != ModelAttemptStatusPlanned || hasActiveAttempt(d.state) {
		return nil, Reject(RejectionCodeInvalidTransition, "model cannot dispatch")
	}
	if err = d.dispatchEvidence(); err != nil {
		return nil, err
	}
	copyEvent.Envelope = d.envelope()
	return []Event{&copyEvent}, nil
}

func (d *decision) dispatchEvidence() error {
	if d.facts.ExecutorFence == 0 || d.facts.WorkerReservationRef == "" || d.facts.DispatchAuthorizationRef == "" {
		return Reject(RejectionCodeCommandRejected, "missing current dispatch evidence")
	}
	return nil
}

func (d *decision) stream(c *ObserveModelStreamCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	inv, att, err := d.model(c.InvocationID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if err = d.modelFence(att); err != nil {
		return nil, err
	}
	if c.FirstChunkRef == "" {
		return nil, Reject(RejectionCodeCommandRejected, "missing stream evidence")
	}
	e := &ModelStreamStartedEvent{Envelope: att.Stream.Envelope, InvocationID: c.InvocationID, AttemptID: c.AttemptID, FirstChunkRef: c.FirstChunkRef}
	if att.Status == ModelAttemptStatusStreaming && *e == att.Stream {
		return nil, nil
	}
	if err = d.streamAllowed(inv, att, c.InvocationID); err != nil {
		return nil, err
	}
	e.Envelope = d.envelope()
	return []Event{e}, nil
}

func (d *decision) modelFence(att *ModelAttempt) error {
	if d.facts.ExecutorFence == 0 || d.facts.ExecutorFence != att.Dispatch.ExecutorFence {
		return Reject(RejectionCodeStaleToken, "stale executor")
	}
	return nil
}

func (d *decision) response(c *ObserveModelResponseCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	inv, att, err := d.model(c.InvocationID, c.AttemptID)
	if err != nil {
		return nil, err
	}
	if err = d.modelFence(att); err != nil {
		return nil, err
	}
	e := &ModelResponseRecordedEvent{
		Envelope: att.Response.Envelope, InvocationID: c.InvocationID,
		AttemptID: c.AttemptID, ResponseRef: c.ResponseRef, UsageStatus: c.UsageStatus, FinishReason: c.FinishReason,
	}
	if att.Status == ModelAttemptStatusCompleted {
		return d.duplicateResponse(inv, att, e)
	}
	if err = d.responseAllowed(inv, att, c.InvocationID); err != nil {
		return nil, err
	}
	if c.ResponseRef == "" || c.UsageStatus == "" {
		return nil, Reject(RejectionCodeCommandRejected, "missing response evidence")
	}
	if err = d.validateResponseFacts(c.AttemptID, c.FinishReason); err != nil {
		return nil, err
	}
	// This slice is the runnable, inference-only Trace A path. Stop/recovery
	// completion dispositions must be implemented atomically by F3, never guessed.
	if err = d.runnable(); err != nil {
		return nil, err
	}
	return d.responseEvents(e), nil
}

func (d *decision) responseEvents(e *ModelResponseRecordedEvent) []Event {
	e.Envelope = d.envelope()
	usage := *d.facts.Usage
	usage.Envelope = d.envelope()
	events := []Event{e, &usage}
	if len(d.facts.ToolPlans) == 0 {
		dec := AfterStep(d.stepOutcome(e.FinishReason, 0))
		return append(events, d.closeStep(&dec, e.ResponseRef, nil)...)
	}
	for _, p := range d.facts.ToolPlans {
		tool := *p
		tool.Envelope = d.envelope()
		events = append(events, &tool)
	}
	if e.FinishReason != FinishReasonLength {
		return events
	}
	// A cut tool call has unreliable arguments: it is never dispatched. Each one
	// gets a synthetic "not run" result before the step closes, so the model can
	// retry with whole arguments.
	for i, p := range d.facts.ToolPlans {
		events = append(events, &ToolResultRecordedEvent{
			Envelope: d.envelope(), ToolCallID: p.ToolCallID, AttemptID: p.FirstAttemptID,
			Outcome: ToolResultOutcomeRefused, ResultRef: d.facts.SyntheticResultRefs[i],
			Code: string(ContinuationReasonTruncatedArgs), EffectEvidence: effectEvidenceNotDispatched, SourcePosition: i,
		})
	}
	dec := AfterStep(d.stepOutcome(e.FinishReason, len(d.facts.ToolPlans)))
	return append(events, d.closeStep(&dec, e.ResponseRef, slices.Clone(d.facts.SyntheticResultRefs))...)
}

// effectEvidenceNotDispatched marks a result that was recorded without any attempt to run the tool.
const effectEvidenceNotDispatched = "not_dispatched"

func (d *decision) validateResponseFacts(attempt string, finish FinishReason) error {
	u := d.facts.Usage
	if u == nil || u.AttemptID != attempt || u.WindowID != "" || u.TokensUsed < 0 || u.DurationUsed != 0 || u.Basis == "" {
		return Reject(RejectionCodeCommandRejected, "invalid usage observation")
	}
	if err := d.validateFinish(finish); err != nil {
		return err
	}
	seen := make(map[string]bool)
	for i, p := range d.facts.ToolPlans {
		if err := validateToolPlan(p, i, attempt, seen); err != nil {
			return err
		}
		if d.invocationExists(p.ToolCallID) || attemptExists(d.state, p.FirstAttemptID) {
			return Reject(RejectionCodeDuplicateAttempt, "reused tool identity")
		}
		seen[p.ToolCallID], seen[p.FirstAttemptID] = true, true
	}
	return nil
}

// validateFinish requires the finish reason to agree with the tool plans the
// host observed: tool calls only come with tool_use or a cut (length) answer,
// a cut answer's calls each need a synthetic result ref, and a tool_use answer
// without calls is not a valid response.
func (d *decision) validateFinish(finish FinishReason) error {
	plans := len(d.facts.ToolPlans)
	if !finish.valid() || (plans > 0) != (finish == FinishReasonToolUse || (finish == FinishReasonLength && plans > 0)) {
		return Reject(RejectionCodeCommandRejected, "finish reason does not match tool calls")
	}
	if finish != FinishReasonLength || plans == 0 {
		return nil
	}
	if len(d.facts.SyntheticResultRefs) != plans || slices.Contains(d.facts.SyntheticResultRefs, "") {
		return Reject(RejectionCodeCommandRejected, "missing synthetic result refs")
	}
	return nil
}

func (d *decision) duplicateResponse(inv *ModelInvocation, att *ModelAttempt, response *ModelResponseRecordedEvent) ([]Event, error) {
	u := d.facts.Usage
	if u == nil {
		return nil, Reject(RejectionCodeCommandRejected, "missing duplicate usage")
	}
	usage := *u
	usage.Envelope = att.ResponseUsage.Envelope
	if *response != att.Response || usage != att.ResponseUsage || len(inv.ToolIDs) != len(d.facts.ToolPlans) {
		return nil, Reject(RejectionCodeInvalidTransition, "conflicting response")
	}
	for i, p := range d.facts.ToolPlans {
		tool := d.state.ToolInvocations[inv.ToolIDs[i]]
		if p == nil || tool == nil {
			return nil, Reject(RejectionCodeInvalidState, "missing tool plan")
		}
		plan := *p
		plan.Envelope = tool.Plan.Envelope
		if plan != tool.Plan {
			return nil, Reject(RejectionCodeInvalidTransition, "conflicting tool plan")
		}
	}
	return nil, nil
}

func (d *decision) completedToolRefs(lastTool, lastResult string) ([]string, error) {
	refs := make([]string, 0, len(d.state.ToolOrder))
	for _, id := range d.state.ToolOrder {
		if id == lastTool {
			refs = append(refs, lastResult)
			continue
		}
		t := d.state.ToolInvocations[id]
		if t == nil || !t.Closed || t.Attempts[t.SelectedAttemptID] == nil {
			return nil, Reject(RejectionCodeInvalidState, "incomplete tool batch")
		}
		refs = append(refs, t.Attempts[t.SelectedAttemptID].Result.ResultRef)
	}
	return refs, nil
}

func (d *decision) validateModelDispatch(c *PrepareDispatchCommand) error {
	p := d.facts.ModelDispatch
	if p == nil || p.AttemptID != c.AttemptID || p.InvocationID != c.InvocationID || p.Token != c.Token ||
		p.ExecutorFence == 0 || p.ExecutorFence != d.facts.ExecutorFence {
		return Reject(RejectionCodeCommandRejected, "invalid dispatch fence")
	}
	if p.ReservationTokens < 0 ||
		p.ProviderSettingsRef == "" || p.AccountID == "" || p.CatalogVersion == "" {
		return Reject(RejectionCodeCommandRejected, "invalid model dispatch facts")
	}
	return nil
}

func (d *decision) responseAllowed(inv *ModelInvocation, att *ModelAttempt, id string) error {
	if inv.Closed || d.state.Finished || id != d.state.CurrentModelInvocationID ||
		(att.Status != ModelAttemptStatusRequesting && att.Status != ModelAttemptStatusStreaming) {
		return Reject(RejectionCodeInvalidTransition, "model cannot complete")
	}
	return d.activeStep()
}

func validateToolPlan(p *ToolCallPlannedEvent, position int, attempt string, seen map[string]bool) error {
	if p == nil || p.ToolCallID == "" || p.FirstAttemptID == "" || p.SourcePosition != position ||
		p.DescriptorVersion == "" || p.InputRef == "" || p.EffectClass != EffectClassPureRead {
		return Reject(RejectionCodeCommandRejected, "unsupported tool descriptor")
	}
	if seen[p.ToolCallID] || seen[p.FirstAttemptID] || p.FirstAttemptID == attempt {
		return Reject(RejectionCodeCommandRejected, "invalid tool plan")
	}
	return nil
}

func (d *decision) streamAllowed(inv *ModelInvocation, att *ModelAttempt, id string) error {
	if inv.Closed || d.state.Finished || id != d.state.CurrentModelInvocationID || att.Status != ModelAttemptStatusRequesting {
		return Reject(RejectionCodeInvalidTransition, "model is not requesting")
	}
	return d.activeStep()
}
