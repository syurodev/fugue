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
	// UserMessageMetadata holds the corresponding item fields.
	UserMessageMetadata struct{ Origin string }
	// UserMessageContent holds the corresponding item fields.
	UserMessageContent struct {
		Text        string
		Attachments []string
	}
)

type (
	// AssistantMessageMetadata holds the corresponding item fields.
	AssistantMessageMetadata struct{ ModelID string }
	// AssistantMessageContent holds the corresponding item fields.
	AssistantMessageContent struct{ Text string }
	// ReasoningMetadata holds the corresponding item fields.
	ReasoningMetadata struct{ Status string }
	// ReasoningContent holds the corresponding item fields.
	ReasoningContent struct{ Text string }
	// ToolCallMetadata holds the corresponding item fields.
	ToolCallMetadata struct {
		ToolName, Status string
		ExitCode         *int
		Duration         time.Duration
	}
)

type (
	// ToolCallContent holds the corresponding item fields.
	ToolCallContent struct{ Title, Input, Output string }
	// FileChangeMetadata holds the corresponding item fields.
	FileChangeMetadata struct {
		Change         string
		Added, Removed int
	}
)

type (
	// FileChangeContent holds the corresponding item fields.
	FileChangeContent struct{ Path, OldPath, Diff string }
	// ApprovalMetadata holds the corresponding item fields.
	ApprovalMetadata struct{ RequestType, Decision, Decider, Receipt string }
	// ApprovalContent holds the corresponding item fields.
	ApprovalContent struct{ Request string }
	// PlanMetadata holds the corresponding item fields.
	PlanMetadata struct{ Steps, Current int }
	// PlanContent holds the corresponding item fields.
	PlanContent struct{ Steps []string }
	// SubagentMetadata holds the corresponding item fields.
	SubagentMetadata struct{ Status string }
	// SubagentContent holds the corresponding item fields.
	SubagentContent struct{ Name, Description, Result string }
	// CommandMetadata holds the corresponding item fields.
	CommandMetadata struct{ Source string }
	// CommandContent holds the corresponding item fields.
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
	// TeamProposalContent holds the corresponding item fields.
	TeamProposalContent struct{ Plan string }
	// TaskLinkMetadata holds the corresponding item fields.
	TaskLinkMetadata struct{ TaskID, AgentID, Status string }
	// TaskLinkContent holds the corresponding item fields.
	TaskLinkContent struct{ Summary string }
	// UsageMetadata holds the corresponding item fields.
	UsageMetadata struct {
		InputTokens, OutputTokens, CacheTokens int64
		Cost                                   float64
	}
)

type (
	// UsageContent holds the corresponding item fields.
	UsageContent struct{}
	// QuotaMetadata holds the corresponding item fields.
	QuotaMetadata struct {
		AccountID, Window string
		Used              float64
		ResetsAt          time.Time
	}
)

type (
	// QuotaContent holds the corresponding item fields.
	QuotaContent struct{}
	// NoticeMetadata holds the corresponding item fields.
	NoticeMetadata struct{ Level, Code string }
	// NoticeContent holds the corresponding item fields.
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
	if i.Metadata == nil || i.Metadata.transcriptMetadata() != i.Kind || (i.Content != nil && i.Content.transcriptContent() != i.Kind) || !i.validMetadata() {
		return errcode.New(errcode.InvalidArgument, "invalid transcript kind or metadata")
	}
	return nil
}

func (i *Item) validMetadata() bool {
	var ok bool
	switch i.Kind {
	case UserMessage:
		m, yes := i.Metadata.(UserMessageMetadata)
		return validateOrigin(m, yes)
	case AssistantMessage:
		_, ok = i.Metadata.(AssistantMessageMetadata)
	case Reasoning:
		m, yes := i.Metadata.(ReasoningMetadata)
		ok = yes && valid(m.Status, "plain", "summary", "redacted")
	case ToolCall:
		m, yes := i.Metadata.(ToolCallMetadata)
		ok = yes && valid(m.Status, "pending", "running", "succeeded", "failed", "denied", "unknown")
	case FileChange:
		m, yes := i.Metadata.(FileChangeMetadata)
		ok = yes && valid(m.Change, "add", "modify", "delete", "rename")
	case Approval:
		m, yes := i.Metadata.(ApprovalMetadata)
		return validApproval(m, yes)
	case Plan, Subagent, Command, TeamProposal, TaskLink, Usage, Quota, Notice:
		return i.validRemaining()
	default:
		return false
	}
	return ok
}

func (i *Item) validRemaining() bool {
	var ok bool
	switch i.Kind {
	case Plan:
		_, ok = i.Metadata.(PlanMetadata)
	case Subagent:
		m, yes := i.Metadata.(SubagentMetadata)
		return validateSubagent(m, yes)
	case Command:
		m, yes := i.Metadata.(CommandMetadata)
		ok = yes && valid(m.Source, "vendor", "fugue")
	case TeamProposal:
		m, yes := i.Metadata.(TeamProposalMetadata)
		ok = yes && valid(m.Decision, "pending", "approved", "approved_edited", "solo", "rejected")
	case TaskLink:
		m, yes := i.Metadata.(TaskLinkMetadata)
		ok = yes && valid(m.Status, "queued", "starting", "running", "asking", "checking", "review", "integrating", "done", "failed", "stopped", "cancelled", "unknown")
	case UserMessage, AssistantMessage, Reasoning, ToolCall, FileChange, Approval:
		return false
	case Usage, Quota, Notice:
		return i.validTerminal()
	default:
		return false
	}
	return ok
}

func (i *Item) validTerminal() bool {
	var ok bool
	switch i.Kind {
	case Usage:
		_, ok = i.Metadata.(UsageMetadata)
	case Quota:
		_, ok = i.Metadata.(QuotaMetadata)
	case Notice:
		m, yes := i.Metadata.(NoticeMetadata)
		ok = yes && valid(m.Level, "info", "warning", "error")
	case UserMessage, AssistantMessage, Reasoning, ToolCall, FileChange, Approval, Plan, Subagent, Command, TeamProposal, TaskLink:
		return false
	default:
		return false
	}
	return ok
}

// ValidateUpdate checks stable identity and advancing revision.
func ValidateUpdate(old, next *Item) error {
	if old.ID != next.ID || old.Kind != next.Kind || old.SessionID != next.SessionID || old.TurnID != next.TurnID || next.Revision <= old.Revision {
		return errcode.New(errcode.InvalidArgument, "invalid transcript update")
	}
	return next.Validate()
}

func validateOrigin(m UserMessageMetadata, ok bool) bool {
	return ok && valid(m.Origin, "user", "lead", "fugue")
}

func validateSubagent(m SubagentMetadata, ok bool) bool {
	return ok && valid(m.Status, "pending", "running", "succeeded", "failed", "cancelled", "unknown")
}

func validApproval(m ApprovalMetadata, ok bool) bool {
	return ok && (m.Decider == "" || valid(m.Decider, "user", "policy", "lead"))
}

// Metadata restricts metadata to the allowlisted item types.
type (
	Metadata interface{ transcriptMetadata() Kind }
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
