package kernel

func evolveModelDispatchEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ModelRequestPlannedEvent:
		s.CurrentModelInvocationID = ev.InvocationID
		inv := &ModelInvocation{
			InvocationID:      ev.InvocationID,
			Planned:           true,
			Completed:         false,
			Failed:            false,
			Closed:            false,
			SelectedAttemptID: "",
			Attempts:          make(map[string]*ModelAttempt),
		}
		inv.Attempts[ev.FirstAttemptID] = &ModelAttempt{
			AttemptID: ev.FirstAttemptID,
			Status:    ModelAttemptStatusPlanned,
		}
		s.ModelInvocations[ev.InvocationID] = inv
		return true
	case *ModelDispatchStartedEvent:
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusRequesting)
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil && inv.Attempts[ev.AttemptID] != nil {
			inv.Attempts[ev.AttemptID].Dispatch = *ev
		}
		return true
	case *ModelStreamStartedEvent:
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusStreaming)
		recordModelStream(s, ev)
		return true
	case *ModelResponseRecordedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil {
			inv.Completed = true
			inv.SelectedAttemptID = ev.AttemptID
			if att := inv.Attempts[ev.AttemptID]; att != nil {
				att.Status = ModelAttemptStatusCompleted
				att.Response = *ev
			}
		}
		return true
	case *ModelRequestFailedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusFailed)
		return true
	case *ModelRequestInterruptedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusInterrupted)
		return true
	default:
		return false
	}
}

func updateModelAttemptStatus(s *State, invID, attID string, status ModelAttemptStatus) {
	if inv := s.ModelInvocations[invID]; inv != nil {
		if att, ok := inv.Attempts[attID]; ok {
			att.Status = status
		}
	}
}

func evolveModelLifecycleEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ModelRetryScheduledEvent:
		return true
	case *ModelAttemptPlannedEvent:
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil {
			inv.Attempts[ev.AttemptID] = &ModelAttempt{
				AttemptID: ev.AttemptID,
				Status:    ModelAttemptStatusPlanned,
			}
		}
		return true
	case *ModelAttemptCancelledEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusCancelled)
		return true
	case *ModelAttemptAbandonedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusAbandonedUnknown)
		if ev.NextAction == NextActionClose {
			if inv := s.ModelInvocations[ev.InvocationID]; inv != nil {
				inv.Closed = true
			}
		}
		return true
	case *ModelInvocationFailedEvent:
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil {
			inv.Failed = true
			inv.Closed = true
		}
		return true
	case *ModelUsageReconciledEvent:
		return true
	default:
		return false
	}
}

func evolveToolCheckEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ToolCallPlannedEvent:
		if _, ok := s.ToolInvocations[ev.ToolCallID]; ok {
			return true
		}
		planTool(s, ev)
		return true
	case *ToolCheckResetEvent:
		resetToolCheck(s, ev.ToolCallID, ev.NewGeneration)
		return true
	case *ToolCheckStartedEvent:
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusChecking)
		if tool := s.ToolInvocations[ev.ToolCallID]; tool != nil {
			tool.CheckGeneration = ev.CheckGeneration
		}
		return true
	case *ToolApprovalRequestedEvent:
		setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusChecking, ToolAttemptStatusAwaitingApproval)
		return true
	case *ToolApprovalResolvedEvent:
		if ev.Resolution == "denied" {
			setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusAwaitingApproval, ToolAttemptStatusDenied)
		}
		return true
	case *ToolReadyRecordedEvent:
		if tool := s.ToolInvocations[ev.ToolCallID]; tool != nil {
			tool.Ready = *ev
		}
		setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusChecking, ToolAttemptStatusReady)
		setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusAwaitingApproval, ToolAttemptStatusReady)
		return true
	default:
		return false
	}
}

