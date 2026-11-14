package client

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1/transcriptv1connect"
	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
)

// Session is one vendor session's list shape: identity and state only, the
// same fields the vendor-sessions topic carries (Q-58 A).
type Session struct {
	// ID is the session's Host API identifier.
	ID string
	// Vendor names the vendor CLI; empty when the host does not map it.
	Vendor string
	// State is the session's vendor state, for example "running".
	State string
	// WaitingReason is why the session waits, when it waits.
	WaitingReason string
	// CreatedAt and UpdatedAt are the host's timestamps.
	CreatedAt, UpdatedAt string
}

// SessionList is the answer to ListSessions: every session and the
// vendor-sessions topic sequence of the list, so a caller subscribes from it
// without a gap.
type SessionList struct {
	Sessions []Session
	Sequence uint64
}

// ListSessions reads every vendor session (vendor-runtime F9.9c). It fails
// with the connect error the host answered; a locked vault does not block it.
func (c *Client) ListSessions(ctx context.Context) (SessionList, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).ListSessions(ctx,
		connect.NewRequest(&vendorsv1.ListSessionsRequest{}))
	if err != nil {
		return SessionList{}, fmt.Errorf("list sessions: %w", err)
	}
	out := SessionList{Sequence: res.Msg.GetSequence()}
	for _, s := range res.Msg.GetSessions() {
		out.Sessions = append(out.Sessions, Session{
			ID: s.GetSessionId(), Vendor: vendorString(s.GetVendor()), State: s.GetState(),
			WaitingReason: s.GetWaitingReason(), CreatedAt: s.GetCreatedAt(), UpdatedAt: s.GetUpdatedAt(),
		})
	}
	return out, nil
}

// vendorString maps the wire vendor to its name; an unspecified vendor keeps
// the empty name, exactly what the host sends it for.
func vendorString(v vendorsv1.Vendor) string {
	switch v {
	case vendorsv1.Vendor_VENDOR_CLAUDE:
		return "claude"
	case vendorsv1.Vendor_VENDOR_CODEX:
		return "codex"
	case vendorsv1.Vendor_VENDOR_AGY:
		return "agy"
	default:
		return ""
	}
}

// TranscriptFileSummary is one file's place in a file_change view.
type TranscriptFileSummary struct {
	Header         string
	Added, Removed uint32
}

// TranscriptView is a transcript item's redacted display view (transcript §7).
type TranscriptView struct {
	// Text is the display text; for a file_change it is the diff.
	Text      string
	Truncated bool
	// CutAt is where Text was cut, and OmittedBytes what was hidden.
	CutAt, OmittedBytes uint32
	// Files summarizes a file_change diff per file.
	Files []TranscriptFileSummary
}

// TranscriptMetadata is the item's allowlisted metadata (transcript §3). Only
// identifiers and numbers: nothing the vendor's content names.
type TranscriptMetadata struct {
	Origin, Model, ReasoningState string
	Tool, Status                  string
	ExitCode                      *int32
	DurationMillis                int64
	ChangeKind                    string
	LinesAdded, LinesRemoved      int64
	RequestKind                   string
	Decision, Decider, Receipt    string
	StepCount, CurrentStep        int64
	Source, ProposalID            string
	AgentCount                    int64
	TaskID, Agent                 string
	InputTokens, OutputTokens     int64
	CacheTokens                   int64
	Cost                          float64
	Account, Window               string
	Used                          float64
	ResetAt, Level, Code          string
	ContentTruncated              bool
	RawTruncated                  bool
	OriginalContentBytes          int64
	OriginalRawBytes              int64
}

// TranscriptItem is one transcript entry at its latest revision (transcript
// §6): metadata and redacted display view. Kind is the item kind's name, for
// example "user_message"; an empty Kind means the host sent a kind this
// client's proto does not name, and the item is still carried whole.
type TranscriptItem struct {
	ItemID, TurnID string
	Kind           string
	Sequence       uint64
	Revision       uint64
	CreatedAt      string
	UpdatedAt      string
	Metadata       TranscriptMetadata
	View           TranscriptView
	ContentLocked  bool
	TurnOrigin     string
}

// TranscriptDetail is TranscriptItem's answer: the item plus the full stored
// content, redacted, when the vault is open (transcript §6).
type TranscriptDetail struct {
	Item    TranscriptItem
	Content []byte
}

// TranscriptPage reads one page of a session's transcript, newest first
// (transcript §6). Before zero reads the newest page; a positive Before reads
// the page strictly older than that creation sequence. Limit zero uses the
// host's default of 200 items.
func (c *Client) TranscriptPage(ctx context.Context, sessionID string, before int64, limit uint32) ([]TranscriptItem, error) {
	res, err := transcriptv1connect.NewTranscriptServiceClient(c.hc, c.base).TranscriptPage(ctx,
		connect.NewRequest(&transcriptv1.TranscriptPageRequest{
			SessionId: sessionID, BeforeSequence: before, Limit: limit,
		}))
	if err != nil {
		return nil, fmt.Errorf("read the transcript page: %w", err)
	}
	out := make([]TranscriptItem, 0, len(res.Msg.GetEntries()))
	for _, e := range res.Msg.GetEntries() {
		out = append(out, itemFromProto(e))
	}
	return out, nil
}

