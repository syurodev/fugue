package kernel

import "time"

// ToolCallPlannedEvent records candidate tool call proposals from a completed model response.
type ToolCallPlannedEvent struct {
	Envelope          Envelope
	ToolCallID        string
	SourcePosition    int
	DescriptorVersion string
	EffectClass       EffectClass
	FirstAttemptID    string
	InputRef          string
}

// EventName returns the canonical event name.
func (*ToolCallPlannedEvent) EventName() string { return "ToolCallPlanned" }

// EventEnvelope returns the event envelope.
func (e *ToolCallPlannedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolCheckResetEvent records resetting pre-dispatch tool checks following host restart recovery.
type ToolCheckResetEvent struct {
	Envelope                 Envelope
	ToolCallID               string
	OldGeneration            uint64
	NewGeneration            uint64
	OwnerInvalidationReceipt string
	ResolvedDependencyRefs   []string
}

// EventName returns the canonical event name.
func (*ToolCheckResetEvent) EventName() string { return "ToolCheckReset" }

// EventEnvelope returns the event envelope.
func (e *ToolCheckResetEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolCheckStartedEvent records starting authorization and schema checks on a frontier tool call.
type ToolCheckStartedEvent struct {
	Envelope        Envelope
	ToolCallID      string
	AttemptID       string
	CheckGeneration uint64
	FrontierCursor  int
}

// EventName returns the canonical event name.
func (*ToolCheckStartedEvent) EventName() string { return "ToolCheckStarted" }

// EventEnvelope returns the event envelope.
func (e *ToolCheckStartedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolApprovalRequestedEvent records an owner-issued approval request for a tool call.
type ToolApprovalRequestedEvent struct {
	Envelope        Envelope
	ToolCallID      string
	CheckGeneration uint64
	OwnerRequestRef string
	RequestRevision uint64
	ExactArgsRef    string
	ApproverRoute   string
	Expiry          time.Time
}

// EventName returns the canonical event name.
func (*ToolApprovalRequestedEvent) EventName() string { return "ToolApprovalRequested" }

// EventEnvelope returns the event envelope.
func (e *ToolApprovalRequestedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolApprovalResolvedEvent records owner resolution of a pending tool approval request.
type ToolApprovalResolvedEvent struct {
	Envelope                Envelope
	ToolCallID              string
	CheckGeneration         uint64
	OwnerResolutionEvidence string
	Resolution              string
}

// EventName returns the canonical event name.
func (*ToolApprovalResolvedEvent) EventName() string { return "ToolApprovalResolved" }

// EventEnvelope returns the event envelope.
func (e *ToolApprovalResolvedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolReadyRecordedEvent records readiness of a tool call for execution scheduling.
type ToolReadyRecordedEvent struct {
	Envelope                 Envelope
	ToolCallID               string
	CheckGeneration          uint64
	ValidatedArgsRef         string
	GateObservation          string
	OneUseApprovalReceiptRef string
}

// EventName returns the canonical event name.
func (*ToolReadyRecordedEvent) EventName() string { return "ToolReadyRecorded" }

// EventEnvelope returns the event envelope.
func (e *ToolReadyRecordedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolDispatchStartedEvent records dispatch authorization and handoff of a tool attempt.
type ToolDispatchStartedEvent struct {
	Envelope          Envelope
	ToolCallID        string
	AttemptID         string
	CheckGeneration   uint64
	ExecutorFence     uint64
	Token             uint64
	ReceiptRef        string
	BudgetReservation string
}

// EventName returns the canonical event name.
func (*ToolDispatchStartedEvent) EventName() string { return "ToolDispatchStarted" }

// EventEnvelope returns the event envelope.
func (e *ToolDispatchStartedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolResultRecordedEvent records a canonical terminal result for a tool call.
type ToolResultRecordedEvent struct {
	Envelope       Envelope
	ToolCallID     string
	AttemptID      string
	Outcome        ToolResultOutcome
	ResultRef      string
	Code           string
	EffectEvidence string
	SourcePosition int
}

// EventName returns the canonical event name.
func (*ToolResultRecordedEvent) EventName() string { return "ToolResultRecorded" }

// EventEnvelope returns the event envelope.
func (e *ToolResultRecordedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolOutcomeUnknownEvent records missing completion evidence for an in-flight tool attempt.
type ToolOutcomeUnknownEvent struct {
	Envelope         Envelope
	ToolCallID       string
	AttemptID        string
	CauseClass       string
	PossibleExposure string
	EvidenceRefs     []string
}

// EventName returns the canonical event name.
func (*ToolOutcomeUnknownEvent) EventName() string { return "ToolOutcomeUnknown" }

// EventEnvelope returns the event envelope.
func (e *ToolOutcomeUnknownEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolReconciledEvent records external proof or reconciliation of an unknown tool attempt.
type ToolReconciledEvent struct {
	Envelope         Envelope
	ToolCallID       string
	AttemptID        string
	OperationReceipt string
	ObservedOutcome  ToolOutcomeUnknownObserved
}

// EventName returns the canonical event name.
func (*ToolReconciledEvent) EventName() string { return "ToolReconciled" }

// EventEnvelope returns the event envelope.
func (e *ToolReconciledEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolAttemptAbandonedEvent records abandonment of an unknown tool attempt per effect class contract.
type ToolAttemptAbandonedEvent struct {
	Envelope       Envelope
	ToolCallID     string
	AttemptID      string
	DispositionRef string
	NextAction     NextAction
}

// EventName returns the canonical event name.
func (*ToolAttemptAbandonedEvent) EventName() string { return "ToolAttemptAbandoned" }

// EventEnvelope returns the event envelope.
func (e *ToolAttemptAbandonedEvent) EventEnvelope() Envelope { return e.Envelope }

// ToolAttemptPlannedEvent records planning a new attempt for a tool invocation under allowed policy.
type ToolAttemptPlannedEvent struct {
	Envelope          Envelope
	ToolCallID        string
	NewAttemptID      string
	PreviousAttemptID string
	InputRef          string
	IdempotencyKey    string
}

// EventName returns the canonical event name.
func (*ToolAttemptPlannedEvent) EventName() string { return "ToolAttemptPlanned" }

// EventEnvelope returns the event envelope.
func (e *ToolAttemptPlannedEvent) EventEnvelope() Envelope { return e.Envelope }

// InvocationQuiesceRequestedEvent records a quiescence request targeting an in-flight attempt.
type InvocationQuiesceRequestedEvent struct {
	Envelope     Envelope
	InvocationID string
	AttemptID    string
	Reason       QuiesceReason
	CommandRef   string
}

// EventName returns the canonical event name.
func (*InvocationQuiesceRequestedEvent) EventName() string { return "InvocationQuiesceRequested" }

// EventEnvelope returns the event envelope.
func (e *InvocationQuiesceRequestedEvent) EventEnvelope() Envelope { return e.Envelope }
