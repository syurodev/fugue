package kernel

import "slices"

// InvariantID identifies a design requirement, not a public transport error.
type InvariantID string

// Stable identifiers from state-machine §3.
const (
	SM01 InvariantID = "SM-01"
	SM02 InvariantID = "SM-02"
	SM03 InvariantID = "SM-03"
	SM04 InvariantID = "SM-04"
	SM05 InvariantID = "SM-05"
	SM06 InvariantID = "SM-06"
	SM07 InvariantID = "SM-07"
	SM08 InvariantID = "SM-08"
	SM09 InvariantID = "SM-09"
	SM10 InvariantID = "SM-10"
	SM11 InvariantID = "SM-11"
	SM12 InvariantID = "SM-12"
)

// CheckInvariants checks committed facts only. Historical rules additionally need
// CheckBatch. Diagnostics contain IDs, never protected refs or payload contents.
// Neither checker establishes caller authority, process-stop proof or permission.
func CheckInvariants(s *State) []InvariantID {
	if s == nil {
		s = NewState()
	}
	failed := make(map[InvariantID]bool)
	active := activeAttemptCount(s)
	failed[SM01] = active > 1
	failed[SM02] = !approvalsMatch(s) || !approvalBlocksMatch(s)
	failed[SM03] = approvalWaitWrong(s, active)
	failed[SM04] = frontierPhaseWrong(s)
	failed[SM05] = s.Finished && !finishSafe(s)
	failed[SM06] = unknownLifecycleWrong(s)
	failed[SM09] = !modelStructureValid(s) || !toolStructureValid(s)
	failed[SM11] = !unknownExposureRetained(s)
	failed[SM12] = !s.Admitted && (active > 0 || len(s.ModelInvocations) > 0 || len(s.ToolInvocations) > 0)
	return invariantIDs(failed)
}

func invariantIDs(failed map[InvariantID]bool) []InvariantID {
	var result []InvariantID
	for _, id := range []InvariantID{SM01, SM02, SM03, SM04, SM05, SM06, SM07, SM08, SM09, SM10, SM11, SM12} {
		if failed[id] {
			result = append(result, id)
		}
	}
	return result
}

func activeAttemptCount(s *State) int {
	count := 0
	for _, inv := range s.ModelInvocations {
		if inv == nil {
			continue
		}
		for _, a := range inv.Attempts {
			if a != nil && (a.Status == ModelAttemptStatusRequesting || a.Status == ModelAttemptStatusStreaming) {
				count++
			}
		}
	}
	return count + activeToolAttemptCount(s)
}

func activeToolAttemptCount(s *State) int {
	count := 0
	for _, tool := range s.ToolInvocations {
		if tool == nil {
			continue
		}
		for _, a := range tool.Attempts {
			if a != nil && a.Status == ToolAttemptStatusDispatching {
				count++
			}
		}
	}
	return count
}

func approvalsMatch(s *State) bool {
	for id, tool := range s.ToolInvocations {
		if tool == nil {
			return false
		}
		waiting := 0
		for _, a := range tool.Attempts {
			if a != nil && a.Status == ToolAttemptStatusAwaitingApproval {
				waiting++
			}
		}
		if !approvalMatches(id, tool, waiting) {
			return false
		}
	}
	return true
}

func approvalWaitWrong(s *State, active int) bool {
	if s.StopIntent != nil || len(s.InterruptionCauses) > 0 || active > 0 {
		return false
	}
	for _, tool := range s.ToolInvocations {
		if tool != nil && tool.Approval.ToolCallID != "" {
			return DeriveLifecycle(s) != LifecycleWaiting || !slices.Contains(DeriveWaitReasons(s), WaitReasonApproval)
		}
	}
	return false
}

func frontierPhaseWrong(s *State) bool {
	phase := DerivePhase(s)
	if phase != PhaseCallingModel && phase != PhaseProcessingTools {
		return false
	}
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	if !s.StepStarted || s.StepAborted || inv == nil {
		return true
	}
	if phase == PhaseCallingModel {
		return s.StepCompleted || inv.Completed || inv.Closed
	}
	return !inv.Completed || inv.Attempts[inv.SelectedAttemptID] == nil
}

func finishSafe(s *State) bool {
	if hasOpenStep(s) {
		return false
	}
	if activeAttemptCount(s) > 0 || len(s.QuiescingAttempts) > 0 || len(s.ActiveTimers) > 0 {
		return false
	}
	if !windowsAccounted(s) {
		return false
	}
	return invocationsTerminal(s)
}

func invocationsTerminal(s *State) bool {
	for _, inv := range s.ModelInvocations {
		if !modelDisposed(inv) {
			return false
		}
	}
	for _, tool := range s.ToolInvocations {
		if !toolDisposed(tool) {
			return false
		}
	}
	return true
}

func windowsAccounted(s *State) bool {
	for _, w := range s.TimeWindows {
		if w == nil || !w.Closed || w.Close.MeasuredElapsed < 0 || w.Close.TailCoverage == "" || w.Close.ReservationDisposition == "" {
			return false
		}
	}
	return true
}

func unknownLifecycleWrong(s *State) bool {
	if !hasUnresolvedUnknown(s) {
		return false
	}
	return DeriveLifecycle(s) != LifecycleInterrupted
}

func hasUnresolvedUnknown(s *State) bool {
	for _, inv := range s.ModelInvocations {
		if inv == nil {
			continue
		}
		for _, a := range inv.Attempts {
			if a != nil && a.Status == ModelAttemptStatusInterrupted {
				return true
			}
		}
	}
	return hasUnknownTool(s)
}

func hasUnknownTool(s *State) bool {
	for _, tool := range s.ToolInvocations {
		if tool == nil {
			continue
		}
		for _, a := range tool.Attempts {
			if a != nil && a.Status == ToolAttemptStatusOutcomeUnknown {
				return true
			}
		}
	}
	return false
}

func approvalMatches(id string, tool *ToolInvocation, waiting int) bool {
	q := tool.Approval
	if waiting == 0 && q.ToolCallID == "" {
		return true
	}
	if waiting != 1 || q.ToolCallID != id || q.CheckGeneration != tool.CheckGeneration ||
		q.OwnerRequestRef == "" || q.RequestRevision == 0 || q.ExactArgsRef == "" {
		return false
	}
	return true
}
