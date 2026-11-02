package kernel

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	evRunID         = "run-1"
	evCmdID         = "cmd-1"
	evCauID         = "cau-1"
	evOrigin        = "test"
	evToken         = 7
	evInput         = "inp-1"
	evSession       = "sess-1"
	evBlockID       = "blk-1"
	evCause         = "effect_unknown"
	evActor         = "user"
	evAttempt1      = "att-1"
	evPay1          = "pay-1"
	evCause1        = "cause-1"
	evModelInv      = "inv"
	evResp          = "resp"
	evCancelled     = "cancelled"
	evVaultLock     = "vault_locked"
	evModelCloseAtt = "att-m-close"
)

func testEvolveRunLifecycleEvents(t *testing.T, env *Envelope, now time.Time) *State {
	t.Helper()
	s := NewState()
	s = Evolve(s, &RunAcceptedEvent{
		Envelope: *env, InputRef: evInput, SessionID: evSession, QueueItemRef: "q-1",
		ProtocolBinding: "proto", InitialBudgets: "b", PriorityClass: "default",
		PeerOutboxRef: "", GoalID: "", GoalRound: 0,
	})
	if !s.Accepted || s.RunID != evRunID {
		t.Fatalf("RunAccepted failed, state: %+v", s)
	}

	s = Evolve(s, &RunAdmittedEvent{
		Envelope: *env, Token: evToken, OwnerIncarnation: 1, QueueDecisionRef: "dec-1",
		ContextEpoch: 1, StartRunID: evRunID, InitialContextRevision: 10,
	})
	if !s.Admitted || s.AdmissionToken != evToken || s.ContextEpoch != 1 || s.ContextRevision != 10 {
		t.Fatalf("RunAdmitted failed, state: %+v", s)
	}

	s = Evolve(s, &RunOwnershipAcquiredEvent{
		Envelope: *env, NewToken: 8, PreviousToken: evToken, RecoveryReceipt: "rcpt", DataDirectoryLockGeneration: 2,
	})
	if s.AdmissionToken != 8 {
		t.Fatalf("RunOwnershipAcquired failed: token = %d", s.AdmissionToken)
	}

	s = Evolve(s, &RunStopRequestedEvent{
		Envelope: *env, Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: evActor, Target: evRunID, RequestTime: now,
	})
	if s.StopIntent == nil || s.StopIntent.Reason != StopReasonUserCancel {
		t.Fatalf("RunStopRequested failed")
	}
	return s
}

func testEvolveRunBlocksAndRecovery(t *testing.T, s *State, env *Envelope) *State {
	t.Helper()
	s = Evolve(s, &RunBlockAddedEvent{
		Envelope: *env, BlockerID: evBlockID, Kind: WaitReasonVaultLocked, Reference: "vault", ResumeCondition: "unlocked",
	})
	if len(s.Blocks) != 1 {
		t.Fatalf("RunBlockAdded failed")
	}

	s = Evolve(s, &RunBlockClearedEvent{Envelope: *env, BlockerID: evBlockID, ResolvingEvidence: "open"})
	if len(s.Blocks) != 0 {
		t.Fatalf("RunBlockCleared failed")
	}

	s = Evolve(s, &RunInterruptedEvent{Envelope: *env, Cause: evCause, AttemptRef: evAttempt1, PayloadRef: evPay1})
	if len(s.InterruptionCauses) != 1 {
		t.Fatalf("RunInterrupted failed")
	}

	s = Evolve(s, &RunRecoveryClearedEvent{
		Envelope: *env, CauseIDs: []string{evCause}, OwnerEvidence: "ok", Disposition: "reconciled", NextAction: "continue",
	})
	if len(s.InterruptionCauses) != 0 {
		t.Fatalf("RunRecoveryCleared failed")
	}
	return s
}

