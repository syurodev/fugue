package kernel

import (
	"testing"
	"time"
)

const (
	testModelInvID    = "inv-1"
	testModelAtt1     = "att-1"
	testModelAtt2     = "att-2"
	testToolCallID    = "tc-1"
	testToolAtt1      = "tatt-1"
	testToolAtt2      = "tatt-2"
	testToolAtt3      = "tatt-3"
	testTimer1        = "t-1"
	testTimer2        = "t-2"
	testWindowID      = "win-1"
	testActionClose   = NextActionClose
	testActionRetry   = NextActionRetry
	testOutcomeSucc   = ToolResultOutcomeSucceeded
	testOutcomeFail   = ToolResultOutcomeFailed
	testInputRef      = "inp"
	testModelInvRef   = "inv"
	testResponseRef   = "resp"
	testBasisMeasured = "measured"
	testAttFail       = "a-fail"
	testAttDen        = "a-den"
	testAttRef        = "a-ref"
	testAttCan        = "a-can"
	testAttAbn        = "a-abn"
	testAttRec        = "att-rec"
	testAttT2         = "att-t2"
	testCancelled     = "cancelled"
)

func testEvolveModelDispatch(t *testing.T, s *State, env *Envelope) *State {
	t.Helper()
	s = Evolve(s, &ModelRequestPlannedEvent{
		Envelope: *env, InvocationID: testModelInvID, Purpose: "turn", FirstAttemptID: testModelAtt1,
		ModelRef: "gpt-4", ContextRef: "ctx-1", ToolDeclarationRefs: []string{"tool-1"}, EstimatedBudgetTokens: 1000,
	})
	if s.ModelInvocations[testModelInvID] == nil || !s.ModelInvocations[testModelInvID].Planned || s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusPlanned {
		t.Fatalf("ModelRequestPlanned failed")
	}

	s = Evolve(s, &ModelDispatchStartedEvent{
		Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, Token: 7, ExecutorFence: 1,
		ReservationTokens: 500, ProviderSettingsRef: "set", AccountID: "acct", CatalogVersion: "v1",
	})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusRequesting {
		t.Fatalf("ModelDispatchStarted failed")
	}

	s = Evolve(s, &ModelStreamStartedEvent{Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, FirstChunkRef: "chunk"})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusStreaming {
		t.Fatalf("ModelStreamStarted failed")
	}

	s = Evolve(s, &ModelResponseRecordedEvent{Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, ResponseRef: testResponseRef, UsageStatus: "ok"})
	inv := s.ModelInvocations[testModelInvID]
	if inv.Attempts[testModelAtt1].Status != ModelAttemptStatusCompleted || !inv.Completed || inv.SelectedAttemptID != testModelAtt1 {
		t.Fatalf("ModelResponseRecorded failed")
	}
	return s
}

