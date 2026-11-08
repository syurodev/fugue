package transcript

import (
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Kind identifies a neutral transcript item.
type Kind string

// Item kinds understood by the transcript model.
const (
	UserMessage      Kind = "user_message"
	AssistantMessage Kind = "assistant_message"
	Reasoning        Kind = "reasoning"
	ToolCall         Kind = "tool_call"
	FileChange       Kind = "file_change"
	Approval         Kind = "approval"
	Plan             Kind = "plan"
	Subagent         Kind = "subagent"
	Command          Kind = "command"
	TeamProposal     Kind = "team_proposal"
	TaskLink         Kind = "task_link"
	Usage            Kind = "usage"
	Quota            Kind = "quota"
	Notice           Kind = "notice"
)

type (
	// UserMessageMetadata contains the UserMessage metadata described in transcript §3.
	UserMessageMetadata struct{ Origin string }
	// UserMessageContent contains the UserMessage protected content described in transcript §3.
	UserMessageContent struct {
		Text        string
		Attachments []string
	}
)

type (
	// AssistantMessageMetadata contains the AssistantMessage metadata described in transcript §3.
	AssistantMessageMetadata struct{ ModelID string }
	// AssistantMessageContent contains the AssistantMessage protected content described in transcript §3.
	AssistantMessageContent struct{ Text string }
	// ReasoningMetadata contains the Reasoning metadata described in transcript §3.
	ReasoningMetadata struct{ Status string }
	// ReasoningContent contains the Reasoning protected content described in transcript §3.
	ReasoningContent struct{ Text string }
	// ToolCallMetadata contains the ToolCall metadata described in transcript §3.
	ToolCallMetadata struct {
		ToolName, Status string
		ExitCode         *int
		Duration         time.Duration
	}
)

type (
	// ToolCallContent contains the ToolCall protected content described in transcript §3.
	ToolCallContent struct{ Title, Input, Output string }
	// FileChangeMetadata contains the FileChange metadata described in transcript §3.
	FileChangeMetadata struct {
		Change         string
		Added, Removed int
	}
)

type (
	// FileChangeContent contains the FileChange protected content described in transcript §3.
	FileChangeContent struct{ Path, OldPath, Diff string }
	// ApprovalMetadata contains the Approval metadata described in transcript §3.
	ApprovalMetadata struct{ RequestType, Decision, Decider, Receipt string }
	// ApprovalContent contains the Approval protected content described in transcript §3.
	ApprovalContent struct{ Request string }
	// PlanMetadata contains the Plan metadata described in transcript §3.
	PlanMetadata struct{ Steps, Current int }
	// PlanContent contains the Plan protected content described in transcript §3.
	PlanContent struct{ Steps []string }
	// SubagentMetadata contains the Subagent metadata described in transcript §3.
	SubagentMetadata struct{ Status string }
	// SubagentContent contains the Subagent protected content described in transcript §3.
	SubagentContent struct{ Name, Description, Result string }
	// CommandMetadata contains the Command metadata described in transcript §3.
	CommandMetadata struct{ Source string }
	// CommandContent contains the Command protected content described in transcript §3.
	CommandContent struct {
		Name string
		Args []string
	}
)

// TeamProposalMetadata contains the proposal identifiers and decision.
type TeamProposalMetadata struct {
	ProposalID string
	Agents     int
	Decision   string
}
type (
	// TeamProposalContent contains the TeamProposal protected content described in transcript §3.
	TeamProposalContent struct{ Plan string }
	// TaskLinkMetadata contains the TaskLink metadata described in transcript §3.
	TaskLinkMetadata struct{ TaskID, AgentID, Status string }
	// TaskLinkContent contains the TaskLink protected content described in transcript §3.
	TaskLinkContent struct{ Summary string }
	// UsageMetadata contains the Usage metadata described in transcript §3.
	UsageMetadata struct {
		InputTokens, OutputTokens, CacheTokens int64
		Cost                                   float64
	}
)

type (
	// UsageContent contains the Usage protected content described in transcript §3.
	UsageContent struct{}
	// QuotaMetadata contains the Quota metadata described in transcript §3.
	QuotaMetadata struct {
		AccountID, Window string
		Used              float64
		ResetsAt          time.Time
	}
)

type (
	// QuotaContent contains the Quota protected content described in transcript §3.
	QuotaContent struct{}
	// NoticeMetadata contains the Notice metadata described in transcript §3.
	NoticeMetadata struct{ Level, Code string }
	// NoticeContent contains the Notice protected content described in transcript §3.
	NoticeContent struct{ Text string }
)

// Item contains stable identity, metadata and protected content.
type Item struct {
	ID, SessionID, TurnID                string
	Kind                                 Kind
	Seq, Revision                        uint64
	CreatedAt, UpdatedAt                 time.Time
	RawRef                               string
	ContentTruncated, RawTruncated       bool
	ContentOriginalSize, RawOriginalSize int
	Metadata                             Metadata
	Content                              Content
}

// UnsupportedEvent retains an unrecognized event as a notice.
func UnsupportedEvent(id, sessionID, turnID, rawRef string) Item {
	return Item{ID: id, SessionID: sessionID, TurnID: turnID, Kind: Notice, RawRef: rawRef, Metadata: NoticeMetadata{Level: "info", Code: "unsupported_event"}, Content: NoticeContent{}}
}

func valid(value string, choices ...string) bool {
	for _, c := range choices {
		if c == value {
			return true
		}
	}
	return false
}

// Validate rejects unknown kinds and invalid metadata.
func (i *Item) Validate() error {
	if i.Metadata == nil || i.Metadata.transcriptMetadata() != i.Kind || (i.Content != nil && i.Content.transcriptContent() != i.Kind) || !i.Metadata.valid() || !i.validCommon() {
		return errcode.New(errcode.InvalidArgument, "invalid transcript kind or metadata")
	}
	return nil
}

func (i *Item) validCommon() bool {
	return i.ID != "" && i.SessionID != "" && i.ContentOriginalSize >= 0 && i.RawOriginalSize >= 0 && i.ContentTruncated == (i.ContentOriginalSize > 0) && i.RawTruncated == (i.RawOriginalSize > 0)
}

// ValidateUpdate checks stable identity and advancing revision.
func ValidateUpdate(old, next *Item) error {
	if old.ID != next.ID || old.Kind != next.Kind || old.SessionID != next.SessionID || old.TurnID != next.TurnID || next.Revision <= old.Revision {
		return errcode.New(errcode.InvalidArgument, "invalid transcript update")
	}
	return next.Validate()
}

// Metadata restricts metadata to the allowlisted item types.
type (
	Metadata interface {
		transcriptMetadata() Kind
		valid() bool
	}
	// Content restricts protected content to transcript content types.
	Content interface{ transcriptContent() Kind }
)

func (UserMessageMetadata) transcriptMetadata() Kind { return UserMessage }
func (UserMessageContent) transcriptContent() Kind   { return UserMessage }

func (AssistantMessageMetadata) transcriptMetadata() Kind { return AssistantMessage }
func (AssistantMessageContent) transcriptContent() Kind   { return AssistantMessage }

func (ReasoningMetadata) transcriptMetadata() Kind { return Reasoning }
func (ReasoningContent) transcriptContent() Kind   { return Reasoning }

func (ToolCallMetadata) transcriptMetadata() Kind { return ToolCall }
func (ToolCallContent) transcriptContent() Kind   { return ToolCall }

func (FileChangeMetadata) transcriptMetadata() Kind { return FileChange }
func (FileChangeContent) transcriptContent() Kind   { return FileChange }

func (ApprovalMetadata) transcriptMetadata() Kind { return Approval }
func (ApprovalContent) transcriptContent() Kind   { return Approval }

func (PlanMetadata) transcriptMetadata() Kind { return Plan }
func (PlanContent) transcriptContent() Kind   { return Plan }

func (SubagentMetadata) transcriptMetadata() Kind { return Subagent }
func (SubagentContent) transcriptContent() Kind   { return Subagent }

func (CommandMetadata) transcriptMetadata() Kind { return Command }
func (CommandContent) transcriptContent() Kind   { return Command }

func (TeamProposalMetadata) transcriptMetadata() Kind { return TeamProposal }
func (TeamProposalContent) transcriptContent() Kind   { return TeamProposal }

func (TaskLinkMetadata) transcriptMetadata() Kind { return TaskLink }
func (TaskLinkContent) transcriptContent() Kind   { return TaskLink }

func (UsageMetadata) transcriptMetadata() Kind { return Usage }
func (UsageContent) transcriptContent() Kind   { return Usage }

func (QuotaMetadata) transcriptMetadata() Kind { return Quota }
func (QuotaContent) transcriptContent() Kind   { return Quota }

func (NoticeMetadata) transcriptMetadata() Kind { return Notice }
func (NoticeContent) transcriptContent() Kind   { return Notice }

func (m UserMessageMetadata) valid() bool { return valid(m.Origin, "user", "lead", "fugue") }

func (AssistantMessageMetadata) valid() bool { return true }

func (m ReasoningMetadata) valid() bool { return valid(m.Status, "plain", "summary", "redacted") }

func (m ToolCallMetadata) valid() bool {
	return valid(m.Status, "pending", "running", "succeeded", "failed", "denied", "unknown")
}

func (m FileChangeMetadata) valid() bool { return valid(m.Change, "add", "modify", "delete", "rename") }

func (m ApprovalMetadata) valid() bool {
	return m.Decider == "" || valid(m.Decider, "user", "policy", "lead")
}

func (PlanMetadata) valid() bool { return true }

func (m SubagentMetadata) valid() bool {
	return valid(m.Status, "pending", "running", "succeeded", "failed", "cancelled", "unknown")
}

func (m CommandMetadata) valid() bool { return valid(m.Source, "vendor", "fugue") }

func (m TeamProposalMetadata) valid() bool {
	return valid(m.Decision, "pending", "approved", "approved_edited", "solo", "rejected")
}

func (m TaskLinkMetadata) valid() bool {
	return valid(m.Status, "queued", "starting", "running", "asking", "checking", "review", "integrating", "done", "failed", "stopped", "cancelled", "unknown")
}

func (UsageMetadata) valid() bool { return true }

func (QuotaMetadata) valid() bool { return true }

func (m NoticeMetadata) valid() bool { return valid(m.Level, "info", "warning", "error") }
