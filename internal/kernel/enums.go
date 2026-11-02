package kernel

// Lifecycle represents the derived top-level execution lifecycle of a run.
type Lifecycle string

// Lifecycle state machine values.
const (
	LifecycleQueued      Lifecycle = "queued"
	LifecycleRunning     Lifecycle = "running"
	LifecycleWaiting     Lifecycle = "waiting"
	LifecycleCancelling  Lifecycle = "cancelling"
	LifecycleInterrupted Lifecycle = "interrupted"
	LifecycleFinished    Lifecycle = "finished"
)

// Phase represents the derived execution frontier phase within a step.
type Phase string

// Phase execution frontier values.
const (
	PhaseFinalizing      Phase = "finalizing"
	PhasePreparing       Phase = "preparing"
	PhaseCallingModel    Phase = "calling_model"
	PhaseProcessingTools Phase = "processing_tools"
)

// WaitReason represents a typed wait dependency keeping an admitted run in Waiting.
type WaitReason string

// WaitReason dependency values.
const (
	WaitReasonApproval          WaitReason = "approval"
	WaitReasonUserInput         WaitReason = "user_input"
	WaitReasonDependency        WaitReason = "dependency"
	WaitReasonRetryTimer        WaitReason = "retry_timer"
	WaitReasonWorkerCapacity    WaitReason = "worker_capacity"
	WaitReasonVaultLocked       WaitReason = "vault_locked"
	WaitReasonHostPaused        WaitReason = "host_paused"
	WaitReasonRecoveryAdmission WaitReason = "recovery_admission"
	WaitReasonStepBudget        WaitReason = "step_budget"
)

// StopMode indicates whether a stop intent terminates or suspends execution.
type StopMode string

// StopMode execution behavior values.
const (
	StopModeTerminal StopMode = "terminal"
	StopModeSuspend  StopMode = "suspend"
)

// StopReason represents the typed origin reason of a stop intent.
type StopReason string

// StopReason trigger values.
const (
	StopReasonUserCancel   StopReason = "user_cancel"
	StopReasonParentCancel StopReason = "parent_cancel"
	StopReasonBudget       StopReason = "budget"
	StopReasonFatalError   StopReason = "fatal_error"
	StopReasonShutdown     StopReason = "shutdown"
)

// RunOutcome represents the committed terminal outcome of a finished run.
type RunOutcome string

// RunOutcome terminal values.
const (
	RunOutcomeCompleted        RunOutcome = "completed"
	RunOutcomeCancelled        RunOutcome = "cancelled"
	RunOutcomeBudgetExceeded   RunOutcome = "budget_exceeded"
	RunOutcomeFailed           RunOutcome = "failed"
	RunOutcomeAbandoned        RunOutcome = "abandoned"
	RunOutcomeAbandonedUnknown RunOutcome = "abandoned_unknown"
)

// EffectClass defines the mutation and retry contract of a tool descriptor.
type EffectClass string

// EffectClass mutation classification values.
const (
	EffectClassPureRead      EffectClass = "pure_read"
	EffectClassIdempotent    EffectClass = "idempotent"
	EffectClassSideEffecting EffectClass = "side_effecting"
)

// ModelAttemptStatus represents the execution state of a single model dispatch attempt.
type ModelAttemptStatus string

// ModelAttemptStatus execution values.
const (
	ModelAttemptStatusPlanned          ModelAttemptStatus = "planned"
	ModelAttemptStatusRequesting       ModelAttemptStatus = "requesting"
	ModelAttemptStatusStreaming        ModelAttemptStatus = "streaming"
	ModelAttemptStatusCompleted        ModelAttemptStatus = "completed"
	ModelAttemptStatusFailed           ModelAttemptStatus = "failed"
	ModelAttemptStatusInterrupted      ModelAttemptStatus = "interrupted"
	ModelAttemptStatusCancelled        ModelAttemptStatus = "cancelled"
	ModelAttemptStatusAbandonedUnknown ModelAttemptStatus = "abandoned_unknown"
)

// ToolAttemptStatus represents the execution state of a single tool dispatch attempt.
type ToolAttemptStatus string