func testEvolveModelErrorsAndRetries(t *testing.T, s *State, env *Envelope) *State {
	t.Helper()
	s = Evolve(s, &ModelRequestFailedEvent{
		Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, Stage: "dispatch",
		FailureClass: "rate_limit", FailureCode: "429", RetryDisposition: "retryable", UsageStatus: "charged", PartialRef: "",
	})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusFailed {
		t.Fatalf("ModelRequestFailed failed")
	}

	s = Evolve(s, &ModelRequestInterruptedEvent{
		Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, UncertaintyClass: "net_drop",
		PartialRef: "part", UsageReservationTokens: 200,
	})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusInterrupted {
		t.Fatalf("ModelRequestInterrupted failed")
	}

	s = Evolve(s, &ModelRetryScheduledEvent{
		Envelope: *env, InvocationID: testModelInvID, PreviousAttemptID: testModelAtt1, NewAttemptID: testModelAtt2,
		PolicyVersion: "v1", RetryNumber: 1, TimerID: testTimer1,
	})
	s = Evolve(s, &ModelAttemptPlannedEvent{Envelope: *env, InvocationID: testModelInvID, AttemptID: testModelAtt2, PreviousAttemptID: testModelAtt1, EstimatedBudgetTokens: 1000})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt2] == nil || s.ModelInvocations[testModelInvID].Attempts[testModelAtt2].Status != ModelAttemptStatusPlanned {
		t.Fatalf("ModelAttemptPlanned failed")
	}

	s = Evolve(s, &ModelAttemptCancelledEvent{Envelope: *env, AttemptID: testModelAtt2, InvocationID: testModelInvID, Stage: "pre_dispatch", Evidence: testCancelled})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt2].Status != ModelAttemptStatusCancelled {
		t.Fatalf("ModelAttemptCancelled failed")
	}

	s = Evolve(s, &ModelAttemptAbandonedEvent{
		Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, Uncertainty: "un", Receipt: "rc", NextAction: testActionClose, UnknownCost: 100,
	})
	if s.ModelInvocations[testModelInvID].Attempts[testModelAtt1].Status != ModelAttemptStatusAbandonedUnknown || !s.ModelInvocations[testModelInvID].Closed {
		t.Fatalf("ModelAttemptAbandoned failed")
	}

	s = Evolve(s, &ModelInvocationFailedEvent{Envelope: *env, InvocationID: testModelInvID, FinalFailureRef: "fail", Decision: "fatal"})
	if !s.ModelInvocations[testModelInvID].Failed || !s.ModelInvocations[testModelInvID].Closed {
		t.Fatalf("ModelInvocationFailed failed")
	}

	s = Evolve(s, &ModelUsageReconciledEvent{Envelope: *env, AttemptID: testModelAtt1, InvocationID: testModelInvID, AccountingEvidence: "ev", OldReservationTokens: 500, ObservedTokens: 450})
	if s.ModelInvocations[testModelInvID] == nil {
		t.Fatalf("ModelUsageReconciled failed")
	}
	return s
}

func testEvolveToolChecks(t *testing.T, s *State, env *Envelope, now time.Time) *State {
	t.Helper()
	s = Evolve(s, &ToolCallPlannedEvent{
		Envelope: *env, ToolCallID: testToolCallID, SourcePosition: 0, DescriptorVersion: testDescriptorVer,
		EffectClass: EffectClassSideEffecting, FirstAttemptID: testToolAtt1, InputRef: testInputRef,
	})
	if s.ToolInvocations[testToolCallID] == nil || s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusPlanned {
		t.Fatalf("ToolCallPlanned failed")
	}

	s = Evolve(s, &ToolCheckStartedEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, CheckGeneration: 1, FrontierCursor: 0})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusChecking || s.ToolInvocations[testToolCallID].CheckGeneration != 1 {
		t.Fatalf("ToolCheckStarted failed")
	}

	s = Evolve(s, &ToolApprovalRequestedEvent{
		Envelope: *env, ToolCallID: testToolCallID, CheckGeneration: 1, OwnerRequestRef: "req",
		RequestRevision: 1, ExactArgsRef: "args", ApproverRoute: "user", Expiry: now.Add(time.Hour),
	})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusAwaitingApproval {
		t.Fatalf("ToolApprovalRequested failed")
	}

	s.ToolInvocations[testToolCallID].Attempts["att-chk"] = &ToolAttempt{AttemptID: "att-chk", Status: ToolAttemptStatusChecking}
	s.ToolInvocations[testToolCallID].Attempts["att-rdy"] = &ToolAttempt{AttemptID: "att-rdy", Status: ToolAttemptStatusReady}
	s = Evolve(s, &ToolCheckResetEvent{
		Envelope: *env, ToolCallID: testToolCallID, OldGeneration: 1, NewGeneration: 2,
		OwnerInvalidationReceipt: testModelInvRef, ResolvedDependencyRefs: []string{"dep"},
	})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusPlanned ||
		s.ToolInvocations[testToolCallID].Attempts["att-chk"].Status != ToolAttemptStatusPlanned ||
		s.ToolInvocations[testToolCallID].Attempts["att-rdy"].Status != ToolAttemptStatusPlanned ||
		s.ToolInvocations[testToolCallID].CheckGeneration != 2 {
		t.Fatalf("ToolCheckReset failed")
	}

	s = Evolve(s, &ToolCheckStartedEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, CheckGeneration: 2, FrontierCursor: 0})
	s = Evolve(s, &ToolReadyRecordedEvent{
		Envelope: *env, ToolCallID: testToolCallID, CheckGeneration: 2, ValidatedArgsRef: "vargs", GateObservation: "ok", OneUseApprovalReceiptRef: "rc",
	})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusReady {
		t.Fatalf("ToolReadyRecorded failed")
	}
	return s
}

