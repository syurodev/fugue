package kernel

// ModelRequestPlannedEvent records planning of a logical model invocation at the step frontier.
type ModelRequestPlannedEvent struct {
	Envelope              Envelope
	InvocationID          string
	Purpose               string
	FirstAttemptID        string
	ModelRef              string
	ContextRef            string
	ToolDeclarationRefs   []string
	EstimatedBudgetTokens int
}

// EventName returns the canonical event name.
func (*ModelRequestPlannedEvent) EventName() string { return "ModelRequestPlanned" }

// EventEnvelope returns the event envelope.
func (e *ModelRequestPlannedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelRequestPlannedEvent) isNil() bool { return e == nil }

// ModelDispatchStartedEvent records dispatch handoff of a model attempt to an executor.
type ModelDispatchStartedEvent struct {
	Envelope            Envelope
	AttemptID           string
	InvocationID        string
	Token               uint64
	ExecutorFence       uint64
	ReservationTokens   int
	ProviderSettingsRef string
	AccountID           string
	CatalogVersion      string
}

// EventName returns the canonical event name.
func (*ModelDispatchStartedEvent) EventName() string { return "ModelDispatchStarted" }

// EventEnvelope returns the event envelope.
func (e *ModelDispatchStartedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelDispatchStartedEvent) isNil() bool { return e == nil }

// ModelStreamStartedEvent records receipt of the first chunk from a streaming model response.
type ModelStreamStartedEvent struct {
	Envelope      Envelope
	AttemptID     string
	InvocationID  string
	FirstChunkRef string
}

// EventName returns the canonical event name.
func (*ModelStreamStartedEvent) EventName() string { return "ModelStreamStarted" }

// EventEnvelope returns the event envelope.
func (e *ModelStreamStartedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelStreamStartedEvent) isNil() bool { return e == nil }

// ModelResponseRecordedEvent records a complete and validated assistant response.
type ModelResponseRecordedEvent struct {
	Envelope     Envelope
	AttemptID    string
	InvocationID string
	ResponseRef  string
	UsageStatus  string
	FinishReason FinishReason
}

// EventName returns the canonical event name.
func (*ModelResponseRecordedEvent) EventName() string { return "ModelResponseRecorded" }

// EventEnvelope returns the event envelope.
func (e *ModelResponseRecordedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelResponseRecordedEvent) isNil() bool { return e == nil }

// ModelRequestFailedEvent records a terminal model attempt failure with error classification.
type ModelRequestFailedEvent struct {
	Envelope         Envelope
	AttemptID        string
	InvocationID     string
	Stage            string
	FailureClass     string
	FailureCode      string
	RetryDisposition string
	UsageStatus      string
	PartialRef       string
}

// EventName returns the canonical event name.
func (*ModelRequestFailedEvent) EventName() string { return "ModelRequestFailed" }

// EventEnvelope returns the event envelope.
func (e *ModelRequestFailedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelRequestFailedEvent) isNil() bool { return e == nil }

// ModelRequestInterruptedEvent records missing completion evidence or transport interruption.
type ModelRequestInterruptedEvent struct {
	Envelope               Envelope
	AttemptID              string
	InvocationID           string
	UncertaintyClass       string
	PartialRef             string
	UsageReservationTokens int
}

// EventName returns the canonical event name.
func (*ModelRequestInterruptedEvent) EventName() string { return "ModelRequestInterrupted" }

// EventEnvelope returns the event envelope.
func (e *ModelRequestInterruptedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelRequestInterruptedEvent) isNil() bool { return e == nil }

// ModelRetryScheduledEvent records scheduling a retry attempt with a timer before waiting.
type ModelRetryScheduledEvent struct {
	Envelope          Envelope
	InvocationID      string
	PreviousAttemptID string
	NewAttemptID      string
	PolicyVersion     string
	RetryNumber       int
	TimerID           string
}

// EventName returns the canonical event name.
func (*ModelRetryScheduledEvent) EventName() string { return "ModelRetryScheduled" }

// EventEnvelope returns the event envelope.
func (e *ModelRetryScheduledEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelRetryScheduledEvent) isNil() bool { return e == nil }

// ModelAttemptPlannedEvent records a subsequent planned attempt under an open invocation.
type ModelAttemptPlannedEvent struct {
	Envelope              Envelope
	InvocationID          string
	AttemptID             string
	PreviousAttemptID     string
	EstimatedBudgetTokens int
}

// EventName returns the canonical event name.
func (*ModelAttemptPlannedEvent) EventName() string { return "ModelAttemptPlanned" }

// EventEnvelope returns the event envelope.
func (e *ModelAttemptPlannedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelAttemptPlannedEvent) isNil() bool { return e == nil }

// ModelAttemptCancelledEvent records cancellation of a planned or quiesced model attempt.
type ModelAttemptCancelledEvent struct {
	Envelope     Envelope
	AttemptID    string
	InvocationID string
	Stage        string
	Evidence     string
}

// EventName returns the canonical event name.
func (*ModelAttemptCancelledEvent) EventName() string { return "ModelAttemptCancelled" }

// EventEnvelope returns the event envelope.
func (e *ModelAttemptCancelledEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelAttemptCancelledEvent) isNil() bool { return e == nil }

// ModelAttemptAbandonedEvent records abandonment of an interrupted model attempt.
type ModelAttemptAbandonedEvent struct {
	Envelope     Envelope
	AttemptID    string
	InvocationID string
	Uncertainty  string
	Receipt      string
	NextAction   NextAction
	UnknownCost  int
}

// EventName returns the canonical event name.
func (*ModelAttemptAbandonedEvent) EventName() string { return "ModelAttemptAbandoned" }

// EventEnvelope returns the event envelope.
func (e *ModelAttemptAbandonedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelAttemptAbandonedEvent) isNil() bool { return e == nil }

// ModelInvocationFailedEvent records final terminal failure of a logical model invocation.
type ModelInvocationFailedEvent struct {
	Envelope        Envelope
	InvocationID    string
	FinalFailureRef string
	Decision        string
}

// EventName returns the canonical event name.
func (*ModelInvocationFailedEvent) EventName() string { return "ModelInvocationFailed" }

// EventEnvelope returns the event envelope.
func (e *ModelInvocationFailedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelInvocationFailedEvent) isNil() bool { return e == nil }

// ModelUsageReconciledEvent records post-execution reconciliation of actual model token usage.
type ModelUsageReconciledEvent struct {
	Envelope             Envelope
	AttemptID            string
	InvocationID         string
	AccountingEvidence   string
	OldReservationTokens int
	ObservedTokens       int
}

// EventName returns the canonical event name.
func (*ModelUsageReconciledEvent) EventName() string { return "ModelUsageReconciled" }

// EventEnvelope returns the event envelope.
func (e *ModelUsageReconciledEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ModelUsageReconciledEvent) isNil() bool { return e == nil }
