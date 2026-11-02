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
func (e *RunAcceptedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunAcceptedEvent) isNil() bool { return e == nil }

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
func (e *RunAdmittedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunAdmittedEvent) isNil() bool { return e == nil }

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
func (e *RunOwnershipAcquiredEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunOwnershipAcquiredEvent) isNil() bool { return e == nil }

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
func (e *RunStopRequestedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunStopRequestedEvent) isNil() bool { return e == nil }

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
func (e *RunBlockAddedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunBlockAddedEvent) isNil() bool { return e == nil }

// RunBlockClearedEvent records that a typed wait dependency was resolved.
type RunBlockClearedEvent struct {
	Envelope          Envelope
	BlockerID         string
	ResolvingEvidence string
}

// EventName returns the canonical event name.
func (*RunBlockClearedEvent) EventName() string { return "RunBlockCleared" }

// EventEnvelope returns the event envelope.
func (e *RunBlockClearedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunBlockClearedEvent) isNil() bool { return e == nil }

// RunInterruptedEvent records an interruption cause requiring intervention or reconciliation.
type RunInterruptedEvent struct {
	Envelope   Envelope
	CauseID    string
	Cause      string
	AttemptRef string
	PayloadRef string
}

// EventName returns the canonical event name.
func (*RunInterruptedEvent) EventName() string { return "RunInterrupted" }

// EventEnvelope returns the event envelope.
func (e *RunInterruptedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunInterruptedEvent) isNil() bool { return e == nil }

// RunRecoveryClearedEvent records that interruption causes were resolved or accepted.
type RunRecoveryClearedEvent struct {
	Envelope      Envelope
	CauseIDs      []string
	OwnerEvidence string
	Disposition   RecoveryDisposition
	NextAction    NextAction
}

// EventName returns the canonical event name.
func (*RunRecoveryClearedEvent) EventName() string { return "RunRecoveryCleared" }

// EventEnvelope returns the event envelope.
func (e *RunRecoveryClearedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunRecoveryClearedEvent) isNil() bool { return e == nil }

// UnknownAcceptedEvent records user acceptance of unknown attempt outcome to close control.
type UnknownAcceptedEvent struct {
	Envelope            Envelope
	TargetAttempts      []string
	Revisions           []uint64
	Actor               string
	AcceptedUncertainty string
	Disposition         ControlDisposition
}

// EventName returns the canonical event name.
func (*UnknownAcceptedEvent) EventName() string { return "UnknownAccepted" }

// EventEnvelope returns the event envelope.
func (e *UnknownAcceptedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *UnknownAcceptedEvent) isNil() bool { return e == nil }

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
	AttemptDispositions         map[string]AttemptDisposition
}

// EventName returns the canonical event name.
func (*RunClosureConfirmedEvent) EventName() string { return "RunClosureConfirmed" }

// EventEnvelope returns the event envelope.
func (e *RunClosureConfirmedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunClosureConfirmedEvent) isNil() bool { return e == nil }

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
func (e *ContextBoundarySelectedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ContextBoundarySelectedEvent) isNil() bool { return e == nil }

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
func (e *RunFinishedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *RunFinishedEvent) isNil() bool { return e == nil }
