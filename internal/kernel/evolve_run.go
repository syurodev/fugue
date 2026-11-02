package kernel

func evolveRunLifecycleEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *RunAcceptedEvent:
		s.Accepted = true
		if s.RunID == "" {
			s.RunID = ev.Envelope.RunID
		}
		return true
	case *RunAdmittedEvent:
		s.Admitted = true
		s.AdmissionToken = ev.Token
		s.OwnerIncarnation = ev.OwnerIncarnation
		s.ContextEpoch = ev.ContextEpoch
		s.ContextRevision = ev.InitialContextRevision
		return true
	case *RunOwnershipAcquiredEvent:
		s.AdmissionToken = ev.NewToken
		return true
	case *RunFinishedEvent:
		s.Finished = true
		s.Outcome = ev.Outcome
		clear(s.ActiveTimers)
		clear(s.QuiescingAttempts)
		return true
	default:
		return false
	}
}

func evolveRunControlEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *RunStopRequestedEvent:
		if s.StopIntent == nil {
			s.StopIntent = &StopIntent{
				Reason:      ev.Reason,
				Mode:        ev.Mode,
				Actor:       ev.Actor,
				Target:      ev.Target,
				RequestTime: ev.RequestTime,
			}
		}
		return true
	case *RunBlockAddedEvent:
		s.Blocks[ev.BlockerID] = &Block{
			BlockerID:       ev.BlockerID,
			Kind:            ev.Kind,
			Reference:       ev.Reference,
			ResumeCondition: ev.ResumeCondition,
		}
		return true
	case *RunBlockClearedEvent:
		delete(s.Blocks, ev.BlockerID)
		return true
	case *RunInterruptedEvent:
		if ev.CauseID != "" {
			s.InterruptionCauses[ev.CauseID] = &InterruptionCause{
				CauseID:    ev.CauseID,
				Cause:      ev.Cause,
				AttemptRef: ev.AttemptRef,
				PayloadRef: ev.PayloadRef,
			}
		}
		return true
	case *RunRecoveryClearedEvent:
		for _, causeID := range ev.CauseIDs {
			delete(s.InterruptionCauses, causeID)
		}
		return true
	default:
		return false
	}
}

func evolveRunRecoveryEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *UnknownAcceptedEvent:
		markAttemptsAbandoned(s, ev.TargetAttempts)
		return true
	case *RunClosureConfirmedEvent:
		s.ContextRequirement = ev.ResultingContextRequirement
		s.ContextRevision = ev.NewContextRevision
		for _, causeID := range ev.CauseIDs {
			delete(s.InterruptionCauses, causeID)
		}
		markAttemptsWithDispositions(s, ev.AttemptDispositions)
		return true
	case *ContextBoundarySelectedEvent:
		s.ContextRevision = ev.NewContextRevision
		s.ContextEpoch = ev.Epoch
		s.ContextRequirement = ContextRequirementNone
		return true
	default:
		return false
	}
}

func markAttemptsAbandoned(s *State, attemptIDs []string) {
	for _, attID := range attemptIDs {
		delete(s.QuiescingAttempts, attID)
		for _, inv := range s.ModelInvocations {
			if att, ok := inv.Attempts[attID]; ok {
				att.Status = ModelAttemptStatusAbandonedUnknown
			}
		}
		for _, tool := range s.ToolInvocations {
			if att, ok := tool.Attempts[attID]; ok {
				att.Status = ToolAttemptStatusAbandonedUnknown
			}
		}
	}
}

func markAttemptsWithDispositions(s *State, disps map[string]AttemptDisposition) {
	for attID := range disps {
		delete(s.QuiescingAttempts, attID)
	}
	markModelAttemptsWithDispositions(s, disps)
	markToolAttemptsWithDispositions(s, disps)
}

func markModelAttemptsWithDispositions(s *State, disps map[string]AttemptDisposition) {
	for attID, disp := range disps {
		for _, inv := range s.ModelInvocations {
			if att, ok := inv.Attempts[attID]; ok {
				switch disp {
				case AttemptDispositionAbandonedUnknown:
					att.Status = ModelAttemptStatusAbandonedUnknown
				case AttemptDispositionCancelled:
					att.Status = ModelAttemptStatusCancelled
				}
			}
		}
	}
}

func markToolAttemptsWithDispositions(s *State, disps map[string]AttemptDisposition) {
	for attID, disp := range disps {
		for _, tool := range s.ToolInvocations {
			if att, ok := tool.Attempts[attID]; ok {
				switch disp {
				case AttemptDispositionAbandonedUnknown:
					att.Status = ToolAttemptStatusAbandonedUnknown
				case AttemptDispositionCancelled:
					att.Status = ToolAttemptStatusCancelled
				}
			}
		}
	}
}

func evolveStepLifecycleEvents(s *State, e Event) bool {
	switch ev := e.(type) {
	case *StepStartedEvent:
		s.StepStarted = true
		s.StepCompleted = false
		s.StepAborted = false
		s.CurrentStepIndex = ev.StepIndex
		s.CurrentModelInvocationID = ""
		s.ToolOrder = nil
		return true
	case *StepCompletedEvent:
		s.StepCompleted = true
		s.StepContinuation = ev.Continuation
		return true
	case *StepAbortedEvent:
		s.StepAborted = true
		s.StepCompleted = false
		return true
	case *BudgetExtendedEvent:
		return true
	default:
		return false
	}
}

func evolveStepContextEvents(_ *State, e Event) bool {
	switch e.(type) {
	case *ToolBindingRevisedEvent,
		*InstructionsLoadedEvent,
		*ContextInjectedEvent,
		*ToolResultsPrunedEvent,
		*ContextCompactedEvent,
		*InputSteeredEvent,
		*InputInterjectedEvent:
		return true
	default:
		return false
	}
}