func testEvolveRunClosuresAndBoundaries(t *testing.T, s *State, env *Envelope, now time.Time) *State {
	t.Helper()
	s = Evolve(s, &UnknownAcceptedEvent{
		Envelope: *env, TargetAttempts: []string{evAttempt1}, Revisions: []uint64{1}, Actor: evActor, AcceptedUncertainty: "un", Disposition: "close",
	})

	s.InterruptionCauses[evCause1] = &InterruptionCause{Cause: evVaultLock, AttemptRef: "", PayloadRef: ""}
	s.ModelInvocations["inv-m-close"] = &ModelInvocation{
		InvocationID: "inv-m-close", Planned: true,
		Attempts: map[string]*ModelAttempt{
			evModelCloseAtt: {AttemptID: evModelCloseAtt, Status: ModelAttemptStatusRequesting},
		},
	}
	s.ToolInvocations["tc-close"] = &ToolInvocation{
		ToolCallID: "tc-close", DescriptorVersion: "1.0", EffectClass: EffectClassSideEffecting,
		Attempts: map[string]*ToolAttempt{
			evAttempt1: {AttemptID: evAttempt1, Status: ToolAttemptStatusDispatching},
		},
	}
	s = Evolve(s, &RunClosureConfirmedEvent{
		Envelope: *env, MetadataOnlyPreviewVersion: "v1", RunRevision: 2, ExactAttemptIDs: []string{evAttempt1, evModelCloseAtt},
		CauseIDs: []string{evCause1}, Actor: evActor, UnavailabilityReason: evVaultLock,
		PreviousContextRevision: 10, NewContextRevision: 11, ResultingContextRequirement: ContextRequirementRevalidationRequired,
		CauseEventRef: "ev-1", AttemptDispositions: map[string]AttemptDisposition{
			evAttempt1:      AttemptDispositionAbandonedUnknown,
			evModelCloseAtt: AttemptDispositionCancelled,
		},
	})
	if s.ContextRequirement != ContextRequirementRevalidationRequired || s.ContextRevision != 11 {
		t.Fatalf("RunClosureConfirmed failed")
	}
	if s.ModelInvocations["inv-m-close"].Attempts[evModelCloseAtt].Status != ModelAttemptStatusCancelled {
		t.Fatalf("expected model attempt cancelled by closure confirmation")
	}
	if s.ToolInvocations["tc-close"].Attempts[evAttempt1].Status != ToolAttemptStatusAbandonedUnknown {
		t.Fatalf("expected tool attempt abandoned unknown by closure confirmation")
	}

	s = Evolve(s, &ContextBoundarySelectedEvent{
		Envelope: *env, TargetQueuedRunID: "run-2", OldContextRevision: 11, NewContextRevision: 12,
		Epoch: 2, StartRunID: "run-2", Mode: "fresh", Actor: evActor, ValidationEvidence: "ok", PreviousDecidingEventRef: "ev-2",
	})
	if s.ContextEpoch != 2 || s.ContextRevision != 12 || s.ContextRequirement != ContextRequirementNone {
		t.Fatalf("ContextBoundarySelected failed")
	}

	s.ActiveTimers["t1"] = &ActiveTimer{TimerID: "t1", Generation: 1, Purpose: TimerPurposeRunDeadline, Deadline: now}
	s = Evolve(s, &RunFinishedEvent{
		Envelope: *env, Outcome: RunOutcomeCompleted, Truncated: false, PrimaryStopRef: "stop",
		SecondaryStopRefs: nil, UnresolvedExposureRefs: nil,
	})
	if !s.Finished || s.Outcome != RunOutcomeCompleted || len(s.ActiveTimers) != 0 {
		t.Fatalf("RunFinished failed")
	}
	return s
}

