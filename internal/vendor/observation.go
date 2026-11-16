package vendor

import (
	"time"

	"github.com/syurodev/fugue/internal/transcript"
)

// Update pairs the stored item with its next revision.
type Update struct {
	Before transcript.Item
	After  transcript.Item
}

// Init is the session initialization observation: identity, model, permission
// mode, and the vendor's slash command list.
type Init struct {
	Version        string
	SessionID      string
	Model          string
	PermissionMode string
	SlashCommands  []string
}

// Partial is one streaming delta, exposed as data so the session actor wires
// it to the transcript delta topic (transcript §5).
type Partial struct {
	MessageID string
	BlockType string
	DeltaType string
	Fragment  string
}

// UsageData is the turn usage the vendor reports.
type UsageData struct {
	InputTokens  int64
	OutputTokens int64
	CacheTokens  int64
	Cost         float64
}

// TurnEnd closes the active turn: outcome, failure code, usage and the
// credential expiry the CLI reported, if any.
type TurnEnd struct {
	TurnID              string
	Outcome             string
	FailureCode         string
	Usage               UsageData
	CredentialExpiresAt *time.Time
}

// Window is one quota window of a rate-limit observation.
type Window struct {
	Name        string
	Utilization float64
	ResetsAt    time.Time
	Status      string
}

// RateLimit carries the rate-limit window data for the quota owner.
type RateLimit struct {
	Windows []Window
}

// ServerRequestKind distinguishes a relayed vendor confirmation from a relayed question.
type ServerRequestKind string

const (
	// ServerRequestConfirm is a relayed vendor approval request (`item/*/requestApproval`).
	ServerRequestConfirm ServerRequestKind = "confirm"
	// ServerRequestQuestion is a relayed vendor user-input question (`item/tool/requestUserInput`).
	ServerRequestQuestion ServerRequestKind = "question"
)

// ServerRequestQuestionOption is one selectable option in a relayed question.
type ServerRequestQuestionOption struct {
	Key         string
	Label       string
	Description string
}

// ServerRequestQuestionItem is one question in a relayed vendor question request.
type ServerRequestQuestionItem struct {
	ID       string
	Header   string
	Question string
	Options  []ServerRequestQuestionOption
}

// ServerRequestOption is one selectable choice in a relayed confirmation
// request when the vendor supplies structured options (`optionId`, `name`,
// `kind`).
type ServerRequestOption struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind,omitempty"`
}

// ServerRequest describes a vendor server request that must be relayed verbatim
// through the interaction owner and answered via Session.Resolve.
type ServerRequest struct {
	RequestID       string
	TurnID          string
	ItemID          string
	Method          string
	Kind            ServerRequestKind
	Summary         string
	Detail          string
	Options         []string
	OptionDetails   []ServerRequestOption       `json:",omitempty"`
	Questions       []ServerRequestQuestionItem `json:",omitempty"`
	QuestionOptions []ServerRequestQuestionOption
	AllowFreeText   bool
	ExpiresAt       time.Time
}

// Observation is everything one vendor event normalized into.
type Observation struct {
	Init          *Init
	Records       []transcript.Item
	Updates       []Update
	Partial       *Partial
	TurnEnd       *TurnEnd
	Quota         *RateLimit
	ServerRequest *ServerRequest
	// Nested counts events with a parent_tool_use_id (subagent streams).
	Nested int
}