func planTool(s *State, ev *ToolCallPlannedEvent) {
	tool := &ToolInvocation{
		ToolCallID:        ev.ToolCallID,
		SourcePosition:    ev.SourcePosition,
		DescriptorVersion: ev.DescriptorVersion,
		EffectClass:       ev.EffectClass,
		CheckGeneration:   1,
		Closed:            false,
		SelectedAttemptID: "",
		Attempts:          make(map[string]*ToolAttempt),
	}
	tool.Attempts[ev.FirstAttemptID] = &ToolAttempt{
		AttemptID: ev.FirstAttemptID,
		Status:    ToolAttemptStatusPlanned,
	}
	tool.Plan = *ev
	countLoopCall(s, tool, ev.RepeatsPrevious)
	s.ToolInvocations[ev.ToolCallID] = tool
	recordModelTool(s, ev.ToolCallID)
	s.ToolOrder = append(s.ToolOrder, ev.ToolCallID)
}

// countLoopCall advances the run's repetition counter for a planned call. A call
// from a cut answer is never run, and its synthetic result skips the reminder
// path, so it neither counts nor resets the count; its Repeat stays zero.
func countLoopCall(s *State, tool *ToolInvocation, repeatsPrevious bool) {
	if responseWasCut(s) {
		return
	}
	if repeatsPrevious {
		s.LoopRepeat++
	} else {
		s.LoopRepeat = 1
	}
	tool.Repeat = s.LoopRepeat
}

// responseWasCut reports whether the step's selected model response ended on length.
func responseWasCut(s *State) bool {
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	if inv == nil {
		return false
	}
	att := inv.Attempts[inv.SelectedAttemptID]
	return att != nil && att.Response.FinishReason == FinishReasonLength
}

func resetToolCheck(s *State, toolCallID string, newGen uint64) {
	if tool := s.ToolInvocations[toolCallID]; tool != nil {
		tool.CheckGeneration = newGen
		for _, att := range tool.Attempts {
			if att.Status == ToolAttemptStatusChecking ||
				att.Status == ToolAttemptStatusAwaitingApproval ||
				att.Status == ToolAttemptStatusReady {
				att.Status = ToolAttemptStatusPlanned
			}
		}
	}
}

func updateToolAttemptStatus(s *State, toolID, attID string, status ToolAttemptStatus) {
	if tool := s.ToolInvocations[toolID]; tool != nil {
		if att, ok := tool.Attempts[attID]; ok {
			att.Status = status
		}
	}
}

func setToolStatusIf(s *State, toolID string, current, target ToolAttemptStatus) {
	if tool := s.ToolInvocations[toolID]; tool != nil {
		for _, att := range tool.Attempts {
			if att.Status == current {
				att.Status = target
			}
		}
	}
}

func evolveToolDispatchEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ToolDispatchStartedEvent:
		s.ToolCallsDispatched++
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusDispatching)
		if tool := s.ToolInvocations[ev.ToolCallID]; tool != nil && tool.Attempts[ev.AttemptID] != nil {
			tool.Attempts[ev.AttemptID].Dispatch = *ev
		}
		return true
	case *ToolResultRecordedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		recordToolResult(s, ev)
		return true
	case *ToolAttemptPlannedEvent:
		if tool := s.ToolInvocations[ev.ToolCallID]; tool != nil {
			tool.Attempts[ev.NewAttemptID] = &ToolAttempt{
				AttemptID: ev.NewAttemptID,
				Status:    ToolAttemptStatusPlanned,
			}
		}
		return true
	case *InvocationQuiesceRequestedEvent:
		if attemptExists(s, ev.AttemptID) {
			s.QuiescingAttempts[ev.AttemptID] = true
		}
		return true
	default:
		return false
	}
}

func attemptExists(s *State, attemptID string) bool {
	if attemptID == "" {
		return false
	}
	return modelAttemptExists(s, attemptID) || toolAttemptExists(s, attemptID)
}

func modelAttemptExists(s *State, attemptID string) bool {
	for _, inv := range s.ModelInvocations {
		if inv != nil {
			if _, ok := inv.Attempts[attemptID]; ok {
				return true
			}
		}
	}
	return false
}

