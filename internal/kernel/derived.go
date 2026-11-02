package kernel

import "slices"

func canonicalWaitOrder() []WaitReason {
	return []WaitReason{
		WaitReasonApproval,
		WaitReasonUserInput,
		WaitReasonDependency,
		WaitReasonRetryTimer,
		WaitReasonWorkerCapacity,
		WaitReasonVaultLocked,
		WaitReasonHostPaused,
		WaitReasonRecoveryAdmission,
		WaitReasonStepBudget,
	}
}

// DeriveLifecycle computes the top-level lifecycle state from authoritative facts.
// Follows precedence order 1 to 7 defined in state-machine §2.
func DeriveLifecycle(s *State) Lifecycle {
	if s == nil {
		return LifecycleQueued
	}
	if s.Finished {
		return LifecycleFinished
	}
	if len(s.InterruptionCauses) > 0 {
		return LifecycleInterrupted
	}
	if s.StopIntent != nil && s.StopIntent.Mode == StopModeTerminal {
		return LifecycleCancelling
	}
	if !s.Admitted {
		return LifecycleQueued
	}
	if hasActiveAttempt(s) {
		return LifecycleRunning
	}
	if len(DeriveWaitReasons(s)) > 0 {
		return LifecycleWaiting
	}
	return LifecycleRunning
}

func hasActiveAttempt(s *State) bool {
	if len(s.QuiescingAttempts) > 0 {
		return true
	}
	for _, inv := range s.ModelInvocations {
		for _, att := range inv.Attempts {
			if att.Status == ModelAttemptStatusRequesting || att.Status == ModelAttemptStatusStreaming {
				return true
			}
		}
	}
	for _, tool := range s.ToolInvocations {
		for _, att := range tool.Attempts {
			if att.Status == ToolAttemptStatusDispatching {
				return true
			}
		}
	}
	return false
}

// DerivePhase computes the execution frontier phase within the current step.
// Follows the conditions defined in state-machine §2.
func DerivePhase(s *State) Phase {
	if s == nil {
		return PhasePreparing
	}
	if isFinalizing(s) {
		return PhaseFinalizing
	}
	if s.CurrentModelInvocationID == "" {
		return PhasePreparing
	}
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	if inv == nil || !inv.Planned {
		return PhasePreparing
	}
	if !inv.Completed && !inv.Closed {
		return PhaseCallingModel
	}
	return PhaseProcessingTools
}

func isFinalizing(s *State) bool {
	if s.Finished {
		return true
	}
	if s.StopIntent != nil && s.StopIntent.Mode == StopModeTerminal {
		return true
	}
	return s.StepCompleted && s.StepContinuation == StepContinuationEnd
}

// DeriveWaitReasons returns all active wait dependencies in fixed canonical order.
func DeriveWaitReasons(s *State) []WaitReason {
	if s == nil {
		return nil
	}
	present := make(map[WaitReason]bool)
	collectBlockReasons(s, present)
	collectTimerReasons(s, present)
	collectApprovalReasons(s, present)

	out := make([]WaitReason, 0, len(present))
	for _, r := range canonicalWaitOrder() {
		if present[r] {
			out = append(out, r)
		}
	}
	return out
}

func collectBlockReasons(s *State, present map[WaitReason]bool) {
	for _, blk := range s.Blocks {
		if slices.Contains(canonicalWaitOrder(), blk.Kind) {
			present[blk.Kind] = true
		}
	}
	if s.StopIntent != nil && s.StopIntent.Mode == StopModeSuspend {
		present[WaitReasonHostPaused] = true
	}
}

func collectTimerReasons(s *State, present map[WaitReason]bool) {
	for _, t := range s.ActiveTimers {
		if t.Purpose == "model_retry" || t.Purpose == "tool_retry" {
			present[WaitReasonRetryTimer] = true
		}
	}
}

func collectApprovalReasons(s *State, present map[WaitReason]bool) {
	for _, tool := range s.ToolInvocations {
		for _, att := range tool.Attempts {
			if att.Status == ToolAttemptStatusAwaitingApproval {
				present[WaitReasonApproval] = true
				return
			}
		}
	}
}
