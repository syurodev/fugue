package kernel

import (
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	testAttID         = "att1"
	testUserActor     = "user"
	testTargetRun     = "run"
	testVaultRef      = "vault"
	testUnlockedCond  = "unlocked"
	testInvID         = "inv1"
	testDescriptorVer = "1.0"
	testUnknownCause  = "effect_unknown"
	testBlockID       = "blk1"
	testSlotRef       = "slot"
	testCapacityCond  = "capacity"
)

type lifecycleTestCase struct {
	name     string
	setup    func(s *State)
	expected Lifecycle
}

func lifecycleCasesPart1(now time.Time) []lifecycleTestCase {
	return []lifecycleTestCase{
		{
			name: "precedence 1: finished wins over all other facts",
			setup: func(s *State) {
				s.Finished = true
				s.Outcome = RunOutcomeCompleted
				s.InterruptionCauses["c1"] = &InterruptionCause{Cause: "corrupt", AttemptRef: testAttID, PayloadRef: "p1"}
				s.StopIntent = &StopIntent{Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: testUserActor, Target: testTargetRun, RequestTime: now}
				s.Admitted = false
			},
			expected: LifecycleFinished,
		},
		{
			name: "precedence 2: interrupted wins over cancelling and active attempts",
			setup: func(s *State) {
				s.InterruptionCauses["c1"] = &InterruptionCause{Cause: testUnknownCause, AttemptRef: testAttID, PayloadRef: "p1"}
				s.StopIntent = &StopIntent{Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: testUserActor, Target: testTargetRun, RequestTime: now}
				s.Admitted = true
				s.QuiescingAttempts[testAttID] = true
			},
			expected: LifecycleInterrupted,
		},
		{
			name: "precedence 3: cancelling wins over queued and waiting",
			setup: func(s *State) {
				s.StopIntent = &StopIntent{Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: testUserActor, Target: testTargetRun, RequestTime: now}
				s.Admitted = false
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonVaultLocked, Reference: testVaultRef, ResumeCondition: testUnlockedCond}
			},
			expected: LifecycleCancelling,
		},
		{
			name: "precedence 4: queued before admission",
			setup: func(s *State) {
				s.Admitted = false
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonVaultLocked, Reference: testVaultRef, ResumeCondition: testUnlockedCond}
			},
			expected: LifecycleQueued,
		},
	}
}

func lifecycleCasesPart2() []lifecycleTestCase {
	return []lifecycleTestCase{
		{
			name: "precedence 5: running with model requesting attempt",
			setup: func(s *State) {
				s.Admitted = true
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonWorkerCapacity, Reference: testSlotRef, ResumeCondition: testCapacityCond}
				inv := &ModelInvocation{InvocationID: testInvID, Planned: true, Attempts: map[string]*ModelAttempt{
					testAttID: {AttemptID: testAttID, Status: ModelAttemptStatusRequesting},
				}}
				s.ModelInvocations[testInvID] = inv
			},
			expected: LifecycleRunning,
		},
		{
			name: "precedence 5: running with model streaming attempt",
			setup: func(s *State) {
				s.Admitted = true
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonWorkerCapacity, Reference: testSlotRef, ResumeCondition: testCapacityCond}
				inv := &ModelInvocation{InvocationID: testInvID, Planned: true, Attempts: map[string]*ModelAttempt{
					testAttID: {AttemptID: testAttID, Status: ModelAttemptStatusStreaming},
				}}
				s.ModelInvocations[testInvID] = inv
			},
			expected: LifecycleRunning,
		},
		{
			name: "precedence 5: running with tool dispatching attempt",
			setup: func(s *State) {
				s.Admitted = true
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonWorkerCapacity, Reference: testSlotRef, ResumeCondition: testCapacityCond}
				tool := &ToolInvocation{ToolCallID: "tc1", DescriptorVersion: testDescriptorVer, EffectClass: EffectClassSideEffecting, Attempts: map[string]*ToolAttempt{
					"tatt1": {AttemptID: "tatt1", Status: ToolAttemptStatusDispatching},
				}}
				s.ToolInvocations["tc1"] = tool
			},
			expected: LifecycleRunning,
		},
		{
			name: "precedence 5: running with tracked quiesce work",
			setup: func(s *State) {
				s.Admitted = true
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonWorkerCapacity, Reference: testSlotRef, ResumeCondition: testCapacityCond}
				s.QuiescingAttempts[testAttID] = true
			},
			expected: LifecycleRunning,
		},
	}
}

