package kernel

import (
	"strings"
	"testing"
	"time"
)

const (
	testRunID   = "run-1"
	testCmdID   = "cmd-1"
	testOrigin  = "test"
	testToken   = 7
	testAttempt = "att"
)

func TestRejection(t *testing.T) {
	t.Parallel()
	rej := Reject(RejectionCodeInvalidState, "cannot admit in current state")
	if rej.Code != RejectionCodeInvalidState {
		t.Fatalf("unexpected code: %s", rej.Code)
	}
	if !strings.Contains(rej.Error(), "invalid_state") || !strings.Contains(rej.Error(), "cannot admit") {
		t.Fatalf("unexpected error string: %s", rej.Error())
	}
}

func testCommands() []struct {
	cmd  Command
	want string
} {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	return []struct {
		cmd  Command
		want string
	}{
		{&AcceptRunCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, InputRef: "i", SessionID: "s", QueueItemRef: "q", ProtocolBinding: "p", Budgets: DefaultRunBudgets(), PriorityClass: "pc", PeerOutboxRef: "", GoalID: "", GoalRound: 0}, "AcceptRun"},
		{&AdmitRunCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, OwnerIncarnation: 1, QueueDecisionRef: "q", ContextEpoch: 1, StartRunID: testRunID, InitialContextRevision: 1}, "AdmitRun"},
		{&AcquireRunOwnershipCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, NewToken: 2, PreviousToken: 1, RecoveryReceipt: "rc", DataDirectoryLockGeneration: 1}, "AcquireRunOwnership"},
		{&AdvanceRunCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, StepIndex: 0}, "AdvanceRun"},
		{&PrepareDispatchCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, InvocationID: testModelInvRef, AttemptID: testAttempt}, "PrepareDispatch"},
		{&ObserveModelStreamCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, AttemptID: testAttempt, InvocationID: testModelInvRef, FirstChunkRef: "chunk"}, "ObserveModelStream"},
		{&ObserveModelResponseCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, AttemptID: testAttempt, InvocationID: testModelInvRef, ResponseRef: "resp", UsageStatus: "ok", FinishReason: FinishReasonStop}, "ObserveModelResponse"},
		{&ObserveModelFailureCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, AttemptID: testAttempt, InvocationID: testModelInvRef, Stage: "st", FailureClass: "c", FailureCode: "cd", RetryDisposition: "r", UsageStatus: "u", PartialRef: ""}, "ObserveModelFailure"},
		{&ObserveToolCheckCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, ToolCallID: "tc", AttemptID: testAttempt, CheckGeneration: 1, ValidatedArgsRef: "args", GateObservation: "go", ApprovalReceiptRef: "ar"}, "ObserveToolCheck"},
		{&ObserveToolApprovalCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, ToolCallID: "tc", CheckGeneration: 1, Resolution: "approved", ResolutionEvidence: "ev"}, "ObserveToolApproval"},
		{&ObserveToolResultCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, ToolCallID: "tc", AttemptID: testAttempt, Outcome: "succeeded", ResultRef: "res", Code: "200", EffectEvidence: "ev", SourcePosition: 0}, "ObserveToolResult"},
		{&RequestStopCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: 9, Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: "u", Target: "t", RequestTime: now}, "RequestStop"},
		{&ObserveVaultCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Locked: true, Evidence: "ev"}, "ObserveVault"},
		{&ObserveHostPauseCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Paused: true, Evidence: "ev"}, "ObserveHostPause"},
		{&ObserveDependencyCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, BlockerID: "b", Kind: WaitReasonVaultLocked, BudgetKind: "", Reference: "ref", ResumeCondition: "rc", Resolved: false, Evidence: "ev"}, "ObserveDependency"},
		{&TimerFiredCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, TimerID: "t", Generation: 1, ObservedTime: now}, "TimerFired"},
		{&RecoverRunCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, InterruptionRef: "ir", Evidence: "ev"}, "RecoverRun"},
		{&ReconcileInvocationCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, InvocationID: testModelInvRef, AttemptID: testAttempt, OperationReceipt: "or", ObservedOutcome: "succeeded"}, "ReconcileInvocation"},
		{&AcceptUnknownCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, TargetAttempts: []string{testAttempt}, Revisions: []uint64{1}, Actor: "u", AcceptedUncertainty: "un", Disposition: "close"}, "AcceptUnknown"},
		{&CloseRunWithoutPayloadCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, MetadataOnlyPreviewVersion: "v1", RunRevision: 1, ExactAttemptIDs: []string{testAttempt}, CauseIDs: []string{"c"}, Actor: "u", UnavailabilityReason: "vault_locked"}, "CloseRunWithoutPayload"},
		{&SelectRunContextCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, OldContextRevision: 1, NewContextRevision: 2, Epoch: 1, StartRunID: testRunID, Mode: "fresh", Actor: "u", ValidationEvidence: "ev"}, "SelectRunContext"},
		{&ResumeRunCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: testToken, Actor: "u", Evidence: "ev"}, "ResumeRun"},
		{&ObserveAccountingCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, AttemptID: testAttempt, WindowID: "win", TokensUsed: 10, DurationUsed: time.Second, Basis: "measured"}, "ObserveAccounting"},
		{&ExtendBudgetCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, Token: 9, Kind: "steps", ExtensionAmount: 5, Actor: "u"}, "ExtendBudget"},
		{&SteerInputCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, InputRef: "inp", Timestamp: now}, "SteerInput"},
		{&InterjectInputCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, InputRef: "inp", TargetAttemptID: testAttempt, TargetInvocationID: testModelInvRef}, "InterjectInput"},
		{&ReviseToolBindingCommand{CommandID: testCmdID, RunID: testRunID, Origin: testOrigin, OldRevision: 1, NewRevision: 2, AddedGroups: []string{"g"}, RemovedGroups: []string{}, DescriptorVersions: map[string]string{"t": "v1"}, Cause: "session_start"}, "ReviseToolBinding"},
	}
}