func testEvolveStepEvents(t *testing.T, s *State, env *Envelope, now time.Time) {
	t.Helper()
	s = Evolve(s, &StepStartedEvent{Envelope: *env, StepIndex: 0, ContextRefs: []string{"ctx"}})
	if !s.StepStarted || s.CurrentStepIndex != 0 {
		t.Fatalf("StepStarted failed")
	}

	s = Evolve(s, &ToolBindingRevisedEvent{
		Envelope: *env, OldRevision: 1, NewRevision: 2, AddedGroups: []string{"g"}, RemovedGroups: nil,
		DescriptorVersions: map[string]string{"t": "v1"}, Cause: "session_start",
	})
	s = Evolve(s, &InstructionsLoadedEvent{Envelope: *env, RuleLayer: "R1", SourcePath: "/r", SnapshotPayloadRef: "s", BudgetTokens: 10, PrunedTokens: 0})
	s = Evolve(s, &ContextInjectedEvent{Envelope: *env, Tag: "tag", Source: "src", PayloadRef: "pay"})
	s = Evolve(s, &ToolResultsPrunedEvent{Envelope: *env, ToolResultPositions: []int{0}, PruneParams: "p", TokensSaved: 10})
	s = Evolve(s, &ContextCompactedEvent{
		Envelope: *env, SummaryPayloadRef: "sum", FirstRetainedPosition: 1, TokensBefore: 20, TokensAfter: 10,
		WorkspaceMemoryRefs: []string{"w"}, ModelInvocationRef: evModelInv,
	})
	s = Evolve(s, &InputSteeredEvent{Envelope: *env, InputRef: evInput, Origin: evActor, Timestamp: now})
	s = Evolve(s, &InputInterjectedEvent{
		Envelope: *env, InputRef: "inp-2", MergedSteerRefs: []string{evInput}, TargetAttemptID: evAttempt1,
		TargetInvocationID: "inv-1", QuiesceResult: "quiesced",
	})

	s = Evolve(s, &StepCompletedEvent{Envelope: *env, StepIndex: 0, ResponseRef: evResp, OrderedResultRefs: []string{"r"}, Continuation: StepContinuationContinue})
	if !s.StepCompleted || s.StepContinuation != StepContinuationContinue {
		t.Fatalf("StepCompleted failed")
	}

	s = Evolve(s, &BudgetExtendedEvent{Envelope: *env, Kind: "steps", ExtensionAmount: 10, Actor: evActor})
	s = Evolve(s, &StepAbortedEvent{Envelope: *env, StepIndex: 0, Reason: "stop", Dispositions: map[string]string{evAttempt1: evCancelled}})
	if !s.StepAborted || s.StepCompleted {
		t.Fatalf("StepAborted failed")
	}
}

func TestEvolvePurityAndCoverage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)

	t.Run("nil event returns cloned state with identical content", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s.RunID = evRunID
		got := Evolve(s, nil)
		if got == s {
			t.Fatalf("expected different pointer for purity")
		}
		if diff := cmp.Diff(s, got); diff != "" {
			t.Fatalf("state mismatch (-want +got):\n%s", diff)
		}
	})

	t.Run("purity: input state maps and pointers are not mutated", func(t *testing.T) {
		t.Parallel()
		initial := NewState()
		initial.RunID = evRunID
		initial.Blocks[evBlockID] = &Block{BlockerID: evBlockID, Kind: WaitReasonVaultLocked, Reference: "r", ResumeCondition: "c"}
		ev := &RunBlockClearedEvent{Envelope: env, BlockerID: evBlockID, ResolvingEvidence: "open"}
		next := Evolve(initial, ev)
		if len(initial.Blocks) != 1 || len(next.Blocks) != 0 {
			t.Fatalf("expected initial.Blocks length 1, next.Blocks length 0")
		}
	})

	t.Run("evolve covers all 23 run and step events", func(t *testing.T) {
		t.Parallel()
		s := testEvolveRunLifecycleEvents(t, &env, now)
		s = testEvolveRunBlocksAndRecovery(t, s, &env)
		s = testEvolveRunClosuresAndBoundaries(t, s, &env, now)
		testEvolveStepEvents(t, s, &env, now)
	})
}
