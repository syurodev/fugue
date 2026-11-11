package vendorstore

// Session contains only session control metadata. ProcessPID, ProcessStartToken
// and ProcessScopeUnit are the process identity that internal/process verifies
// before it signals a recovered process (F9.2b); ProcessScopeUnit is empty when
// the process is uncontained.
type Session struct {
	ID                string
	Vendor            string
	AccountID         string
	Role              string
	State             string
	LastSequence      int64
	TeamID            string
	TaskID            string
	ResumeHandle      string
	WaitingReason     string
	ApprovalMode      string
	CloseReason       string
	StopProof         string
	ProcessPID        int64
	ProcessStartToken string
	ProcessScopeUnit  string
	PolicyRevision    int64
	CreatedAt         string
	UpdatedAt         string
}

// Event is a durable vendor-domain event without protected content.
type Event struct {
	contentID         string
	rawID             string
	Type              string
	ProcessPID        int64
	ProcessStartToken string
	ProcessScopeUnit  string
	ResumeHandle      string
	CloseReason       string
	State             string
	WaitingReason     string
	TurnID            string
	TurnOrigin        string
	Outcome           string
	FailureCode       string
	StopProof         string
	Item              *Item
	Payload           []byte
}

// Item contains transcript identity and sealed content references.
type Item struct {
	Metadata ItemMetadata
	ID       string
	TurnID   string
	Kind     string
	Revision int64
	Content  []byte
	Raw      []byte
}

// Event types persisted by the vendor journal.
const (
	VendorSessionStarted      = "VendorSessionStarted"
	VendorTurnStarted         = "VendorTurnStarted"
	TranscriptItemRecorded    = "TranscriptItemRecorded"
	TranscriptItemUpdated     = "TranscriptItemUpdated"
	ApprovalRequested         = "ApprovalRequested"
	ApprovalDecided           = "ApprovalDecided"
	VendorTurnEnded           = "VendorTurnEnded"
	UsageObserved             = "UsageObserved"
	QuotaObserved             = "QuotaObserved"
	VendorProcessExited       = "VendorProcessExited"
	VendorSessionClosed       = "VendorSessionClosed"
	VendorSessionStateChanged = "VendorSessionStateChanged"
)

const (
	TurnOriginFugue  = "fugue"
	TurnOriginVendor = "vendor"
)

// ItemMetadata contains only the transcript section 3 control fields.
// Under INV-09, metadata is an allowlist of enums, counts, IDs, or timestamps;
// free-text fields (such as titles, paths, argv, or user/vendor-supplied names)
// are forbidden and belong in protected content payloads instead.
type ItemMetadata struct {
	Origin               string  `json:"origin,omitempty"`
	Model                string  `json:"model,omitempty"`
	ReasoningState       string  `json:"reasoning_state,omitempty"`
	Tool                 string  `json:"tool,omitempty"`
	Status               string  `json:"status,omitempty"`
	ExitCode             *int    `json:"exit_code,omitempty"`
	DurationMillis       int64   `json:"duration_millis,omitempty"`
	ChangeKind           string  `json:"change_kind,omitempty"`
	LinesAdded           int64   `json:"lines_added,omitempty"`
	LinesRemoved         int64   `json:"lines_removed,omitempty"`
	RequestKind          string  `json:"request_kind,omitempty"`
	Decision             string  `json:"decision,omitempty"`
	Decider              string  `json:"decider,omitempty"`
	Receipt              string  `json:"receipt,omitempty"`
	StepCount            int64   `json:"step_count,omitempty"`
	CurrentStep          int64   `json:"current_step,omitempty"`
	Source               string  `json:"source,omitempty"`
	ProposalID           string  `json:"proposal_id,omitempty"`
	AgentCount           int64   `json:"agent_count,omitempty"`
	TaskID               string  `json:"task_id,omitempty"`
	Agent                string  `json:"agent,omitempty"`
	InputTokens          int64   `json:"input_tokens,omitempty"`
	OutputTokens         int64   `json:"output_tokens,omitempty"`
	CacheTokens          int64   `json:"cache_tokens,omitempty"`
	Cost                 float64 `json:"cost,omitempty"`
	Account              string  `json:"account,omitempty"`
	Window               string  `json:"window,omitempty"`
	Used                 float64 `json:"used,omitempty"`
	ResetAt              string  `json:"reset_at,omitempty"`
	Level                string  `json:"level,omitempty"`
	Code                 string  `json:"code,omitempty"`
	ContentTruncated     bool    `json:"content_truncated,omitempty"`
	RawTruncated         bool    `json:"raw_truncated,omitempty"`
	OriginalContentBytes int64   `json:"original_content_bytes,omitempty"`
	OriginalRawBytes     int64   `json:"original_raw_bytes,omitempty"`
}
