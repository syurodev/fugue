package vendorstore

// Session contains only session control metadata.
type Session struct {
	ID                                                                                                                        string
	Vendor                                                                                                                    string
	AccountID                                                                                                                 string
	Role                                                                                                                      string
	State                                                                                                                     string
	LastSequence                                                                                                              int64
	TeamID, TaskID, ResumeHandle, WaitingReason, ApprovalMode, CloseReason, StopProof, ProcessStartTime, CreatedAt, UpdatedAt string
	ProcessPID, PolicyRevision                                                                                                int64
}

// Event is a durable vendor-domain event without protected content.
type Event struct {
	contentID, rawID                                              string
	ProcessPID                                                    int64
	ProcessStartTime, ResumeHandle, CloseReason                   string
	Type                                                          string
	State, WaitingReason, TurnID, Outcome, FailureCode, StopProof string
	Item                                                          *Item
	Payload                                                       []byte
}

// Item contains transcript identity and sealed content references.
type Item struct {
	Metadata         ItemMetadata
	ID, TurnID, Kind string
	Revision         int64
	Content, Raw     []byte
}

// Event types persisted by the vendor journal.
const (
	VendorSessionStarted   = "VendorSessionStarted"
	VendorTurnStarted      = "VendorTurnStarted"
	TranscriptItemRecorded = "TranscriptItemRecorded"
	TranscriptItemUpdated  = "TranscriptItemUpdated"
	ApprovalRequested      = "ApprovalRequested"
	ApprovalDecided        = "ApprovalDecided"
	VendorTurnEnded        = "VendorTurnEnded"
	UsageObserved          = "UsageObserved"
	QuotaObserved          = "QuotaObserved"
	VendorProcessExited    = "VendorProcessExited"
	VendorSessionClosed    = "VendorSessionClosed"
)

// StateStarting identifies a session awaiting handshake.
const StateStarting = "starting"

// ItemMetadata contains only the transcript section 3 control fields.
type ItemMetadata struct {
	Origin         string  `json:"origin,omitempty"`
	Model          string  `json:"model,omitempty"`
	ReasoningState string  `json:"reasoning_state,omitempty"`
	Tool           string  `json:"tool,omitempty"`
	Title          string  `json:"title,omitempty"`
	Status         string  `json:"status,omitempty"`
	ExitCode       *int    `json:"exit_code,omitempty"`
	DurationMillis int64   `json:"duration_millis,omitempty"`
	Path           string  `json:"path,omitempty"`
	ChangeKind     string  `json:"change_kind,omitempty"`
	LinesAdded     int64   `json:"lines_added,omitempty"`
	LinesRemoved   int64   `json:"lines_removed,omitempty"`
	RequestKind    string  `json:"request_kind,omitempty"`
	Decision       string  `json:"decision,omitempty"`
	Decider        string  `json:"decider,omitempty"`
	Receipt        string  `json:"receipt,omitempty"`
	StepCount      int64   `json:"step_count,omitempty"`
	CurrentStep    int64   `json:"current_step,omitempty"`
	Name           string  `json:"name,omitempty"`
	Source         string  `json:"source,omitempty"`
	ProposalID     string  `json:"proposal_id,omitempty"`
	AgentCount     int64   `json:"agent_count,omitempty"`
	TaskID         string  `json:"task_id,omitempty"`
	Agent          string  `json:"agent,omitempty"`
	InputTokens    int64   `json:"input_tokens,omitempty"`
	OutputTokens   int64   `json:"output_tokens,omitempty"`
	CacheTokens    int64   `json:"cache_tokens,omitempty"`
	Cost           float64 `json:"cost,omitempty"`
	Account        string  `json:"account,omitempty"`
	Window         string  `json:"window,omitempty"`
	Used           float64 `json:"used,omitempty"`
	ResetAt        string  `json:"reset_at,omitempty"`
	Level          string  `json:"level,omitempty"`
	Code           string  `json:"code,omitempty"`
}

// StateClosed is terminal.
const StateClosed = "closed"

// ItemPlan and ItemQuota are shared transcript kind names.
const (
	ItemPlan  = "plan"
	ItemQuota = "quota"
)

// StateWaiting requires a validated waiting reason.
const (
	StateWaiting   = "waiting"
	metadataStatus = "Status"
)

// Persisted terminal outcome values shared with transcript status metadata.
const (
	OutcomeFailed  = "failed"
	OutcomeUnknown = "unknown"
)

// Approval modes for Fugue-owned tools (roadmap F6.3b).
const (
	ApprovalAuto = "auto"
	ApprovalAsk  = "ask"
	ApprovalFull = "full"
)