func lifecycleCasesPart3() []lifecycleTestCase {
	return []lifecycleTestCase{
		{
			name: "precedence 6: waiting when quiescent with unresolved wait dependency",
			setup: func(s *State) {
				s.Admitted = true
				s.Blocks[testBlockID] = &Block{BlockerID: testBlockID, Kind: WaitReasonVaultLocked, Reference: testVaultRef, ResumeCondition: testUnlockedCond}
			},
			expected: LifecycleWaiting,
		},
		{
			name: "precedence 7: running when admitted and no wait dependency or active effect",
			setup: func(s *State) {
				s.Admitted = true
			},
			expected: LifecycleRunning,
		},
	}
}

func TestDeriveLifecyclePrecedence(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	cases := append(append(lifecycleCasesPart1(now), lifecycleCasesPart2()...), lifecycleCasesPart3()...)
	t.Run("nil state returns queued", func(t *testing.T) {
		t.Parallel()
		if got := DeriveLifecycle(nil); got != LifecycleQueued {
			t.Fatalf("DeriveLifecycle(nil) = %s, want %s", got, LifecycleQueued)
		}
	})

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewState()
			tt.setup(s)
			if got := DeriveLifecycle(s); got != tt.expected {
				t.Fatalf("DeriveLifecycle() = %s, want %s", got, tt.expected)
			}
		})
	}
}

type phaseTestCase struct {
	name     string
	setup    func(s *State)
	expected Phase
}

func phaseCases(now time.Time) []phaseTestCase {
	cases := phaseCasesFinalizing(now)
	return append(cases, phaseCasesActive(now)...)
}

func phaseCasesFinalizing(now time.Time) []phaseTestCase {
	return []phaseTestCase{
		{
			name:     "phase finalizing: finished",
			setup:    func(s *State) { s.Finished = true },
			expected: PhaseFinalizing,
		},
		{
			name: "phase finalizing: terminal stop intent",
			setup: func(s *State) {
				s.StopIntent = &StopIntent{Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: testUserActor, Target: testTargetRun, RequestTime: now}
			},
			expected: PhaseFinalizing,
		},
		{
			name: "phase finalizing: step completed with end continuation",
			setup: func(s *State) {
				s.StepCompleted = true
				s.StepContinuation = StepContinuationEnd
			},
			expected: PhaseFinalizing,
		},
	}
}

func phaseCasesActive(now time.Time) []phaseTestCase {
	return []phaseTestCase{
		{
			name: "phase preparing: step completed with continue continuation",
			setup: func(s *State) {
				s.CurrentModelInvocationID = testInvID
				s.ModelInvocations[testInvID] = &ModelInvocation{InvocationID: testInvID, Planned: true, Completed: true, SelectedAttemptID: testAttID}
				s.StepCompleted = true
				s.StepContinuation = StepContinuationContinue
			},
			expected: PhasePreparing,
		},
		{
			name: "phase preparing: suspend stop intent does not finalize",
			setup: func(s *State) {
				s.StopIntent = &StopIntent{Reason: StopReasonShutdown, Mode: StopModeSuspend, Actor: testUserActor, Target: testTargetRun, RequestTime: now}
			},
			expected: PhasePreparing,
		},
		{
			name:     "phase preparing: no model invocation planned",
			setup:    func(s *State) { s.CurrentModelInvocationID = "" },
			expected: PhasePreparing,
		},
		{
			name: "phase preparing: invocation ID present but not planned",
			setup: func(s *State) {
				s.CurrentModelInvocationID = testInvID
				s.ModelInvocations[testInvID] = &ModelInvocation{InvocationID: testInvID, Planned: false}
			},
			expected: PhasePreparing,
		},
		{
			name: "phase calling_model: model planned and open",
			setup: func(s *State) {
				s.CurrentModelInvocationID = testInvID
				s.ModelInvocations[testInvID] = &ModelInvocation{InvocationID: testInvID, Planned: true}
			},
			expected: PhaseCallingModel,
		},
		{
			name: "phase calling_model: model streaming",
			setup: func(s *State) {
				s.CurrentModelInvocationID = testInvID
				s.ModelInvocations[testInvID] = &ModelInvocation{InvocationID: testInvID, Planned: true, Attempts: map[string]*ModelAttempt{
					testAttID: {AttemptID: testAttID, Status: ModelAttemptStatusStreaming},
				}}
			},
			expected: PhaseCallingModel,
		},
		{
			name: "phase processing_tools: model response completed and selected",
			setup: func(s *State) {
				s.CurrentModelInvocationID = testInvID
				s.ModelInvocations[testInvID] = &ModelInvocation{InvocationID: testInvID, Planned: true, Completed: true, SelectedAttemptID: testAttID}
			},
			expected: PhaseProcessingTools,
		},
	}
}