func testEvolveToolExecutionAndOutcomes(t *testing.T, s *State, env *Envelope) {
	t.Helper()
	s = Evolve(s, &ToolDispatchStartedEvent{
		Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, CheckGeneration: 2,
		ExecutorFence: 1, Token: 7, ReceiptRef: "rc", BudgetReservation: "res",
	})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusDispatching {
		t.Fatalf("ToolDispatchStarted failed")
	}

	s = Evolve(s, &ToolOutcomeUnknownEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, CauseClass: "crash", PossibleExposure: "unc", EvidenceRefs: nil})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusOutcomeUnknown {
		t.Fatalf("ToolOutcomeUnknown failed")
	}

	s = Evolve(s, &ToolReconciledEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, OperationReceipt: "op", ObservedOutcome: ToolOutcomeUnknownObservedDisconnected})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt1].Status != ToolAttemptStatusSucceeded {
		t.Fatalf("ToolReconciled failed")
	}

	s = Evolve(s, &ToolAttemptPlannedEvent{Envelope: *env, ToolCallID: testToolCallID, NewAttemptID: testToolAtt2, PreviousAttemptID: testToolAtt1, InputRef: "i", IdempotencyKey: "k"})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt2] == nil || s.ToolInvocations[testToolCallID].Attempts[testToolAtt2].Status != ToolAttemptStatusPlanned {
		t.Fatalf("ToolAttemptPlanned failed")
	}
	s = Evolve(s, &ToolAttemptAbandonedEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt2, DispositionRef: "d", NextAction: testActionRetry})
	s = Evolve(s, &InvocationQuiesceRequestedEvent{Envelope: *env, InvocationID: testModelInvRef, AttemptID: testToolAtt1, Reason: QuiesceReasonStop, CommandRef: "c"})
	if !s.QuiescingAttempts[testToolAtt1] {
		t.Fatalf("InvocationQuiesceRequested failed")
	}

	s = Evolve(s, &ToolResultRecordedEvent{
		Envelope: *env, ToolCallID: testToolCallID, AttemptID: testToolAtt1, Outcome: testOutcomeSucc, ResultRef: "r", Code: "200", EffectEvidence: "e", SourcePosition: 0,
	})
	if !s.ToolInvocations[testToolCallID].Closed || s.ToolInvocations[testToolCallID].SelectedAttemptID != testToolAtt1 {
		t.Fatalf("ToolResultRecorded failed")
	}
	if s.QuiescingAttempts[testToolAtt1] {
		t.Fatalf("expected QuiescingAttempts to be cleared after ToolResultRecorded")
	}

	s.ToolInvocations[testToolCallID].Attempts[testToolAtt3] = &ToolAttempt{AttemptID: testToolAtt3, Status: ToolAttemptStatusAwaitingApproval}
	s = Evolve(s, &ToolApprovalResolvedEvent{Envelope: *env, ToolCallID: testToolCallID, CheckGeneration: 2, OwnerResolutionEvidence: "den", Resolution: "denied"})
	if s.ToolInvocations[testToolCallID].Attempts[testToolAtt3].Status != ToolAttemptStatusDenied {
		t.Fatalf("ToolApprovalResolved denied failed")
	}
}

