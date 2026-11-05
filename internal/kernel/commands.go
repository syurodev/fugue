package kernel

import "time"

// AcceptRunCommand requests admitting a new run into the session queue.
type AcceptRunCommand struct {
	CommandID       string
	RunID           string
	Origin          string
	InputRef        string
	SessionID       string
	QueueItemRef    string
	ProtocolBinding string
	Budgets         RunBudgets
	PriorityClass   string
	PeerOutboxRef   string
	GoalID          string
	GoalRound       int
}

// CommandName returns the canonical command name.
func (*AcceptRunCommand) CommandName() string { return "AcceptRun" }

// AdmitRunCommand requests transitioning a queued run to admitted active execution.
type AdmitRunCommand struct {
	CommandID              string
	RunID                  string
	Origin                 string
	Token                  uint64
	OwnerIncarnation       uint64
	QueueDecisionRef       string
	ContextEpoch           uint64
	StartRunID             string
	InitialContextRevision uint64
}

// CommandName returns the canonical command name.
func (*AdmitRunCommand) CommandName() string { return "AdmitRun" }

// AcquireRunOwnershipCommand requests recovery ownership of the active run.
type AcquireRunOwnershipCommand struct {
	CommandID                   string
	RunID                       string
	Origin                      string
	NewToken                    uint64
	PreviousToken               uint64
	RecoveryReceipt             string
	DataDirectoryLockGeneration uint64
}

// CommandName returns the canonical command name.
func (*AcquireRunOwnershipCommand) CommandName() string { return "AcquireRunOwnership" }

// AdvanceRunCommand requests scheduling the next step or dispatch progression.
type AdvanceRunCommand struct {
	CommandID string
	RunID     string
	Origin    string
	Token     uint64
	StepIndex int
}

// CommandName returns the canonical command name.
func (*AdvanceRunCommand) CommandName() string { return "AdvanceRun" }

// PrepareDispatchCommand requests pre-dispatch checks or attempt handoff.
type PrepareDispatchCommand struct {
	CommandID    string
	RunID        string
	Origin       string
	Token        uint64
	InvocationID string
	AttemptID    string
}

// CommandName returns the canonical command name.
func (*PrepareDispatchCommand) CommandName() string { return "PrepareDispatch" }

// ObserveModelStreamCommand reports receipt of a streaming chunk from a model worker.
type ObserveModelStreamCommand struct {
	CommandID     string
	RunID         string
	Origin        string
	Token         uint64
	AttemptID     string
	InvocationID  string
	FirstChunkRef string
}

// CommandName returns the canonical command name.
func (*ObserveModelStreamCommand) CommandName() string { return "ObserveModelStream" }

// ObserveModelResponseCommand reports receipt of a completed model response.
type ObserveModelResponseCommand struct {
	CommandID    string
	RunID        string
	Origin       string
	Token        uint64
	AttemptID    string
	InvocationID string
	ResponseRef  string
	UsageStatus  string
	FinishReason FinishReason
}

// CommandName returns the canonical command name.
func (*ObserveModelResponseCommand) CommandName() string { return "ObserveModelResponse" }

// ObserveModelFailureCommand reports terminal error evidence from a model attempt.
type ObserveModelFailureCommand struct {
	CommandID        string
	RunID            string
	Origin           string
	Token            uint64
	AttemptID        string
	InvocationID     string
	Stage            string
	FailureClass     string
	FailureCode      string
	RetryDisposition string
	UsageStatus      string
	PartialRef       string
}

// CommandName returns the canonical command name.
func (*ObserveModelFailureCommand) CommandName() string { return "ObserveModelFailure" }

// ObserveToolCheckCommand reports validation and gateway check results for a tool call.
type ObserveToolCheckCommand struct {
	CommandID          string
	RunID              string
	Origin             string
	Token              uint64
	ToolCallID         string
	AttemptID          string
	CheckGeneration    uint64
	ValidatedArgsRef   string
	GateObservation    string
	ApprovalReceiptRef string
}

// CommandName returns the canonical command name.
func (*ObserveToolCheckCommand) CommandName() string { return "ObserveToolCheck" }

// ObserveToolApprovalCommand reports owner approval decision for a tool call.
type ObserveToolApprovalCommand struct {
	CommandID          string
	RunID              string
	Origin             string
	Token              uint64
	ToolCallID         string
	CheckGeneration    uint64
	Resolution         string
	ResolutionEvidence string
}

// CommandName returns the canonical command name.
func (*ObserveToolApprovalCommand) CommandName() string { return "ObserveToolApproval" }

// ObserveToolResultCommand reports execution result or synthetic disposition for a tool attempt.
type ObserveToolResultCommand struct {
	CommandID      string
	RunID          string
	Origin         string
	Token          uint64
	ToolCallID     string
	AttemptID      string
	Outcome        string
	ResultRef      string
	Code           string
	EffectEvidence string
	SourcePosition int
}

// CommandName returns the canonical command name.
func (*ObserveToolResultCommand) CommandName() string { return "ObserveToolResult" }

// RequestStopCommand requests stopping or suspending active execution.
type RequestStopCommand struct {
	CommandID   string
	RunID       string
	Origin      string
	Token       uint64
	Reason      StopReason
	Mode        StopMode
	Actor       string
	Target      string
	RequestTime time.Time
}

// CommandName returns the canonical command name.
func (*RequestStopCommand) CommandName() string { return "RequestStop" }

// ObserveVaultCommand reports vault lock or unlock state changes.
type ObserveVaultCommand struct {
	CommandID string
	RunID     string
	Origin    string
	Locked    bool
	Evidence  string
}

// CommandName returns the canonical command name.
func (*ObserveVaultCommand) CommandName() string { return "ObserveVault" }