func TestCommandNames(t *testing.T) {
	t.Parallel()
	for _, tc := range testCommands() {
		if got := tc.cmd.CommandName(); got != tc.want {
			t.Fatalf("CommandName() = %s, want %s", got, tc.want)
		}
	}
}

func testEnvelope(runID string, seq uint64, now time.Time) Envelope {
	return Envelope{
		RunID:       runID,
		Sequence:    seq,
		CommandID:   testCmdID,
		CausationID: "cause-0",
		Origin:      testOrigin,
		Token:       testToken,
		Timestamp:   now,
	}
}

func TestEventNamesAndEnvelopes(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := testEnvelope(testRunID, 1, now)
	events := allTestEvents(&env, now)

	if len(events) != 54 {
		t.Fatalf("expected 54 events, got %d", len(events))
	}
	for _, tc := range events {
		if got := tc.ev.EventName(); got != tc.want {
			t.Fatalf("EventName() = %s, want %s", got, tc.want)
		}
		if tc.ev.EventEnvelope().RunID != testRunID {
			t.Fatalf("EventEnvelope().RunID = %s, want %s", tc.ev.EventEnvelope().RunID, testRunID)
		}
		if tc.ev.isNil() {
			t.Fatalf("isNil() = true for non-nil %s", tc.want)
		}
	}
}

func TestTypedNilEvents(t *testing.T) {
	t.Parallel()
	for _, ev := range allTypedNilEvents() {
		if !ev.isNil() {
			t.Fatalf("expected isNil() == true for %T", ev)
		}
		if env := ev.EventEnvelope(); env != (Envelope{}) {
			t.Fatalf("expected empty envelope for %T, got %+v", ev, env)
		}
	}
}

