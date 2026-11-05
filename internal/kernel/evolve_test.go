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
	evDescriptorVer = "1.0"
	evToolQ         = "tc-q"
	evAttemptQ      = "ta-q"
	evCmdQ          = "cmd-q"
	evModelQ        = "m-inv"
)

func testEvolveRunLifecycleEvents(t *testing.T, env *Envelope, now time.Time) *State {
	t.Helper()
	s := NewState()
	s = Evolve(s, &RunAcceptedEvent{
		Envelope: *env, InputRef: evInput, SessionID: evSession, QueueItemRef: "q-1",
		ProtocolBinding: "proto", Budgets: DefaultRunBudgets(), PriorityClass: "default",
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
		Envelope: *env, BlockerID: evBlockID, Kind: WaitReasonVaultLocked, BudgetKind: "", Reference: "vault", ResumeCondition: "unlocked",
	})
	if len(s.Blocks) != 1 {
		t.Fatalf("RunBlockAdded failed")
	}

	s = Evolve(s, &RunBlockClearedEvent{Envelope: *env, BlockerID: evBlockID, ResolvingEvidence: "open"})
	if len(s.Blocks) != 0 {
		t.Fatalf("RunBlockCleared failed")
	}

	s = Evolve(s, &RunInterruptedEvent{Envelope: *env, CauseID: evCause, Cause: evCause, AttemptRef: evAttempt1, PayloadRef: evPay1})
	if len(s.InterruptionCauses) != 1 {
		t.Fatalf("RunInterrupted failed")
	}

	s = Evolve(s, &RunRecoveryClearedEvent{
		Envelope: *env, CauseIDs: []string{evCause}, OwnerEvidence: "ok", Disposition: RecoveryDispositionReconciled, NextAction: NextActionClose,
	})
	if len(s.InterruptionCauses) != 0 {
		t.Fatalf("RunRecoveryCleared failed")
	}
	return s
}