// ObserveHostPauseCommand reports host quiescence or suspension state changes.
type ObserveHostPauseCommand struct {
	CommandID string
	RunID     string
	Origin    string
	Paused    bool
	Evidence  string
}

// CommandName returns the canonical command name.
func (*ObserveHostPauseCommand) CommandName() string { return "ObserveHostPause" }

// ObserveDependencyCommand reports a typed blocker added or resolved.
type ObserveDependencyCommand struct {
	CommandID       string
	RunID           string
	Origin          string
	BlockerID       string
	Kind            WaitReason
	BudgetKind      BudgetKind
	Reference       string
	ResumeCondition string
	Resolved        bool
	Evidence        string
}

// CommandName returns the canonical command name.
func (*ObserveDependencyCommand) CommandName() string { return "ObserveDependency" }

// TimerFiredCommand reports that a scheduled timer has fired.
type TimerFiredCommand struct {
	CommandID    string
	RunID        string
	Origin       string
	TimerID      string
	Generation   uint64
	ObservedTime time.Time
}

// CommandName returns the canonical command name.
func (*TimerFiredCommand) CommandName() string { return "TimerFired" }

// RecoverRunCommand requests classifying in-flight work and unknown effects after restart.
type RecoverRunCommand struct {
	CommandID       string
	RunID           string
	Origin          string
	Token           uint64
	InterruptionRef string
	Evidence        string
}

// CommandName returns the canonical command name.
func (*RecoverRunCommand) CommandName() string { return "RecoverRun" }

// ReconcileInvocationCommand provides proof or reconciliation for an unknown attempt.
type ReconcileInvocationCommand struct {
	CommandID        string
	RunID            string
	Origin           string
	InvocationID     string
	AttemptID        string
	OperationReceipt string
	ObservedOutcome  string
}

// CommandName returns the canonical command name.
func (*ReconcileInvocationCommand) CommandName() string { return "ReconcileInvocation" }

// AcceptUnknownCommand records user acceptance of unknown attempt outcome.
type AcceptUnknownCommand struct {
	CommandID           string
	RunID               string
	Origin              string
	TargetAttempts      []string
	Revisions           []uint64
	Actor               string
	AcceptedUncertainty string
	Disposition         ControlDisposition
}

// CommandName returns the canonical command name.
func (*AcceptUnknownCommand) CommandName() string { return "AcceptUnknown" }

// CloseRunWithoutPayloadCommand requests closing control without access to protected payloads.
type CloseRunWithoutPayloadCommand struct {
	CommandID                  string
	RunID                      string
	Origin                     string
	MetadataOnlyPreviewVersion string
	RunRevision                uint64
	ExactAttemptIDs            []string
	CauseIDs                   []string
	Actor                      string
	UnavailabilityReason       string
}

// CommandName returns the canonical command name.
func (*CloseRunWithoutPayloadCommand) CommandName() string { return "CloseRunWithoutPayload" }

// SelectRunContextCommand selects context continuity for a queued run.
type SelectRunContextCommand struct {
	CommandID          string
	RunID              string
	Origin             string
	OldContextRevision uint64
	NewContextRevision uint64
	Epoch              uint64
	StartRunID         string
	Mode               string
	Actor              string
	ValidationEvidence string
}

// CommandName returns the canonical command name.
func (*SelectRunContextCommand) CommandName() string { return "SelectRunContext" }

// ResumeRunCommand requests resuming an interrupted or quiescent run.
type ResumeRunCommand struct {
	CommandID string
	RunID     string
	Origin    string
	Token     uint64
	Actor     string
	Evidence  string
}

// CommandName returns the canonical command name.
func (*ResumeRunCommand) CommandName() string { return "ResumeRun" }

// ObserveAccountingCommand reports reconciled token or time accounting observations.
type ObserveAccountingCommand struct {
	CommandID    string
	RunID        string
	Origin       string
	AttemptID    string
	WindowID     string
	TokensUsed   int
	DurationUsed time.Duration
	Basis        string
}

// CommandName returns the canonical command name.
func (*ObserveAccountingCommand) CommandName() string { return "ObserveAccounting" }

// ExtendBudgetCommand requests extending execution budget (e.g. maxSteps).
type ExtendBudgetCommand struct {
	CommandID       string
	RunID           string
	Origin          string
	Token           uint64
	Kind            BudgetKind
	ExtensionAmount int64
	Actor           string
}

// CommandName returns the canonical command name.
func (*ExtendBudgetCommand) CommandName() string { return "ExtendBudget" }

// SteerInputCommand provides steering guidance queued for next step context injection.
type SteerInputCommand struct {
	CommandID string
	RunID     string
	Origin    string
	InputRef  string
	Timestamp time.Time
}

// CommandName returns the canonical command name.
func (*SteerInputCommand) CommandName() string { return "SteerInput" }

// InterjectInputCommand provides immediate user input preempting active work.
type InterjectInputCommand struct {
	CommandID          string
	RunID              string
	Origin             string
	InputRef           string
	TargetAttemptID    string
	TargetInvocationID string
}

// CommandName returns the canonical command name.
func (*InterjectInputCommand) CommandName() string { return "InterjectInput" }

// ReviseToolBindingCommand updates tool descriptor bindings at a step boundary.
type ReviseToolBindingCommand struct {
	CommandID          string
	RunID              string
	Origin             string
	OldRevision        uint64
	NewRevision        uint64
	AddedGroups        []string
	RemovedGroups      []string
	DescriptorVersions map[string]string
	Cause              string
}

// CommandName returns the canonical command name.
func (*ReviseToolBindingCommand) CommandName() string { return "ReviseToolBinding" }