func testEvolveTimersAndBudgets(t *testing.T, s *State, env *Envelope, now time.Time) {
	t.Helper()
	s = Evolve(s, &TimeBudgetWindowOpenedEvent{
		Envelope: *env, WindowID: testWindowID, Generation: 1, Incarnation: 1, TrancheReservationDuration: time.Minute, Policy: "std", MonotonicBaselineNano: 10,
	})

	s = Evolve(s, &TimerScheduledEvent{
		Envelope: *env, TimerID: testTimer1, Generation: 1, Purpose: TimerPurposeRunDeadline, TargetID: testWindowID,
		ClockDomain: "monotonic", SchedulingDeadline: now.Add(time.Minute), Duration: time.Minute, PolicyVersion: "v1",
	})
	if s.ActiveTimers[testTimer1] == nil {
		t.Fatalf("TimerScheduled failed")
	}

	s = Evolve(s, &BudgetObservedEvent{Envelope: *env, AttemptID: "att", WindowID: testWindowID, TokensUsed: 10, DurationUsed: time.Second, Basis: testBasisMeasured})
	s = Evolve(s, &TimerElapsedEvent{Envelope: *env, TimerID: testTimer1, Generation: 1, ObservedTime: now.Add(time.Minute)})
	if len(s.ActiveTimers) != 0 {
		t.Fatalf("TimerElapsed failed")
	}

	s = Evolve(s, &TimerScheduledEvent{
		Envelope: *env, TimerID: testTimer2, Generation: 1, Purpose: TimerPurposeModelRetry, TargetID: testModelInvRef,
		ClockDomain: "utc", SchedulingDeadline: now.Add(time.Second), Duration: time.Second, PolicyVersion: "v1",
	})
	s = Evolve(s, &TimerCancelledEvent{Envelope: *env, TimerID: testTimer2, Generation: 1, CauseRef: "cancel"})
	if len(s.ActiveTimers) != 0 {
		t.Fatalf("TimerCancelled failed")
	}

	s = Evolve(s, &TimeBudgetWindowClosedEvent{
		Envelope: *env, WindowID: testWindowID, MeasuredElapsed: time.Second, TailCoverage: testBasisMeasured, ReservationDisposition: "released",
	})
	if len(s.ActiveTimers) != 0 {
		t.Fatalf("TimeBudgetWindowClosed check failed")
	}
}

func testToolResultOutcomes(t *testing.T, env *Envelope) {
	t.Helper()
	s := NewState()
	s.ToolInvocations[testToolCallID] = &ToolInvocation{
		ToolCallID: testToolCallID, DescriptorVersion: testDescriptorVer, EffectClass: EffectClassSideEffecting,
		Attempts: map[string]*ToolAttempt{
			testAttFail: {AttemptID: testAttFail, Status: ToolAttemptStatusDispatching},
			testAttDen:  {AttemptID: testAttDen, Status: ToolAttemptStatusDispatching},
			testAttRef:  {AttemptID: testAttRef, Status: ToolAttemptStatusDispatching},
			testAttCan:  {AttemptID: testAttCan, Status: ToolAttemptStatusDispatching},
			testAttAbn:  {AttemptID: testAttAbn, Status: ToolAttemptStatusDispatching},
		},
	}

	cases := []struct {
		id      string
		outcome ToolResultOutcome
		want    ToolAttemptStatus
	}{
		{testAttFail, testOutcomeFail, ToolAttemptStatusFailed},
		{testAttDen, ToolResultOutcomeDenied, ToolAttemptStatusDenied},
		{testAttRef, ToolResultOutcomeRefused, ToolAttemptStatusRefused},
		{testAttCan, ToolResultOutcomeCancelled, ToolAttemptStatusCancelled},
		{testAttAbn, ToolResultOutcomeAbandonedUnknown, ToolAttemptStatusAbandonedUnknown},
	}

	for _, tc := range cases {
		s = Evolve(s, &ToolResultRecordedEvent{
			Envelope: *env, ToolCallID: testToolCallID, AttemptID: tc.id, Outcome: tc.outcome, ResultRef: "r", Code: "c", EffectEvidence: "e", SourcePosition: 0,
		})
		if s.ToolInvocations[testToolCallID].Attempts[tc.id].Status != tc.want {
			t.Fatalf("expected status %s for outcome %s", tc.want, tc.outcome)
		}
	}
}

