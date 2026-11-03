package kernel

// TimeWindow retains accounting evidence independently of the scheduling timer.
// Closing execution control never manufactures accounting coverage.
type TimeWindow struct {
	Open   TimeBudgetWindowOpenedEvent
	Close  TimeBudgetWindowClosedEvent
	Closed bool
}

func cloneTimeWindows(src map[string]*TimeWindow) map[string]*TimeWindow {
	dst := make(map[string]*TimeWindow, len(src))
	for id, w := range src {
		if w != nil {
			copyWindow := *w
			dst[id] = &copyWindow
		} else {
			dst[id] = nil
		}
	}
	return dst
}

func evolveInvariantFacts(s *State, e Event) {
	evolveWindowFacts(s, e)
	recordModelEvidence(s, e)
	switch ev := e.(type) {
	case *ModelDispatchStartedEvent:
		recordModelOwner(s, ev.InvocationID, ev.AttemptID)
	case *ToolDispatchStartedEvent:
		recordToolOwner(s, ev.ToolCallID, ev.AttemptID)
	case *ToolApprovalRequestedEvent:
		if t := s.ToolInvocations[ev.ToolCallID]; t != nil {
			t.Approval = *ev
			t.LastApproval = *ev
		}
	case *ToolApprovalResolvedEvent:
		recordApprovalResolution(s, ev)
		clearApproval(s, ev.ToolCallID, ev.CheckGeneration)
	case *ToolCheckResetEvent:
		clearApproval(s, ev.ToolCallID, ev.OldGeneration)
	case *ToolResultRecordedEvent:
		if t := s.ToolInvocations[ev.ToolCallID]; t != nil {
			var empty ToolApprovalRequestedEvent
			t.Approval = empty
		}
	case *ToolOutcomeUnknownEvent:
		setExposure(s, ev.ToolCallID, ev.AttemptID, true)
	case *ToolReconciledEvent:
		recordReconciliation(s, ev)
	}
}

func clearApproval(s *State, id string, generation uint64) {
	if t := s.ToolInvocations[id]; t != nil && t.Approval.CheckGeneration == generation {
		var empty ToolApprovalRequestedEvent
		t.Approval = empty
	}
}

func setExposure(s *State, toolID, attemptID string, unknown bool) {
	if t := s.ToolInvocations[toolID]; t != nil {
		if a := t.Attempts[attemptID]; a != nil {
			a.ExposureUnknown = unknown
		}
	}
}

func evolveWindowFacts(s *State, e Event) {
	switch ev := e.(type) {
	case *TimeBudgetWindowOpenedEvent:
		if _, exists := s.TimeWindows[ev.WindowID]; !exists {
			s.TimeWindows[ev.WindowID] = &TimeWindow{Open: *ev}
		}
	case *TimeBudgetWindowClosedEvent:
		if w := s.TimeWindows[ev.WindowID]; w != nil && !w.Closed {
			w.Close = *ev
			w.Closed = true
		}

	}
}

func recordModelOwner(s *State, id, attempt string) {
	if inv := s.ModelInvocations[id]; inv != nil {
		if a := inv.Attempts[attempt]; a != nil {
			a.OwnerIncarnation = s.OwnerIncarnation
		}
	}
}

func recordToolOwner(s *State, id, attempt string) {
	if t := s.ToolInvocations[id]; t != nil {
		if a := t.Attempts[attempt]; a != nil {
			a.OwnerIncarnation = s.OwnerIncarnation
		}
	}
}

func recordModelEvidence(s *State, e Event) {
	switch ev := e.(type) {
	case *ModelRequestFailedEvent:
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil && inv.Attempts[ev.AttemptID] != nil {
			inv.Attempts[ev.AttemptID].Failure = *ev
		}
	case *ModelRequestInterruptedEvent:
		if inv := s.ModelInvocations[ev.InvocationID]; inv != nil && inv.Attempts[ev.AttemptID] != nil {
			inv.Attempts[ev.AttemptID].Interrupted = *ev
		}
	}
}

func recordApprovalResolution(s *State, e *ToolApprovalResolvedEvent) {
	if t := s.ToolInvocations[e.ToolCallID]; t != nil && t.Approval.CheckGeneration == e.CheckGeneration {
		t.LastResolution = *e
	}
}

func recordReconciliation(s *State, e *ToolReconciledEvent) {
	setExposure(s, e.ToolCallID, e.AttemptID, false)
	if t := s.ToolInvocations[e.ToolCallID]; t != nil {
		if a := t.Attempts[e.AttemptID]; a != nil {
			a.Reconciliation = *e
		}
	}
}