// TranscriptItem reads one item at its latest revision with its full stored
// content, redacted, when the vault is open.
func (c *Client) TranscriptItem(ctx context.Context, sessionID, itemID string) (TranscriptDetail, error) {
	res, err := transcriptv1connect.NewTranscriptServiceClient(c.hc, c.base).TranscriptItem(ctx,
		connect.NewRequest(&transcriptv1.TranscriptItemRequest{SessionId: sessionID, ItemId: itemID}))
	if err != nil {
		return TranscriptDetail{}, fmt.Errorf("read the transcript item: %w", err)
	}
	return TranscriptDetail{
		Item:    itemFromProto(res.Msg.GetItem().GetEntry()),
		Content: res.Msg.GetItem().GetContentJson(),
	}, nil
}

// itemFromProto maps the wire entry onto the public shape.
func itemFromProto(e *transcriptv1.TranscriptEntry) TranscriptItem {
	item := TranscriptItem{
		ItemID: e.GetItemId(), TurnID: e.GetTurnId(), Kind: kindString(e.GetKind()),
		Sequence: e.GetSequence(), Revision: e.GetRevision(),
		CreatedAt: e.GetCreatedAt(), UpdatedAt: e.GetUpdatedAt(),
		ContentLocked: e.GetContentLocked(), TurnOrigin: e.GetTurnOrigin(),
	}
	if m := e.GetMetadata(); m != nil {
		item.Metadata = metadataFromProto(m)
	}
	if v := e.GetView(); v != nil {
		item.View = viewFromProto(v)
	}
	return item
}

// metadataFromProto maps the wire metadata onto the public shape.
func metadataFromProto(m *transcriptv1.TranscriptMetadata) TranscriptMetadata {
	return TranscriptMetadata{
		Origin: m.GetOrigin(), Model: m.GetModel(), ReasoningState: m.GetReasoningState(),
		Tool: m.GetTool(), Status: m.GetStatus(), ExitCode: m.ExitCode, DurationMillis: m.GetDurationMillis(),
		ChangeKind: m.GetChangeKind(), LinesAdded: m.GetLinesAdded(), LinesRemoved: m.GetLinesRemoved(),
		RequestKind: m.GetRequestKind(), Decision: m.GetDecision(), Decider: m.GetDecider(), Receipt: m.GetReceipt(),
		StepCount: m.GetStepCount(), CurrentStep: m.GetCurrentStep(), Source: m.GetSource(), ProposalID: m.GetProposalId(),
		AgentCount: m.GetAgentCount(), TaskID: m.GetTaskId(), Agent: m.GetAgent(),
		InputTokens: m.GetInputTokens(), OutputTokens: m.GetOutputTokens(), CacheTokens: m.GetCacheTokens(),
		Cost: m.GetCost(), Account: m.GetAccount(), Window: m.GetWindow(), Used: m.GetUsed(),
		ResetAt: m.GetResetAt(), Level: m.GetLevel(), Code: m.GetCode(),
		ContentTruncated: m.GetContentTruncated(), RawTruncated: m.GetRawTruncated(),
		OriginalContentBytes: m.GetOriginalContentBytes(), OriginalRawBytes: m.GetOriginalRawBytes(),
	}
}

// viewFromProto maps the wire view onto the public shape.
func viewFromProto(v *transcriptv1.TranscriptView) TranscriptView {
	out := TranscriptView{
		Text: v.GetText(), Truncated: v.GetTruncated(), CutAt: v.GetCutAt(), OmittedBytes: v.GetOmittedBytes(),
	}
	for _, f := range v.GetFiles() {
		out.Files = append(out.Files, TranscriptFileSummary{Header: f.GetHeader(), Added: f.GetAdded(), Removed: f.GetRemoved()})
	}
	return out
}

// kindString maps the wire kind to its name (transcript §3); an unspecified
// kind keeps the empty name, and an unknown item is still carried whole.
func kindString(k transcriptv1.TranscriptKind) string {
	switch k {
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USER_MESSAGE:
		return "user_message"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_ASSISTANT_MESSAGE:
		return "assistant_message"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_REASONING:
		return "reasoning"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TOOL_CALL:
		return "tool_call"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_FILE_CHANGE:
		return "file_change"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_APPROVAL:
		return "approval"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_PLAN:
		return "plan"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_SUBAGENT:
		return "subagent"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_COMMAND:
		return "command"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TEAM_PROPOSAL:
		return "team_proposal"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TASK_LINK:
		return "task_link"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USAGE:
		return "usage"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_QUOTA:
		return "quota"
	case transcriptv1.TranscriptKind_TRANSCRIPT_KIND_NOTICE:
		return "notice"
	default:
		return ""
	}
}