func toolAttemptExists(s *State, attemptID string) bool {
	for _, tool := range s.ToolInvocations {
		if tool != nil {
			if _, ok := tool.Attempts[attemptID]; ok {
				return true
			}
		}
	}
	return false
}

func evolveToolOutcomeEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ToolOutcomeUnknownEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusOutcomeUnknown)
		return true
	case *ToolReconciledEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		switch ev.ObservedOutcome {
		case ToolOutcomeUnknownObservedDisconnected:
			updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusSucceeded)
		case ToolOutcomeUnknownObservedProcessLost, ToolOutcomeUnknownObservedTimeout:
			updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusFailed)
		}
		return true
	case *ToolAttemptAbandonedEvent:
		delete(s.QuiescingAttempts, ev.AttemptID)
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusAbandonedUnknown)
		if ev.NextAction == NextActionClose {
			if tool := s.ToolInvocations[ev.ToolCallID]; tool != nil {
				tool.Closed = true
			}
		}
		return true
	default:
		return false
	}
}

func recordToolResult(s *State, ev *ToolResultRecordedEvent) {
	delete(s.QuiescingAttempts, ev.AttemptID)
	tool := s.ToolInvocations[ev.ToolCallID]
	if tool == nil {
		return
	}
	tool.Closed = true
	tool.SelectedAttemptID = ev.AttemptID
	att, ok := tool.Attempts[ev.AttemptID]
	if !ok || att == nil {
		return
	}
	att.Result = *ev
	switch ev.Outcome {
	case ToolResultOutcomeSucceeded:
		att.Status = ToolAttemptStatusSucceeded
	case ToolResultOutcomeFailed:
		att.Status = ToolAttemptStatusFailed
	case ToolResultOutcomeDenied:
		att.Status = ToolAttemptStatusDenied
	case ToolResultOutcomeRefused:
		att.Status = ToolAttemptStatusRefused
	case ToolResultOutcomeCancelled:
		att.Status = ToolAttemptStatusCancelled
	case ToolResultOutcomeAbandonedUnknown:
		att.Status = ToolAttemptStatusAbandonedUnknown
	}
}

func evolveTimerBudgetEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *BudgetObservedEvent:
		if ev.TokensUsed > 0 {
			s.TokensUsed = addSaturating(s.TokensUsed, int64(ev.TokensUsed))
		}
		recordResponseUsage(s, ev)
		return true
	case *TimeBudgetWindowOpenedEvent, *TimeBudgetWindowClosedEvent:
		return true
	case *TimerScheduledEvent:
		s.ActiveTimers[ev.TimerID] = &ActiveTimer{
			TimerID:    ev.TimerID,
			Generation: ev.Generation,
			Purpose:    ev.Purpose,
			Deadline:   ev.SchedulingDeadline,
		}
		return true
	case *TimerElapsedEvent:
		delete(s.ActiveTimers, ev.TimerID)
		return true
	case *TimerCancelledEvent:
		delete(s.ActiveTimers, ev.TimerID)
		return true
	default:
		return false
	}
}

func recordModelTool(s *State, id string) {
	if inv := s.ModelInvocations[s.CurrentModelInvocationID]; inv != nil {
		inv.ToolIDs = append(inv.ToolIDs, id)
	}
}

func recordModelStream(s *State, ev *ModelStreamStartedEvent) {
	if inv := s.ModelInvocations[ev.InvocationID]; inv != nil && inv.Attempts[ev.AttemptID] != nil {
		inv.Attempts[ev.AttemptID].Stream = *ev
	}
}

// Preserve the original completion observation for duplicate detection; later
// accounting events must not change the identity of an already accepted response.
func recordResponseUsage(s *State, ev *BudgetObservedEvent) {
	for _, inv := range s.ModelInvocations {
		if inv == nil {
			continue
		}
		att := inv.Attempts[ev.AttemptID]
		if att != nil && att.Response.ResponseRef != "" && att.ResponseUsage.AttemptID == "" {
			att.ResponseUsage = *ev
		}
	}
}