func testToolRecoveryDispositions(t *testing.T, env *Envelope) {
	t.Helper()
	s := NewState()
	s.ToolInvocations[testToolCallID] = &ToolInvocation{
		ToolCallID: testToolCallID, DescriptorVersion: testDescriptorVer, EffectClass: EffectClassSideEffecting,
		Attempts: map[string]*ToolAttempt{
			testAttRec: {AttemptID: testAttRec, Status: ToolAttemptStatusOutcomeUnknown},
		},
	}

	s = Evolve(s, &ToolReconciledEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testAttRec, OperationReceipt: "o", ObservedOutcome: ToolOutcomeUnknownObservedProcessLost})
	if s.ToolInvocations[testToolCallID].Attempts[testAttRec].Status != ToolAttemptStatusFailed {
		t.Fatalf("expected tool reconciled failed")
	}

	s = Evolve(s, &ToolAttemptAbandonedEvent{Envelope: *env, ToolCallID: testToolCallID, AttemptID: testAttRec, DispositionRef: "d", NextAction: testActionClose})
	if !s.ToolInvocations[testToolCallID].Closed {
		t.Fatalf("expected tool closed on abandon close")
	}

	s.ToolInvocations["tc-2"] = &ToolInvocation{
		ToolCallID: "tc-2", DescriptorVersion: testDescriptorVer, EffectClass: EffectClassPureRead,
		Attempts: map[string]*ToolAttempt{testAttT2: {AttemptID: testAttT2, Status: ToolAttemptStatusOutcomeUnknown}},
	}
	s = Evolve(s, &UnknownAcceptedEvent{Envelope: *env, TargetAttempts: []string{testAttT2}, Revisions: []uint64{1}, Actor: "u", AcceptedUncertainty: "un", Disposition: ControlDispositionClose})
	if s.ToolInvocations["tc-2"].Attempts[testAttT2].Status != ToolAttemptStatusAbandonedUnknown {
		t.Fatalf("expected tool attempt abandoned unknown")
	}

	s = Evolve(s, &RunClosureConfirmedEvent{
		Envelope: *env, MetadataOnlyPreviewVersion: "v1", RunRevision: 3, ExactAttemptIDs: []string{testAttT2},
		CauseIDs: nil, Actor: "u", UnavailabilityReason: "payload_corrupt", PreviousContextRevision: 1, NewContextRevision: 2,
		ResultingContextRequirement: ContextRequirementFreshRequired, CauseEventRef: "", AttemptDispositions: map[string]AttemptDisposition{testAttT2: AttemptDispositionCancelled},
	})
	if s.ToolInvocations["tc-2"].Attempts[testAttT2].Status != ToolAttemptStatusCancelled {
		t.Fatalf("expected cancelled disposition from closure confirmation")
	}
}

func TestEvolveInvocationAndTimerEvents(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope("run-1", 1, now)

	t.Run("evolve covers all 12 model events", func(t *testing.T) {
		t.Parallel()
		s := testEvolveModelDispatch(t, NewState(), &env)
		testEvolveModelErrorsAndRetries(t, s, &env)
	})

	t.Run("evolve covers all 13 tool events", func(t *testing.T) {
		t.Parallel()
		s := testEvolveToolChecks(t, NewState(), &env, now)
		testEvolveToolExecutionAndOutcomes(t, s, &env)
	})

	t.Run("evolve covers all 6 timer and budget events", func(t *testing.T) {
		t.Parallel()
		testEvolveTimersAndBudgets(t, NewState(), &env, now)
	})

	t.Run("evolve tool results and dispositions coverage", func(t *testing.T) {
		t.Parallel()
		testToolResultOutcomes(t, &env)
		testToolRecoveryDispositions(t, &env)
	})
}
