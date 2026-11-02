package kernel

import "time"

// StepStartedEvent records the opening of a new step frontier.
type StepStartedEvent struct {
	Envelope    Envelope
	StepIndex   int
	ContextRefs []string
}

// EventName returns the canonical event name.
func (*StepStartedEvent) EventName() string { return "StepStarted" }

// EventEnvelope returns the event envelope.
func (e *StepStartedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *StepStartedEvent) isNil() bool { return e == nil }

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
func (e *ToolBindingRevisedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ToolBindingRevisedEvent) isNil() bool { return e == nil }

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
func (e *InstructionsLoadedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *InstructionsLoadedEvent) isNil() bool { return e == nil }

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
func (e *ContextInjectedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ContextInjectedEvent) isNil() bool { return e == nil }

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
func (e *ToolResultsPrunedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ToolResultsPrunedEvent) isNil() bool { return e == nil }

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
func (e *ContextCompactedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *ContextCompactedEvent) isNil() bool { return e == nil }

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
func (e *InputSteeredEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *InputSteeredEvent) isNil() bool { return e == nil }

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
func (e *InputInterjectedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *InputInterjectedEvent) isNil() bool { return e == nil }

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
func (e *StepCompletedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *StepCompletedEvent) isNil() bool { return e == nil }

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
func (e *BudgetExtendedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *BudgetExtendedEvent) isNil() bool { return e == nil }

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
func (e *StepAbortedEvent) EventEnvelope() Envelope {
	if e == nil {
		return Envelope{}
	}
	return e.Envelope
}

func (e *StepAbortedEvent) isNil() bool { return e == nil }