func allTypedNilEvents() []Event {
	return []Event{
		(*RunAcceptedEvent)(nil),
		(*RunAdmittedEvent)(nil),
		(*RunOwnershipAcquiredEvent)(nil),
		(*RunStopRequestedEvent)(nil),
		(*RunBlockAddedEvent)(nil),
		(*RunBlockClearedEvent)(nil),
		(*RunInterruptedEvent)(nil),
		(*RunRecoveryClearedEvent)(nil),
		(*UnknownAcceptedEvent)(nil),
		(*RunClosureConfirmedEvent)(nil),
		(*ContextBoundarySelectedEvent)(nil),
		(*RunFinishedEvent)(nil),
		(*StepStartedEvent)(nil),
		(*ToolBindingRevisedEvent)(nil),
		(*InstructionsLoadedEvent)(nil),
		(*ContextInjectedEvent)(nil),
		(*ToolResultsPrunedEvent)(nil),
		(*ContextCompactedEvent)(nil),
		(*InputSteeredEvent)(nil),
		(*InputInterjectedEvent)(nil),
		(*StepCompletedEvent)(nil),
		(*BudgetExtendedEvent)(nil),
		(*StepAbortedEvent)(nil),
		(*ModelRequestPlannedEvent)(nil),
		(*ModelDispatchStartedEvent)(nil),
		(*ModelStreamStartedEvent)(nil),
		(*ModelResponseRecordedEvent)(nil),
		(*ModelRequestFailedEvent)(nil),
		(*ModelRequestInterruptedEvent)(nil),
		(*ModelRetryScheduledEvent)(nil),
		(*ModelAttemptPlannedEvent)(nil),
		(*ModelAttemptCancelledEvent)(nil),
		(*ModelAttemptAbandonedEvent)(nil),
		(*ModelInvocationFailedEvent)(nil),
		(*ModelUsageReconciledEvent)(nil),
		(*ToolCallPlannedEvent)(nil),
		(*ToolCheckResetEvent)(nil),
		(*ToolCheckStartedEvent)(nil),
		(*ToolApprovalRequestedEvent)(nil),
		(*ToolApprovalResolvedEvent)(nil),
		(*ToolReadyRecordedEvent)(nil),
		(*ToolDispatchStartedEvent)(nil),
		(*ToolResultRecordedEvent)(nil),
		(*ToolOutcomeUnknownEvent)(nil),
		(*ToolReconciledEvent)(nil),
		(*ToolAttemptAbandonedEvent)(nil),
		(*ToolAttemptPlannedEvent)(nil),
		(*InvocationQuiesceRequestedEvent)(nil),
		(*TimeBudgetWindowOpenedEvent)(nil),
		(*TimeBudgetWindowClosedEvent)(nil),
		(*TimerScheduledEvent)(nil),
		(*TimerElapsedEvent)(nil),
		(*TimerCancelledEvent)(nil),
		(*BudgetObservedEvent)(nil),
	}
}

func allTestEvents(env *Envelope, now time.Time) []struct {
	ev   Event
	want string
} {
	return append(append(testRunEvents(env, now), testModelEvents(env)...), testToolAndTimerEvents(env, now)...)
}

