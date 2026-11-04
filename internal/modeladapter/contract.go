package modeladapter

import "time"

// Request is the provider-neutral input for one model attempt.
type Request struct {
	Model      string
	Transcript []Message
	Tools      []Tool
	Reasoning  string
	MaxOutput  int
	CacheMarks []int
	Affinity   string
	Account    string
}

// Message is one transcript item. Content remains opaque to providers that do not
// need to inspect its representation.
type Message struct {
	Role    string
	Content string
}

// Tool is a tool declaration bound to the current request.
type Tool struct {
	Name        string
	Description string
	Schema      []byte
}

// EventKind identifies one contract stream event.
type EventKind string

// EventStart marks the beginning of provider output.
const (
	EventStart         EventKind = "start"
	EventTextStart     EventKind = "text_start"
	EventTextDelta     EventKind = "text_delta"
	EventTextEnd       EventKind = "text_end"
	EventThinkingStart EventKind = "thinking_start"
	EventThinkingDelta EventKind = "thinking_delta"
	EventThinkingEnd   EventKind = "thinking_end"
	EventToolCallStart EventKind = "toolcall_start"
	EventToolCallDelta EventKind = "toolcall_delta"
	EventToolCallEnd   EventKind = "toolcall_end"
	EventDone          EventKind = "done"
	EventError         EventKind = "error"
)

// StopReason describes a completed provider response.
type StopReason string

// StopReasonStop indicates normal completion.
const (
	StopReasonStop    StopReason = "stop"
	StopReasonToolUse StopReason = "tool_use"
	StopReasonLength  StopReason = "length"
	StopReasonRefusal StopReason = "refusal"
	StopReasonError   StopReason = "error"
	StopReasonAborted StopReason = "aborted"
)

// Usage is normalized token accounting supplied by a provider when known.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Reasoning  int
}

// Event is one ordered content or terminal event. Fields not relevant to Kind are zero.
type Event struct {
	Kind         EventKind
	ContentIndex int
	CallID       string
	Name         string
	Delta        string
	Arguments    string
	StopReason   StopReason
	Usage        Usage
	Failure      *Failure
}

// ErrorClass is a stable provider failure category used by retry and recovery policy.
type ErrorClass string

// ErrorRateLimit identifies provider rate limiting.
const (
	ErrorRateLimit       ErrorClass = "rate_limit"
	ErrorOverloaded      ErrorClass = "overloaded"
	ErrorServer          ErrorClass = "server"
	ErrorTimeout         ErrorClass = "timeout"
	ErrorTransport       ErrorClass = "transport"
	ErrorEmptyResponse   ErrorClass = "empty_response"
	ErrorContextOverflow ErrorClass = "context_overflow"
	ErrorAuth            ErrorClass = "auth"
	ErrorPermission      ErrorClass = "permission"
	ErrorQuota           ErrorClass = "quota"
	ErrorInvalidRequest  ErrorClass = "invalid_request"
	ErrorModelNotFound   ErrorClass = "model_not_found"
	ErrorRefusal         ErrorClass = "refusal"
	ErrorContentFilter   ErrorClass = "content_filter"
)

// Failure is a terminal, classified provider failure. Message must not contain
// prompt contents, credentials, or other protected payloads.
type Failure struct {
	Class      ErrorClass
	Code       string
	Message    string
	RetryAfter time.Duration
}

// Retryable reports whether the design classifies class for automatic retry.
func (class ErrorClass) Retryable() bool {
	switch class {
	case ErrorRateLimit, ErrorOverloaded, ErrorServer, ErrorTimeout, ErrorTransport, ErrorEmptyResponse:
		return true
	case ErrorContextOverflow, ErrorAuth, ErrorPermission, ErrorQuota, ErrorInvalidRequest,
		ErrorModelNotFound, ErrorRefusal, ErrorContentFilter:
		return false
	default:
		return false
	}
}