func TestDerivePhaseFrontier(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	t.Run("nil state returns preparing", func(t *testing.T) {
		t.Parallel()
		if got := DerivePhase(nil); got != PhasePreparing {
			t.Fatalf("DerivePhase(nil) = %s, want %s", got, PhasePreparing)
		}
	})

	for _, tt := range phaseCases(now) {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := NewState()
			tt.setup(s)
			if got := DerivePhase(s); got != tt.expected {
				t.Fatalf("DerivePhase() = %s, want %s", got, tt.expected)
			}
		})
	}
}

func setupEveryReason(s *State, now time.Time) {
	s.ToolInvocations["tc1"] = &ToolInvocation{
		ToolCallID: "tc1", DescriptorVersion: testDescriptorVer, EffectClass: EffectClassSideEffecting,
		Attempts: map[string]*ToolAttempt{"a1": {AttemptID: "a1", Status: ToolAttemptStatusAwaitingApproval}},
	}
	s.Blocks["b_in"] = &Block{BlockerID: "b_in", Kind: WaitReasonUserInput, Reference: "r", ResumeCondition: "c"}
	s.ActiveTimers["t_ret"] = &ActiveTimer{TimerID: "t_ret", Generation: 1, Purpose: TimerPurposeModelRetry, Deadline: now.Add(time.Second)}
	s.Blocks["b_cap"] = &Block{BlockerID: "b_cap", Kind: WaitReasonWorkerCapacity, Reference: "r", ResumeCondition: "c"}
	s.Blocks["b_vlt"] = &Block{BlockerID: "b_vlt", Kind: WaitReasonVaultLocked, Reference: "r", ResumeCondition: "c"}
	s.Blocks["b_pause"] = &Block{BlockerID: "b_pause", Kind: WaitReasonHostPaused, Reference: "r", ResumeCondition: "c"}
	s.Blocks["b_rec"] = &Block{BlockerID: "b_rec", Kind: WaitReasonRecoveryAdmission, Reference: "r", ResumeCondition: "c"}
	s.Blocks["b_bud"] = &Block{BlockerID: "b_bud", Kind: WaitReasonBudget, BudgetKind: BudgetKindSteps, Reference: "r", ResumeCondition: "c"}
	s.Blocks["b_quota"] = &Block{BlockerID: "b_quota", Kind: WaitReasonQuotaRefresh, Reference: "r", ResumeCondition: "c"}
}

func TestDeriveWaitReasons(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	t.Run("nil state returns nil", func(t *testing.T) {
		t.Parallel()
		if reasons := DeriveWaitReasons(nil); reasons != nil {
			t.Fatalf("expected nil reasons, got %v", reasons)
		}
	})

	t.Run("empty state has no wait reasons", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		if reasons := DeriveWaitReasons(s); len(reasons) != 0 {
			t.Fatalf("expected empty reasons, got %v", reasons)
		}
	})

	t.Run("collects every reason in canonical order", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		setupEveryReason(s, now)
		got := DeriveWaitReasons(s)
		expected := []WaitReason{
			WaitReasonApproval, WaitReasonUserInput,
			WaitReasonRetryTimer, WaitReasonWorkerCapacity, WaitReasonVaultLocked,
			WaitReasonHostPaused, WaitReasonRecoveryAdmission, WaitReasonBudget, WaitReasonQuotaRefresh,
		}
		if diff := cmp.Diff(expected, got); diff != "" {
			t.Fatalf("DeriveWaitReasons mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("deduplicates multiple blocks of the same kind", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s.Blocks["b1"] = &Block{BlockerID: "b1", Kind: WaitReasonVaultLocked, Reference: "r1", ResumeCondition: testUnlockedCond}
		s.Blocks["b2"] = &Block{BlockerID: "b2", Kind: WaitReasonVaultLocked, Reference: "r2", ResumeCondition: testUnlockedCond}
		got := DeriveWaitReasons(s)
		if len(got) != 1 || got[0] != WaitReasonVaultLocked {
			t.Fatalf("expected [vault_locked], got %v", got)
		}
	})

	t.Run("handles tool retry timer", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s.ActiveTimers["t1"] = &ActiveTimer{TimerID: "t1", Generation: 1, Purpose: TimerPurposeToolRetry, Deadline: now.Add(time.Second)}
		if got := DeriveWaitReasons(s); !slices.Contains(got, WaitReasonRetryTimer) {
			t.Fatalf("expected retry_timer in wait reasons, got %v", got)
		}
	})
}