func testRunEvents(env *Envelope, now time.Time) []struct {
	ev   Event
	want string
} {
	return []struct {
		ev   Event
		want string
	}{
		{&RunAcceptedEvent{Envelope: *env, InputRef: "", SessionID: "", QueueItemRef: "", ProtocolBinding: "", Budgets: DefaultRunBudgets(), PriorityClass: "", PeerOutboxRef: "", GoalID: "", GoalRound: 0}, "RunAccepted"},
		{&RunAdmittedEvent{Envelope: *env, Token: 0, OwnerIncarnation: 0, QueueDecisionRef: "", ContextEpoch: 0, StartRunID: "", InitialContextRevision: 0}, "RunAdmitted"},
		{&RunOwnershipAcquiredEvent{Envelope: *env, NewToken: 0, PreviousToken: 0, RecoveryReceipt: "", DataDirectoryLockGeneration: 0}, "RunOwnershipAcquired"},
		{&RunStopRequestedEvent{Envelope: *env, Reason: StopReasonUserCancel, Mode: StopModeTerminal, Actor: "", Target: "", RequestTime: now}, "RunStopRequested"},
		{&RunBlockAddedEvent{Envelope: *env, BlockerID: "", Kind: WaitReasonApproval, BudgetKind: "", Reference: "", ResumeCondition: ""}, "RunBlockAdded"},
		{&RunBlockClearedEvent{Envelope: *env, BlockerID: "", ResolvingEvidence: ""}, "RunBlockCleared"},
		{&RunInterruptedEvent{Envelope: *env, CauseID: "", Cause: "", AttemptRef: "", PayloadRef: ""}, "RunInterrupted"},
		{&RunRecoveryClearedEvent{Envelope: *env, CauseIDs: nil, OwnerEvidence: "", Disposition: RecoveryDisposition(""), NextAction: NextAction("")}, "RunRecoveryCleared"},
		{&UnknownAcceptedEvent{Envelope: *env, TargetAttempts: nil, Revisions: nil, Actor: "", AcceptedUncertainty: "", Disposition: ControlDisposition("")}, "UnknownAccepted"},
		{&RunClosureConfirmedEvent{Envelope: *env, MetadataOnlyPreviewVersion: "", RunRevision: 0, ExactAttemptIDs: nil, CauseIDs: nil, Actor: "", UnavailabilityReason: "", PreviousContextRevision: 0, NewContextRevision: 0, ResultingContextRequirement: ContextRequirementNone, CauseEventRef: "", AttemptDispositions: nil}, "RunClosureConfirmed"},
		{&ContextBoundarySelectedEvent{Envelope: *env, TargetQueuedRunID: "", OldContextRevision: 0, NewContextRevision: 0, Epoch: 0, StartRunID: "", Mode: "", Actor: "", ValidationEvidence: "", PreviousDecidingEventRef: ""}, "ContextBoundarySelected"},
		{&RunFinishedEvent{Envelope: *env, Outcome: RunOutcomeCompleted, Truncated: false, FailureCode: "", PrimaryStopRef: "", SecondaryStopRefs: nil, UnresolvedExposureRefs: nil}, "RunFinished"},
		{&StepStartedEvent{Envelope: *env, StepIndex: 0, ContextRefs: nil}, "StepStarted"},
		{&ToolBindingRevisedEvent{Envelope: *env, OldRevision: 0, NewRevision: 0, AddedGroups: nil, RemovedGroups: nil, DescriptorVersions: nil, Cause: ""}, "ToolBindingRevised"},
		{&InstructionsLoadedEvent{Envelope: *env, RuleLayer: "", SourcePath: "", SnapshotPayloadRef: "", BudgetTokens: 0, PrunedTokens: 0}, "InstructionsLoaded"},
		{&ContextInjectedEvent{Envelope: *env, Tag: "", Source: "", PayloadRef: ""}, "ContextInjected"},
		{&ToolResultsPrunedEvent{Envelope: *env, ToolResultPositions: nil, PruneParams: "", TokensSaved: 0}, "ToolResultsPruned"},
		{&ContextCompactedEvent{Envelope: *env, SummaryPayloadRef: "", FirstRetainedPosition: 0, TokensBefore: 0, TokensAfter: 0, WorkspaceMemoryRefs: nil, ModelInvocationRef: ""}, "ContextCompacted"},
		{&InputSteeredEvent{Envelope: *env, InputRef: "", Origin: "", Timestamp: now}, "InputSteered"},
		{&InputInterjectedEvent{Envelope: *env, InputRef: "", MergedSteerRefs: nil, TargetAttemptID: "", TargetInvocationID: "", QuiesceResult: ""}, "InputInterjected"},
		{&StepCompletedEvent{Envelope: *env, StepIndex: 0, ResponseRef: "", OrderedResultRefs: nil, Continuation: StepContinuationEnd, Reason: ContinuationReasonToolCalls, BudgetKind: ""}, "StepCompleted"},
		{&BudgetExtendedEvent{Envelope: *env, Kind: "", ExtensionAmount: 0, Actor: ""}, "BudgetExtended"},
		{&StepAbortedEvent{Envelope: *env, StepIndex: 0, Reason: "", Dispositions: nil}, "StepAborted"},
	}
}

