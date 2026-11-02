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
		return true
	case *ModelStreamStartedEvent:
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusStreaming)
		return true
	case *ModelResponseRecordedEvent:
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil {
			inv.Completed = true
			inv.SelectedAttemptID = ev.AttemptID
			if att, ok := inv.Attempts[ev.AttemptID]; ok {
				att.Status = ModelAttemptStatusCompleted
			}
		}
		return true
	case *ModelRequestFailedEvent:
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusFailed)
		return true
	case *ModelRequestInterruptedEvent:
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
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusCancelled)
		return true
	case *ModelAttemptAbandonedEvent:
		updateModelAttemptStatus(s, ev.InvocationID, ev.AttemptID, ModelAttemptStatusAbandonedUnknown)
		if ev.NextAction == "close" {
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
		s.ToolInvocations[ev.ToolCallID] = tool
		s.ToolOrder = append(s.ToolOrder, ev.ToolCallID)
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
		setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusChecking, ToolAttemptStatusReady)
		setToolStatusIf(s, ev.ToolCallID, ToolAttemptStatusAwaitingApproval, ToolAttemptStatusReady)
		return true
	default:
		return false
	}
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
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusDispatching)
		return true
	case *ToolResultRecordedEvent:
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
		s.QuiescingAttempts[ev.AttemptID] = true
		return true
	default:
		return false
	}
}

func evolveToolOutcomeEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *ToolOutcomeUnknownEvent:
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusOutcomeUnknown)
		return true
	case *ToolReconciledEvent:
		switch ev.ObservedOutcome {
		case "succeeded":
			updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusSucceeded)
		case "failed":
			updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusFailed)
		}
		return true
	case *ToolAttemptAbandonedEvent:
		updateToolAttemptStatus(s, ev.ToolCallID, ev.AttemptID, ToolAttemptStatusAbandonedUnknown)
		if ev.NextAction == "close" {
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
	tool := s.ToolInvocations[ev.ToolCallID]
	if tool == nil {
		return
	}
	tool.Closed = true
	tool.SelectedAttemptID = ev.AttemptID
	att, ok := tool.Attempts[ev.AttemptID]
	if !ok {
		return
	}
	switch ev.Outcome {
	case "succeeded":
		att.Status = ToolAttemptStatusSucceeded
	case "failed":
		att.Status = ToolAttemptStatusFailed
	case "denied":
		att.Status = ToolAttemptStatusDenied
	case "refused":
		att.Status = ToolAttemptStatusRefused
	case "cancelled":
		att.Status = ToolAttemptStatusCancelled
	case "abandoned_unknown":
		att.Status = ToolAttemptStatusAbandonedUnknown
	}
}

func evolveTimerBudgetEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *TimeBudgetWindowOpenedEvent, *TimeBudgetWindowClosedEvent, *BudgetObservedEvent:
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
