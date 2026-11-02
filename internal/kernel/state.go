package kernel

import "time"

// StopIntent records a committed stop or suspend request on a run.
type StopIntent struct {
	Reason      StopReason
	Mode        StopMode
	Actor       string
	Target      string
	RequestTime time.Time
}

// ModelAttempt tracks the execution lifecycle of a single model dispatch.
type ModelAttempt struct {
	AttemptID string
	Status    ModelAttemptStatus
}

// ModelInvocation tracks the logical model operation of a step across retry attempts.
type ModelInvocation struct {
	InvocationID      string
	Planned           bool
	Completed         bool
	Failed            bool
	Closed            bool
	SelectedAttemptID string
	Attempts          map[string]*ModelAttempt
}

// ToolAttempt tracks the execution lifecycle of a single tool dispatch attempt.
type ToolAttempt struct {
	AttemptID string
	Status    ToolAttemptStatus
}

// ToolInvocation tracks a logical tool call proposal across checks and attempts.
type ToolInvocation struct {
	ToolCallID        string
	SourcePosition    int
	DescriptorVersion string
	EffectClass       EffectClass
	CheckGeneration   uint64
	Closed            bool
	SelectedAttemptID string
	Attempts          map[string]*ToolAttempt
}

// Block represents a typed wait dependency keeping the run in Waiting.
type Block struct {
	BlockerID       string
	Kind            WaitReason
	Reference       string
	ResumeCondition string
}

// ActiveTimer tracks a durable timer currently waiting to fire.
type ActiveTimer struct {
	TimerID    string
	Generation uint64
	Purpose    TimerPurpose
	Deadline   time.Time
}

// InterruptionCause represents an unresolved cause requiring repair or reconciliation.
type InterruptionCause struct {
	Cause      string
	AttemptRef string
	PayloadRef string
}

// State represents the complete fact-based projection of the kernel state machine.
type State struct {
	RunID                    string
	Accepted                 bool
	Admitted                 bool
	AdmissionToken           uint64
	OwnerIncarnation         uint64
	Finished                 bool
	Outcome                  RunOutcome
	StopIntent               *StopIntent
	ContextRequirement       ContextRequirement
	ContextEpoch             uint64
	ContextRevision          uint64
	CurrentStepIndex         int
	StepStarted              bool
	StepCompleted            bool
	StepAborted              bool
	StepContinuation         StepContinuation
	CurrentModelInvocationID string
	ModelInvocations         map[string]*ModelInvocation
	ToolInvocations          map[string]*ToolInvocation
	ToolOrder                []string
	Blocks                   map[string]*Block
	ActiveTimers             map[string]*ActiveTimer
	InterruptionCauses       map[string]*InterruptionCause
	QuiescingAttempts        map[string]bool
}

// NewState returns an initialized empty State with non-nil maps and slices.
func NewState() *State {
	return &State{
		RunID:                    "",
		Accepted:                 false,
		Admitted:                 false,
		AdmissionToken:           0,
		OwnerIncarnation:         0,
		Finished:                 false,
		Outcome:                  "",
		StopIntent:               nil,
		ContextRequirement:       ContextRequirementNone,
		ContextEpoch:             0,
		ContextRevision:          0,
		CurrentStepIndex:         0,
		StepStarted:              false,
		StepCompleted:            false,
		StepAborted:              false,
		StepContinuation:         "",
		CurrentModelInvocationID: "",
		ModelInvocations:         make(map[string]*ModelInvocation),
		ToolInvocations:          make(map[string]*ToolInvocation),
		ToolOrder:                make([]string, 0),
		Blocks:                   make(map[string]*Block),
		ActiveTimers:             make(map[string]*ActiveTimer),
		InterruptionCauses:       make(map[string]*InterruptionCause),
		QuiescingAttempts:        make(map[string]bool),
	}
}