func testModelEvents(env *Envelope) []struct {
	ev   Event
	want string
} {
	return []struct {
		ev   Event
		want string
	}{
		{&ModelRequestPlannedEvent{Envelope: *env, InvocationID: "", Purpose: "", FirstAttemptID: "", ModelRef: "", ContextRef: "", ToolDeclarationRefs: nil, EstimatedBudgetTokens: 0}, "ModelRequestPlanned"},
		{&ModelDispatchStartedEvent{Envelope: *env, AttemptID: "", InvocationID: "", Token: 0, ExecutorFence: 0, ReservationTokens: 0, ProviderSettingsRef: "", AccountID: "", CatalogVersion: ""}, "ModelDispatchStarted"},
		{&ModelStreamStartedEvent{Envelope: *env, AttemptID: "", InvocationID: "", FirstChunkRef: ""}, "ModelStreamStarted"},
		{&ModelResponseRecordedEvent{Envelope: *env, AttemptID: "", InvocationID: "", ResponseRef: "", UsageStatus: "", FinishReason: FinishReasonStop}, "ModelResponseRecorded"},
		{&ModelRequestFailedEvent{Envelope: *env, AttemptID: "", InvocationID: "", Stage: "", FailureClass: "", FailureCode: "", RetryDisposition: "", UsageStatus: "", PartialRef: ""}, "ModelRequestFailed"},
		{&ModelRequestInterruptedEvent{Envelope: *env, AttemptID: "", InvocationID: "", UncertaintyClass: "", PartialRef: "", UsageReservationTokens: 0}, "ModelRequestInterrupted"},
		{&ModelRetryScheduledEvent{Envelope: *env, InvocationID: "", PreviousAttemptID: "", NewAttemptID: "", PolicyVersion: "", RetryNumber: 0, TimerID: ""}, "ModelRetryScheduled"},
		{&ModelAttemptPlannedEvent{Envelope: *env, InvocationID: "", AttemptID: "", PreviousAttemptID: "", EstimatedBudgetTokens: 0}, "ModelAttemptPlanned"},
		{&ModelAttemptCancelledEvent{Envelope: *env, AttemptID: "", InvocationID: "", Stage: "", Evidence: ""}, "ModelAttemptCancelled"},
		{&ModelAttemptAbandonedEvent{Envelope: *env, AttemptID: "", InvocationID: "", Uncertainty: "", Receipt: "", NextAction: NextAction(""), UnknownCost: 0}, "ModelAttemptAbandoned"},
		{&ModelInvocationFailedEvent{Envelope: *env, InvocationID: "", FinalFailureRef: "", Decision: ""}, "ModelInvocationFailed"},
		{&ModelUsageReconciledEvent{Envelope: *env, AttemptID: "", InvocationID: "", AccountingEvidence: "", OldReservationTokens: 0, ObservedTokens: 0}, "ModelUsageReconciled"},
	}
}