// ToolAttemptStatus execution values.
const (
	ToolAttemptStatusPlanned          ToolAttemptStatus = "planned"
	ToolAttemptStatusChecking         ToolAttemptStatus = "checking"
	ToolAttemptStatusAwaitingApproval ToolAttemptStatus = "awaiting_approval"
	ToolAttemptStatusReady            ToolAttemptStatus = "ready"
	ToolAttemptStatusDispatching      ToolAttemptStatus = "dispatching"
	ToolAttemptStatusSucceeded        ToolAttemptStatus = "succeeded"
	ToolAttemptStatusFailed           ToolAttemptStatus = "failed"
	ToolAttemptStatusDenied           ToolAttemptStatus = "denied"
	ToolAttemptStatusRefused          ToolAttemptStatus = "refused"
	ToolAttemptStatusCancelled        ToolAttemptStatus = "cancelled"
	ToolAttemptStatusOutcomeUnknown   ToolAttemptStatus = "outcome_unknown"
	ToolAttemptStatusAbandonedUnknown ToolAttemptStatus = "abandoned_unknown"
)

// StepContinuation indicates whether a step completion requests another step or ends the run.
type StepContinuation string

// StepContinuation transition directive values.
const (
	StepContinuationContinue StepContinuation = "continue"
	StepContinuationEnd      StepContinuation = "end"
)

// ContextRequirement expresses the context requirement on admission after an abnormal close.
type ContextRequirement string

// ContextRequirement recovery classification values.
const (
	ContextRequirementNone                 ContextRequirement = "none"
	ContextRequirementRevalidationRequired ContextRequirement = "revalidation_required"
	ContextRequirementFreshRequired        ContextRequirement = "fresh_required"
)

// AttemptDisposition represents the terminal disposition applied to an attempt during closure/recovery.
type AttemptDisposition string

// AttemptDisposition values.
const (
	AttemptDispositionAbandonedUnknown AttemptDisposition = "abandoned_unknown"
	AttemptDispositionCancelled        AttemptDisposition = "cancelled"
)

// TimerPurpose represents the intended domain purpose of a scheduled timer.
type TimerPurpose string

// TimerPurpose values.
const (
	TimerPurposeModelRetry  TimerPurpose = "model_retry"
	TimerPurposeToolRetry   TimerPurpose = "tool_retry"
	TimerPurposeRunDeadline TimerPurpose = "run_deadline"
)

// ToolResultOutcome represents the terminal execution outcome of a tool attempt.
type ToolResultOutcome string

// ToolResultOutcome values.
const (
	ToolResultOutcomeSucceeded        ToolResultOutcome = "succeeded"
	ToolResultOutcomeFailed           ToolResultOutcome = "failed"
	ToolResultOutcomeDenied           ToolResultOutcome = "denied"
	ToolResultOutcomeRefused          ToolResultOutcome = "refused"
	ToolResultOutcomeCancelled        ToolResultOutcome = "cancelled"
	ToolResultOutcomeAbandonedUnknown ToolResultOutcome = "abandoned_unknown"
)

// ToolOutcomeUnknownObserved represents the observed reason an outcome could not be determined.
type ToolOutcomeUnknownObserved string

// ToolOutcomeUnknownObserved values.
const (
	ToolOutcomeUnknownObservedDisconnected ToolOutcomeUnknownObserved = "disconnected"
	ToolOutcomeUnknownObservedProcessLost  ToolOutcomeUnknownObserved = "process_lost"
	ToolOutcomeUnknownObservedTimeout      ToolOutcomeUnknownObserved = "timeout"
)

// ToolReconciliationResolution represents how an unknown tool outcome was resolved by reconciliation.
type ToolReconciliationResolution string

// ToolReconciliationResolution values.
const (
	ToolReconciliationResolutionConfirmedApplied    ToolReconciliationResolution = "confirmed_applied"
	ToolReconciliationResolutionConfirmedNotApplied ToolReconciliationResolution = "confirmed_not_applied"
)

// QuiesceReason represents the trigger reason for quiescing an active invocation.
type QuiesceReason string

// QuiesceReason values.
const (
	QuiesceReasonInterject QuiesceReason = "interject"
	QuiesceReasonStop      QuiesceReason = "stop"
)

// RecoveryDisposition represents the terminal disposition applied when clearing recovery causes.
type RecoveryDisposition string

// RecoveryDisposition values.
const (
	RecoveryDispositionReconciled        RecoveryDisposition = "reconciled"
	RecoveryDispositionAcceptedUnknown   RecoveryDisposition = "accepted_unknown"
	RecoveryDispositionClosedUnavailable RecoveryDisposition = "closed_unavailable"
)

// ControlDisposition represents the terminal control disposition for an accepted unknown outcome.
type ControlDisposition string

// ControlDisposition values.
const (
	ControlDispositionClose ControlDisposition = "close"
)

// NextAction represents the authorized next control action following recovery or closure.
type NextAction string

// NextAction values.
const (
	NextActionClose NextAction = "close"
	NextActionRetry NextAction = "retry"
)
