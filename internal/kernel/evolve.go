package kernel

import "slices"

// Evolve is the pure total state transition function of the kernel state machine.
// Given the current state and an event, it returns the next state without mutating
// the input state or performing side effects.
func Evolve(s *State, e Event) *State {
	if s == nil {
		s = NewState()
	}
	next := cloneState(s)
	if isNilEvent(e) {
		return &next
	}
	if env := e.EventEnvelope(); env.Sequence > next.LastSequence {
		next.LastSequence = env.Sequence
	}
	switch {
	case evolveRunAndStepEvents(&next, e):
	case evolveModelEvents(&next, e):
	case evolveToolEvents(&next, e):
	case evolveTimerBudgetEvents(&next, e):
	}
	return &next
}

func isNilEvent(e Event) bool {
	return e == nil || e.isNil()
}

func evolveRunAndStepEvents(s *State, e Event) bool {
	return evolveRunLifecycleEvents(s, e) ||
		evolveRunControlEvents(s, e) ||
		evolveRunRecoveryEvents(s, e) ||
		evolveStepLifecycleEvents(s, e) ||
		evolveStepContextEvents(s, e)
}

func evolveModelEvents(s *State, e Event) bool {
	return evolveModelDispatchEvents(s, e) ||
		evolveModelLifecycleEvents(s, e)
}

func evolveToolEvents(s *State, e Event) bool {
	return evolveToolCheckEvents(s, e) ||
		evolveToolDispatchEvents(s, e) ||
		evolveToolOutcomeEvents(s, e)
}

func cloneState(s *State) State {
	next := *s
	next.ModelInvocations = cloneModelInvocations(s.ModelInvocations)
	next.ToolInvocations = cloneToolInvocations(s.ToolInvocations)
	next.ToolOrder = slices.Clone(s.ToolOrder)
	next.Blocks = cloneBlocks(s.Blocks)
	next.ActiveTimers = cloneActiveTimers(s.ActiveTimers)
	next.InterruptionCauses = cloneInterruptionCauses(s.InterruptionCauses)
	next.QuiescingAttempts = cloneQuiescingAttempts(s.QuiescingAttempts)
	if s.StopIntent != nil {
		stopCopy := *s.StopIntent
		next.StopIntent = &stopCopy
	}
	return next
}

func cloneModelInvocations(src map[string]*ModelInvocation) map[string]*ModelInvocation {
	dst := make(map[string]*ModelInvocation, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		invCopy := *v
		invCopy.ToolIDs = slices.Clone(v.ToolIDs)
		invCopy.Attempts = cloneModelAttempts(v.Attempts)
		dst[k] = &invCopy
	}
	return dst
}

func cloneModelAttempts(src map[string]*ModelAttempt) map[string]*ModelAttempt {
	dst := make(map[string]*ModelAttempt, len(src))
	for ak, av := range src {
		if av == nil {
			dst[ak] = nil
			continue
		}
		attCopy := *av
		dst[ak] = &attCopy
	}
	return dst
}

func cloneToolInvocations(src map[string]*ToolInvocation) map[string]*ToolInvocation {
	dst := make(map[string]*ToolInvocation, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		toolCopy := *v
		toolCopy.Attempts = cloneToolAttempts(v.Attempts)
		dst[k] = &toolCopy
	}
	return dst
}

func cloneToolAttempts(src map[string]*ToolAttempt) map[string]*ToolAttempt {
	dst := make(map[string]*ToolAttempt, len(src))
	for ak, av := range src {
		if av == nil {
			dst[ak] = nil
			continue
		}
		attCopy := *av
		dst[ak] = &attCopy
	}
	return dst
}

func cloneBlocks(src map[string]*Block) map[string]*Block {
	dst := make(map[string]*Block, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		blkCopy := *v
		dst[k] = &blkCopy
	}
	return dst
}

func cloneActiveTimers(src map[string]*ActiveTimer) map[string]*ActiveTimer {
	dst := make(map[string]*ActiveTimer, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		timerCopy := *v
		dst[k] = &timerCopy
	}
	return dst
}

func cloneInterruptionCauses(src map[string]*InterruptionCause) map[string]*InterruptionCause {
	dst := make(map[string]*InterruptionCause, len(src))
	for k, v := range src {
		if v == nil {
			dst[k] = nil
			continue
		}
		causeCopy := *v
		dst[k] = &causeCopy
	}
	return dst
}

func cloneQuiescingAttempts(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
