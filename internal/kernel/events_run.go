package kernel

import "time"

// RunAcceptedEvent records that a run was accepted into the session queue.
type RunAcceptedEvent struct {
	Envelope        Envelope
	InputRef        string
	SessionID       string
	QueueItemRef    string
	ProtocolBinding string
	InitialBudgets  string
	PriorityClass   string
	PeerOutboxRef   string
	GoalID          string
	GoalRound       int
}

// EventName returns the canonical event name.
func (*RunAcceptedEvent) EventName() string { return "RunAccepted" }

// EventEnvelope returns the event envelope.
func (e *RunAcceptedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunAdmittedEvent records that an accepted run was admitted as the active run.
type RunAdmittedEvent struct {
	Envelope               Envelope
	Token                  uint64
	OwnerIncarnation       uint64
	QueueDecisionRef       string
	ContextEpoch           uint64
	StartRunID             string
	InitialContextRevision uint64
}

// EventName returns the canonical event name.
func (*RunAdmittedEvent) EventName() string { return "RunAdmitted" }

// EventEnvelope returns the event envelope.
func (e *RunAdmittedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunOwnershipAcquiredEvent records recovery acquisition of the active run under exclusive lock.
type RunOwnershipAcquiredEvent struct {
	Envelope                    Envelope
	NewToken                    uint64
	PreviousToken               uint64
	RecoveryReceipt             string
	DataDirectoryLockGeneration uint64
}

// EventName returns the canonical event name.
func (*RunOwnershipAcquiredEvent) EventName() string { return "RunOwnershipAcquired" }

// EventEnvelope returns the event envelope.
func (e *RunOwnershipAcquiredEvent) EventEnvelope() Envelope { return e.Envelope }

// RunStopRequestedEvent records an explicit request to stop or suspend the run.
type RunStopRequestedEvent struct {
	Envelope    Envelope
	Reason      StopReason
	Mode        StopMode
	Actor       string
	Target      string
	RequestTime time.Time
}

// EventName returns the canonical event name.
func (*RunStopRequestedEvent) EventName() string { return "RunStopRequested" }

// EventEnvelope returns the event envelope.
func (e *RunStopRequestedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunBlockAddedEvent records a new typed wait dependency on the run.
type RunBlockAddedEvent struct {
	Envelope        Envelope
	BlockerID       string
	Kind            WaitReason
	Reference       string
	ResumeCondition string
}

// EventName returns the canonical event name.
func (*RunBlockAddedEvent) EventName() string { return "RunBlockAdded" }

// EventEnvelope returns the event envelope.
func (e *RunBlockAddedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunBlockClearedEvent records that a typed wait dependency was resolved.
type RunBlockClearedEvent struct {
	Envelope          Envelope
	BlockerID         string
	ResolvingEvidence string
}

// EventName returns the canonical event name.
func (*RunBlockClearedEvent) EventName() string { return "RunBlockCleared" }

// EventEnvelope returns the event envelope.
func (e *RunBlockClearedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunInterruptedEvent records an interruption cause requiring intervention or reconciliation.
type RunInterruptedEvent struct {
	Envelope   Envelope
	Cause      string
	AttemptRef string
	PayloadRef string
}

// EventName returns the canonical event name.
func (*RunInterruptedEvent) EventName() string { return "RunInterrupted" }

// EventEnvelope returns the event envelope.
func (e *RunInterruptedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunRecoveryClearedEvent records that interruption causes were resolved or accepted.
type RunRecoveryClearedEvent struct {
	Envelope      Envelope
	CauseIDs      []string
	OwnerEvidence string
	Disposition   string
	NextAction    string
}

// EventName returns the canonical event name.
func (*RunRecoveryClearedEvent) EventName() string { return "RunRecoveryCleared" }

// EventEnvelope returns the event envelope.
func (e *RunRecoveryClearedEvent) EventEnvelope() Envelope { return e.Envelope }

// UnknownAcceptedEvent records user acceptance of unknown attempt outcome to close control.
type UnknownAcceptedEvent struct {
	Envelope            Envelope
	TargetAttempts      []string
	Revisions           []uint64
	Actor               string
	AcceptedUncertainty string
	Disposition         string
}

// EventName returns the canonical event name.
func (*UnknownAcceptedEvent) EventName() string { return "UnknownAccepted" }

// EventEnvelope returns the event envelope.
func (e *UnknownAcceptedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunClosureConfirmedEvent records confirmation of closing control without payload access.
type RunClosureConfirmedEvent struct {
	Envelope                    Envelope
	MetadataOnlyPreviewVersion  string
	RunRevision                 uint64
	ExactAttemptIDs             []string
	CauseIDs                    []string
	Actor                       string
	UnavailabilityReason        string
	PreviousContextRevision     uint64
	NewContextRevision          uint64
	ResultingContextRequirement ContextRequirement
	CauseEventRef               string
	AttemptDispositions         map[string]string
}

// EventName returns the canonical event name.
func (*RunClosureConfirmedEvent) EventName() string { return "RunClosureConfirmed" }

// EventEnvelope returns the event envelope.
func (e *RunClosureConfirmedEvent) EventEnvelope() Envelope { return e.Envelope }

// ContextBoundarySelectedEvent records selection of context continuity (fresh vs continue).
type ContextBoundarySelectedEvent struct {
	Envelope                 Envelope
	TargetQueuedRunID        string
	OldContextRevision       uint64
	NewContextRevision       uint64
	Epoch                    uint64
	StartRunID               string
	Mode                     string
	Actor                    string
	ValidationEvidence       string
	PreviousDecidingEventRef string
}

// EventName returns the canonical event name.
func (*ContextBoundarySelectedEvent) EventName() string { return "ContextBoundarySelected" }

// EventEnvelope returns the event envelope.
func (e *ContextBoundarySelectedEvent) EventEnvelope() Envelope { return e.Envelope }

// RunFinishedEvent records that execution has reached a terminal outcome.
type RunFinishedEvent struct {
	Envelope               Envelope
	Outcome                RunOutcome
	Truncated              bool
	PrimaryStopRef         string
	SecondaryStopRefs      []string
	UnresolvedExposureRefs []string
}

// EventName returns the canonical event name.
func (*RunFinishedEvent) EventName() string { return "RunFinished" }

// EventEnvelope returns the event envelope.
func (e *RunFinishedEvent) EventEnvelope() Envelope { return e.Envelope }

// StepStartedEvent records the opening of a new step frontier.
type StepStartedEvent struct {
	Envelope    Envelope
	StepIndex   int
	ContextRefs []string
}

// EventName returns the canonical event name.
func (*StepStartedEvent) EventName() string { return "StepStarted" }

// EventEnvelope returns the event envelope.
func (e *StepStartedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolBindingRevisedEvent records revision of available tool descriptors at step boundary.
type ToolBindingRevisedEvent struct {
	Envelope           Envelope
	OldRevision        uint64
	NewRevision        uint64
	AddedGroups        []string
	RemovedGroups      []string
	DescriptorVersions map[string]string
	Cause              string
}

// EventName returns the canonical event name.
func (*ToolBindingRevisedEvent) EventName() string { return "ToolBindingRevised" }

// EventEnvelope returns the event envelope.
func (e *ToolBindingRevisedEvent) EventEnvelope() Envelope { return e.Envelope }

// InstructionsLoadedEvent records rule layer instructions loaded into context assembly.
type InstructionsLoadedEvent struct {
	Envelope           Envelope
	RuleLayer          string
	SourcePath         string
	SnapshotPayloadRef string
	BudgetTokens       int
	PrunedTokens       int
}

// EventName returns the canonical event name.
func (*InstructionsLoadedEvent) EventName() string { return "InstructionsLoaded" }

// EventEnvelope returns the event envelope.
func (e *InstructionsLoadedEvent) EventEnvelope() Envelope { return e.Envelope }

// ContextInjectedEvent records an injected contextual message at step boundary.
type ContextInjectedEvent struct {
	Envelope   Envelope
	Tag        string
	Source     string
	PayloadRef string
}

// EventName returns the canonical event name.
func (*ContextInjectedEvent) EventName() string { return "ContextInjected" }

// EventEnvelope returns the event envelope.
func (e *ContextInjectedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolResultsPrunedEvent records compaction of previous tool results to save tokens.
type ToolResultsPrunedEvent struct {
	Envelope            Envelope
	ToolResultPositions []int
	PruneParams         string
	TokensSaved         int
}

// EventName returns the canonical event name.
func (*ToolResultsPrunedEvent) EventName() string { return "ToolResultsPruned" }

// EventEnvelope returns the event envelope.
func (e *ToolResultsPrunedEvent) EventEnvelope() Envelope { return e.Envelope }

// ContextCompactedEvent records context summarization across step boundaries.
type ContextCompactedEvent struct {
	Envelope              Envelope
	SummaryPayloadRef     string
	FirstRetainedPosition int
	TokensBefore          int
	TokensAfter           int
	WorkspaceMemoryRefs   []string
	ModelInvocationRef    string
}

// EventName returns the canonical event name.
func (*ContextCompactedEvent) EventName() string { return "ContextCompacted" }

// EventEnvelope returns the event envelope.
func (e *ContextCompactedEvent) EventEnvelope() Envelope { return e.Envelope }

// InputSteeredEvent records user steer input queued for next step boundary injection.
type InputSteeredEvent struct {
	Envelope  Envelope
	InputRef  string
	Origin    string
	Timestamp time.Time
}

// EventName returns the canonical event name.
func (*InputSteeredEvent) EventName() string { return "InputSteered" }

// EventEnvelope returns the event envelope.
func (e *InputSteeredEvent) EventEnvelope() Envelope { return e.Envelope }

// InputInterjectedEvent records user interjection taking priority over current work.
type InputInterjectedEvent struct {
	Envelope           Envelope
	InputRef           string
	MergedSteerRefs    []string
	TargetAttemptID    string
	TargetInvocationID string
	QuiesceResult      string
}

// EventName returns the canonical event name.
func (*InputInterjectedEvent) EventName() string { return "InputInterjected" }

// EventEnvelope returns the event envelope.
func (e *InputInterjectedEvent) EventEnvelope() Envelope { return e.Envelope }

// StepCompletedEvent records canonical completion of the current step.
type StepCompletedEvent struct {
	Envelope          Envelope
	StepIndex         int
	ResponseRef       string
	OrderedResultRefs []string
	Continuation      StepContinuation
}

// EventName returns the canonical event name.
func (*StepCompletedEvent) EventName() string { return "StepCompleted" }

// EventEnvelope returns the event envelope.
func (e *StepCompletedEvent) EventEnvelope() Envelope { return e.Envelope }

// BudgetExtendedEvent records user extension of execution budget (e.g. maxSteps).
type BudgetExtendedEvent struct {
	Envelope        Envelope
	Kind            string
	ExtensionAmount int
	Actor           string
}

// EventName returns the canonical event name.
func (*BudgetExtendedEvent) EventName() string { return "BudgetExtended" }

// EventEnvelope returns the event envelope.
func (e *BudgetExtendedEvent) EventEnvelope() Envelope { return e.Envelope }

// StepAbortedEvent records early termination of a step before canonical completion.
type StepAbortedEvent struct {
	Envelope     Envelope
	StepIndex    int
	Reason       string
	Dispositions map[string]string
}

// EventName returns the canonical event name.
func (*StepAbortedEvent) EventName() string { return "StepAborted" }

// EventEnvelope returns the event envelope.
func (e *StepAbortedEvent) EventEnvelope() Envelope { return e.Envelope }