func testEvolveRunClosuresAndBoundaries(t *testing.T, s *State, env *Envelope, now time.Time) *State {
	t.Helper()
	s = Evolve(s, &UnknownAcceptedEvent{
		Envelope: *env, TargetAttempts: []string{evAttempt1}, Revisions: []uint64{1}, Actor: evActor, AcceptedUncertainty: "un", Disposition: ControlDispositionClose,
	})

	s.InterruptionCauses[evCause1] = &InterruptionCause{CauseID: evCause1, Cause: evVaultLock, AttemptRef: "", PayloadRef: ""}
	s.ModelInvocations["inv-m-close"] = &ModelInvocation{
		InvocationID: "inv-m-close", Planned: true,
		Attempts: map[string]*ModelAttempt{
			evModelCloseAtt: {AttemptID: evModelCloseAtt, Status: ModelAttemptStatusRequesting},
		},
	}
	s.ToolInvocations["tc-close"] = &ToolInvocation{
		ToolCallID: "tc-close", DescriptorVersion: evDescriptorVer, EffectClass: EffectClassSideEffecting,
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
		Envelope: *env, Outcome: RunOutcomeCompleted, Truncated: false, FailureCode: "", PrimaryStopRef: "stop",
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

	s = Evolve(s, &StepCompletedEvent{Envelope: *env, StepIndex: 0, ResponseRef: evResp, OrderedResultRefs: []string{"r"}, Continuation: StepContinuationContinue, Reason: ContinuationReasonToolCalls, BudgetKind: ""})
	if !s.StepCompleted || s.StepContinuation != StepContinuationContinue {
		t.Fatalf("StepCompleted failed")
	}

	s = Evolve(s, &BudgetExtendedEvent{Envelope: *env, Kind: "steps", ExtensionAmount: 10, Actor: evActor})
	s = Evolve(s, &StepAbortedEvent{Envelope: *env, StepIndex: 0, Reason: "stop", Dispositions: map[string]string{evAttempt1: evCancelled}})
	if !s.StepAborted || s.StepCompleted {
		t.Fatalf("StepAborted failed")
	}
}

func TestEvolvePurity(t *testing.T) {
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

	t.Run("typed nil event returns cloned state with identical content", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s.RunID = evRunID
		var ev *RunFinishedEvent
		var e Event = ev
		got := Evolve(s, e)
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
}

func TestEvolveNilMapValues(t *testing.T) {
	t.Parallel()
	s := NewState()
	s.Blocks["nil_block"] = nil
	s.ActiveTimers["nil_timer"] = nil
	s.ModelInvocations["nil_model"] = nil
	s.ToolInvocations["nil_tool"] = nil
	s.InterruptionCauses["nil_cause"] = nil

	got := Evolve(s, nil)
	if got == s {
		t.Fatalf("expected different pointer")
	}
	_ = DeriveLifecycle(s)
	_ = DeriveWaitReasons(s)
	_ = DerivePhase(s)
}

func TestEvolveQuiesceLifecycle(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)
	s := NewState()
	s.RunID = evRunID
	s.Admitted = true

	s = Evolve(s, &ToolCallPlannedEvent{
		Envelope: env, ToolCallID: evToolQ, SourcePosition: 0, DescriptorVersion: evDescriptorVer,
		EffectClass: EffectClassSideEffecting, FirstAttemptID: evAttemptQ, InputRef: "", Terminates: false, RepeatsPrevious: false,
	})
	s = Evolve(s, &ToolDispatchStartedEvent{
		Envelope: env, ToolCallID: evToolQ, AttemptID: evAttemptQ, CheckGeneration: 1,
		ExecutorFence: 1, Token: 7, ReceiptRef: "r", BudgetReservation: "b",
	})
	if DeriveLifecycle(s) != LifecycleRunning {
		t.Fatalf("expected running after dispatch")
	}

	s = Evolve(s, &InvocationQuiesceRequestedEvent{
		Envelope: env, InvocationID: "inv-q", AttemptID: evAttemptQ,
		Reason: QuiesceReasonStop, CommandRef: evCmdQ,
	})
	if !s.QuiescingAttempts[evAttemptQ] {
		t.Fatalf("expected attempt in QuiescingAttempts")
	}
	if DeriveLifecycle(s) != LifecycleRunning {
		t.Fatalf("expected running while attempt is quiescing")
	}

	s = Evolve(s, &ToolResultRecordedEvent{
		Envelope: env, ToolCallID: evToolQ, AttemptID: evAttemptQ,
		Outcome: ToolResultOutcomeCancelled, ResultRef: "res-q", Code: "cancelled",
		EffectEvidence: "none", SourcePosition: 0,
	})
	if s.QuiescingAttempts[evAttemptQ] {
		t.Fatalf("expected QuiescingAttempts to be cleared after cancellation")
	}

	s = Evolve(s, &RunBlockAddedEvent{
		Envelope: env, BlockerID: "b-vault", Kind: WaitReasonVaultLocked, BudgetKind: "",
		Reference: "ref-v", ResumeCondition: "unlock",
	})
	if got := DeriveLifecycle(s); got != LifecycleWaiting {
		t.Fatalf("expected LifecycleWaiting after quiesced tool cancellation with block, got %s", got)
	}
	reasons := DeriveWaitReasons(s)
	if len(reasons) != 1 || reasons[0] != WaitReasonVaultLocked {
		t.Fatalf("expected [WaitReasonVaultLocked], got %v", reasons)
	}
}

func TestEvolveQuiesceGhostAttemptIgnored(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)
	s := NewState()
	s.RunID = evRunID
	s.Admitted = true
	s.Blocks["b-vault"] = &Block{
		BlockerID: "b-vault", Kind: WaitReasonVaultLocked,
		Reference: "ref-v", ResumeCondition: "unlock",
	}

	s = Evolve(s, &InvocationQuiesceRequestedEvent{
		Envelope: env, InvocationID: "inv-q", AttemptID: "ghost-attempt",
		Reason: QuiesceReasonStop, CommandRef: "cmd-ghost",
	})
	if s.QuiescingAttempts["ghost-attempt"] {
		t.Fatalf("expected ghost attempt NOT to be added to QuiescingAttempts")
	}
	if got := DeriveLifecycle(s); got != LifecycleWaiting {
		t.Fatalf("expected LifecycleWaiting preserved despite ghost quiesce, got %s", got)
	}
}

func TestEvolveQuiesceModelAttempt(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)
	s := NewState()
	s.RunID = evRunID
	s.Admitted = true

	s = Evolve(s, &ModelRequestPlannedEvent{
		Envelope: env, InvocationID: evModelQ, Purpose: "p",
		FirstAttemptID: "m-att-1", ModelRef: "gpt-4", ContextRef: "c",
		ToolDeclarationRefs: nil, EstimatedBudgetTokens: 100,
	})
	s = Evolve(s, &InvocationQuiesceRequestedEvent{
		Envelope: env, InvocationID: evModelQ, AttemptID: "m-att-1",
		Reason: QuiesceReasonStop, CommandRef: evCmdQ,
	})
	if !s.QuiescingAttempts["m-att-1"] {
		t.Fatalf("expected model attempt in QuiescingAttempts")
	}
	s = Evolve(s, &InvocationQuiesceRequestedEvent{
		Envelope: env, InvocationID: evModelQ, AttemptID: "",
		Reason: QuiesceReasonStop, CommandRef: evCmdQ,
	})
	if s.QuiescingAttempts[""] {
		t.Fatalf("expected empty attemptID not in QuiescingAttempts")
	}
}