func testToolAndTimerEvents(env *Envelope, now time.Time) []struct {
	ev   Event
	want string
} {
	return []struct {
		ev   Event
		want string
	}{
		{&ToolCallPlannedEvent{Envelope: *env, ToolCallID: "", SourcePosition: 0, DescriptorVersion: "", EffectClass: EffectClassPureRead, FirstAttemptID: "", InputRef: "", Terminates: false, RepeatsPrevious: false}, "ToolCallPlanned"},
		{&ToolCheckResetEvent{Envelope: *env, ToolCallID: "", OldGeneration: 0, NewGeneration: 0, OwnerInvalidationReceipt: "", ResolvedDependencyRefs: nil}, "ToolCheckReset"},
		{&ToolCheckStartedEvent{Envelope: *env, ToolCallID: "", AttemptID: "", CheckGeneration: 0, FrontierCursor: 0}, "ToolCheckStarted"},
		{&ToolApprovalRequestedEvent{Envelope: *env, ToolCallID: "", CheckGeneration: 0, OwnerRequestRef: "", RequestKind: "", RequestRevision: 0, ExactArgsRef: "", ApproverRoute: "", Expiry: now}, "ToolApprovalRequested"},
		{&ToolApprovalResolvedEvent{Envelope: *env, ToolCallID: "", CheckGeneration: 0, OwnerResolutionEvidence: "", Resolution: ""}, "ToolApprovalResolved"},
		{&ToolReadyRecordedEvent{Envelope: *env, ToolCallID: "", CheckGeneration: 0, ValidatedArgsRef: "", GateObservation: "", OneUseApprovalReceiptRef: ""}, "ToolReadyRecorded"},
		{&ToolDispatchStartedEvent{Envelope: *env, ToolCallID: "", AttemptID: "", CheckGeneration: 0, ExecutorFence: 0, Token: 0, ReceiptRef: "", BudgetReservation: ""}, "ToolDispatchStarted"},
		{&ToolResultRecordedEvent{Envelope: *env, ToolCallID: "", AttemptID: "", Outcome: ToolResultOutcome(""), ResultRef: "", Code: "", EffectEvidence: "", SourcePosition: 0}, "ToolResultRecorded"},
		{&ToolOutcomeUnknownEvent{Envelope: *env, ToolCallID: "", AttemptID: "", CauseClass: "", PossibleExposure: "", EvidenceRefs: nil}, "ToolOutcomeUnknown"},
		{&ToolReconciledEvent{Envelope: *env, ToolCallID: "", AttemptID: "", OperationReceipt: "", ObservedOutcome: ToolOutcomeUnknownObserved("")}, "ToolReconciled"},
		{&ToolAttemptAbandonedEvent{Envelope: *env, ToolCallID: "", AttemptID: "", DispositionRef: "", NextAction: NextAction("")}, "ToolAttemptAbandoned"},
		{&ToolAttemptPlannedEvent{Envelope: *env, ToolCallID: "", NewAttemptID: "", PreviousAttemptID: "", InputRef: "", IdempotencyKey: ""}, "ToolAttemptPlanned"},
		{&InvocationQuiesceRequestedEvent{Envelope: *env, InvocationID: "", AttemptID: "", Reason: QuiesceReason(""), CommandRef: ""}, "InvocationQuiesceRequested"},

		{&TimeBudgetWindowOpenedEvent{Envelope: *env, WindowID: "", Generation: 0, Incarnation: 0, TrancheReservationDuration: 0, Policy: "", MonotonicBaselineNano: 0}, "TimeBudgetWindowOpened"},
		{&TimeBudgetWindowClosedEvent{Envelope: *env, WindowID: "", MeasuredElapsed: 0, TailCoverage: "", ReservationDisposition: ""}, "TimeBudgetWindowClosed"},
		{&TimerScheduledEvent{Envelope: *env, TimerID: "", Generation: 0, Purpose: "", TargetID: "", ClockDomain: "", SchedulingDeadline: now, Duration: 0, PolicyVersion: ""}, "TimerScheduled"},
		{&TimerElapsedEvent{Envelope: *env, TimerID: "", Generation: 0, ObservedTime: now}, "TimerElapsed"},
		{&TimerCancelledEvent{Envelope: *env, TimerID: "", Generation: 0, CauseRef: ""}, "TimerCancelled"},
		{&BudgetObservedEvent{Envelope: *env, AttemptID: "", WindowID: "", TokensUsed: 0, DurationUsed: 0, Basis: ""}, "BudgetObserved"},
	}
}
