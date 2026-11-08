package vendorstore

// Session states. The vendor runtime owns the transitions between them; the
// store only checks that a persisted state is one of these.
const (
	StateStarting         = "starting"
	StateReady            = "ready"
	StateRunning          = "running"
	StateAwaitingApproval = "awaiting_approval"
	StateWaiting          = "waiting"
	StateDetached         = "detached"
	StateClosed           = "closed"
)

// Waiting reasons, valid only while the session state is StateWaiting.
const (
	WaitingVaultLocked = "vault_locked"
	WaitingQuota       = "quota"
	WaitingCapacity    = "capacity"
)

// Turn outcomes persisted when a turn ends.
const (
	OutcomeCompleted   = "completed"
	OutcomeInterrupted = "interrupted"
	OutcomeFailed      = "failed"
	OutcomeUnknown     = "unknown"
)

// Stop proofs recorded when a session closes: whether the vendor process tree
// was proved stopped.
const (
	StopProved  = "proved"
	StopUnknown = "unknown"
)

// Approval modes for Fugue-owned tools (roadmap F6.3b).
const (
	ApprovalPlan = "plan"
	ApprovalAsk  = "ask"
	ApprovalAuto = "auto"
	ApprovalFull = "full"
)

// Transcript item kinds (transcript section 3).
const (
	KindUserMessage      = "user_message"
	KindAssistantMessage = "assistant_message"
	KindReasoning        = "reasoning"
	KindToolCall         = "tool_call"
	KindFileChange       = "file_change"
	KindApproval         = "approval"
	KindPlan             = "plan"
	KindSubagent         = "subagent"
	KindCommand          = "command"
	KindTeamProposal     = "team_proposal"
	KindTaskLink         = "task_link"
	KindUsage            = "usage"
	KindQuota            = "quota"
	KindNotice           = "notice"
)

// Tool-call statuses carried in transcript item metadata.
const (
	ToolPending   = "pending"
	ToolRunning   = "running"
	ToolSucceeded = "succeeded"
	ToolFailed    = "failed"
	ToolDenied    = "denied"
	ToolUnknown   = "unknown"
)

func validState(state string) bool {
	switch state {
	case StateStarting, StateReady, StateRunning, StateAwaitingApproval, StateWaiting, StateDetached, StateClosed:
		return true
	default:
		return false
	}
}

func validWaitingReason(reason string) bool {
	switch reason {
	case WaitingVaultLocked, WaitingQuota, WaitingCapacity:
		return true
	default:
		return false
	}
}

func validOutcome(outcome string) bool {
	switch outcome {
	case OutcomeCompleted, OutcomeInterrupted, OutcomeFailed, OutcomeUnknown:
		return true
	default:
		return false
	}
}

func validStopProof(proof string) bool {
	switch proof {
	case StopProved, StopUnknown:
		return true
	default:
		return false
	}
}

func validMode(mode string) bool {
	switch mode {
	case ApprovalPlan, ApprovalAsk, ApprovalAuto, ApprovalFull:
		return true
	default:
		return false
	}
}

func validToolStatus(status string) bool {
	switch status {
	case ToolPending, ToolRunning, ToolSucceeded, ToolFailed, ToolDenied, ToolUnknown:
		return true
	default:
		return false
	}
}