func TestEvolveInterruptionCauses(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)
	s := NewState()
	s.RunID = evRunID
	s.Admitted = true

	s = Evolve(s, &RunInterruptedEvent{
		Envelope: env, CauseID: "cause-1", Cause: evCause, AttemptRef: "att-1", PayloadRef: "pay-1",
	})
	s = Evolve(s, &RunInterruptedEvent{
		Envelope: env, CauseID: "cause-2", Cause: evCause, AttemptRef: "att-2", PayloadRef: "pay-2",
	})

	if len(s.InterruptionCauses) != 2 {
		t.Fatalf("expected 2 distinct interruption causes, got %d", len(s.InterruptionCauses))
	}
	if s.InterruptionCauses["cause-1"].AttemptRef != "att-1" || s.InterruptionCauses["cause-2"].AttemptRef != "att-2" {
		t.Fatalf("attempt refs corrupted across causes")
	}
	if DeriveLifecycle(s) != LifecycleInterrupted {
		t.Fatalf("expected LifecycleInterrupted")
	}

	s = Evolve(s, &RunRecoveryClearedEvent{
		Envelope: env, CauseIDs: []string{"cause-1"}, OwnerEvidence: "ev",
		Disposition: RecoveryDispositionReconciled, NextAction: NextActionClose,
	})
	if len(s.InterruptionCauses) != 1 || s.InterruptionCauses["cause-2"] == nil {
		t.Fatalf("expected cause-2 to remain after clearing cause-1")
	}
	if DeriveLifecycle(s) != LifecycleInterrupted {
		t.Fatalf("expected LifecycleInterrupted still while cause-2 remains")
	}

	s = Evolve(s, &RunRecoveryClearedEvent{
		Envelope: env, CauseIDs: []string{"cause-2"}, OwnerEvidence: "ev",
		Disposition: RecoveryDispositionReconciled, NextAction: NextActionClose,
	})
	if len(s.InterruptionCauses) != 0 {
		t.Fatalf("expected 0 interruption causes after clearing all")
	}
	if DeriveLifecycle(s) != LifecycleRunning {
		t.Fatalf("expected LifecycleRunning after all causes cleared")
	}

	s = Evolve(s, &RunInterruptedEvent{
		Envelope: env, CauseID: "", Cause: "effect_unknown", AttemptRef: "att-empty", PayloadRef: "pay-empty",
	})
	if len(s.InterruptionCauses) != 0 {
		t.Fatalf("expected empty CauseID to be ignored")
	}
}

func TestEvolveIdempotenceAndMonotonicity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	t.Run("tool call planned is idempotent and sequence tracks monotonically", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s.RunID = evRunID

		env1 := testEnvelope(evRunID, 10, now)
		s = Evolve(s, &ToolCallPlannedEvent{
			Envelope: env1, ToolCallID: "tc-idem", SourcePosition: 0, DescriptorVersion: evDescriptorVer,
			EffectClass: EffectClassPureRead, FirstAttemptID: "att-idem-1", InputRef: "", Terminates: false, RepeatsPrevious: false,
		})
		if s.LastSequence != 10 {
			t.Fatalf("expected LastSequence 10, got %d", s.LastSequence)
		}
		if len(s.ToolOrder) != 1 {
			t.Fatalf("expected ToolOrder length 1, got %d", len(s.ToolOrder))
		}

		env2 := testEnvelope(evRunID, 11, now)
		s = Evolve(s, &ToolCallPlannedEvent{
			Envelope: env2, ToolCallID: "tc-idem", SourcePosition: 0, DescriptorVersion: evDescriptorVer,
			EffectClass: EffectClassPureRead, FirstAttemptID: "att-idem-1", InputRef: "", Terminates: false, RepeatsPrevious: false,
		})
		if s.LastSequence != 11 {
			t.Fatalf("expected LastSequence 11, got %d", s.LastSequence)
		}
		if len(s.ToolOrder) != 1 {
			t.Fatalf("expected ToolOrder length still 1 (no duplicate), got %d", len(s.ToolOrder))
		}
	})

	t.Run("run accepted does not overwrite existing run ID", func(t *testing.T) {
		t.Parallel()
		s := NewState()
		s = Evolve(s, &RunAcceptedEvent{
			Envelope: testEnvelope("run-orig", 1, now),
			InputRef: "", SessionID: "", QueueItemRef: "", ProtocolBinding: "",
			Budgets: DefaultRunBudgets(), PriorityClass: "", PeerOutboxRef: "", GoalID: "", GoalRound: 0,
		})
		if s.RunID != "run-orig" {
			t.Fatalf("expected RunID run-orig, got %s", s.RunID)
		}
		s = Evolve(s, &RunAcceptedEvent{
			Envelope: testEnvelope("run-other", 2, now),
			InputRef: "", SessionID: "", QueueItemRef: "", ProtocolBinding: "",
			Budgets: DefaultRunBudgets(), PriorityClass: "", PeerOutboxRef: "", GoalID: "", GoalRound: 0,
		})
		if s.RunID != "run-orig" {
			t.Fatalf("expected RunID to remain run-orig, got %s", s.RunID)
		}
	})
}

func TestEvolveRunAndStepCoverage(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(evRunID, 1, now)
	s := testEvolveRunLifecycleEvents(t, &env, now)
	s = testEvolveRunBlocksAndRecovery(t, s, &env)
	s = testEvolveRunClosuresAndBoundaries(t, s, &env, now)
	testEvolveStepEvents(t, s, &env, now)
}
