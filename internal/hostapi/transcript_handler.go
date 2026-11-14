package hostapi

import (
	"context"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	transcriptv1 "github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/transcript/v1/transcriptv1connect"
)

const transcriptUnaryLimit = 5 * time.Second

type transcriptHandler struct{ backend app.Backend }

type transcriptProvider interface {
	TranscriptService() (*app.Transcripts, error)
}

func (h *transcriptHandler) auth(ctx context.Context) (*app.Transcripts, string, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "transcripts")
	if authErr != nil {
		return nil, "", authErr
	}
	provider, ok := h.backend.(transcriptProvider)
	if !ok {
		return nil, "", toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the transcript service is unavailable"))
	}
	svc, err := provider.TranscriptService()
	if err != nil {
		return nil, "", toConnectError(ctx, h.backend, err)
	}
	return svc, principal, nil
}

func (h *transcriptHandler) TranscriptPage(ctx context.Context, req *connect.Request[transcriptv1.TranscriptPageRequest]) (*connect.Response[transcriptv1.TranscriptPageResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, transcriptUnaryLimit)
	defer cancel()
	limit := req.Msg.Limit
	if limit > 500 {
		limit = 500
	}
	entries, err := svc.TranscriptPageFor(ctx, principal, app.TranscriptPageParams{
		Session: req.Msg.SessionId, Before: req.Msg.BeforeSequence, Limit: int(limit),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	response := &transcriptv1.TranscriptPageResponse{}
	for i := range entries {
		response.Entries = append(response.Entries, protoTranscriptEntry(&entries[i]))
	}
	return connect.NewResponse(response), nil
}

func (h *transcriptHandler) TranscriptItem(ctx context.Context, req *connect.Request[transcriptv1.TranscriptItemRequest]) (*connect.Response[transcriptv1.TranscriptItemResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, transcriptUnaryLimit)
	defer cancel()
	detail, err := svc.TranscriptItemFor(ctx, principal, req.Msg.SessionId, req.Msg.ItemId)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&transcriptv1.TranscriptItemResponse{Item: &transcriptv1.TranscriptDetail{
		Entry: protoTranscriptEntry(&detail.TranscriptEntry), ContentJson: detail.Content,
	}}), nil
}

func protoTranscriptEntry(entry *app.TranscriptEntry) *transcriptv1.TranscriptEntry {
	return &transcriptv1.TranscriptEntry{
		ItemId: entry.ID, TurnId: entry.TurnID, Kind: protoTranscriptKind(entry.Kind),
		Sequence: entry.Sequence, Revision: entry.Revision, CreatedAt: entry.CreatedAt, UpdatedAt: entry.UpdatedAt,
		Metadata: protoTranscriptMetadata(&entry.Metadata), View: protoTranscriptView(entry.View),
		ContentLocked: entry.ContentLocked, TurnOrigin: entry.TurnOrigin,
	}
}

func protoTranscriptView(view app.TranscriptView) *transcriptv1.TranscriptView {
	out := &transcriptv1.TranscriptView{
		Text: view.Text, Truncated: view.Truncated, CutAt: safeUint32(view.CutAt), OmittedBytes: safeUint32(view.OmittedBytes),
	}
	for i := range view.Files {
		file := &view.Files[i]
		out.Files = append(out.Files, &transcriptv1.TranscriptFileSummary{
			Header: file.Header, Added: safeUint32(file.Added), Removed: safeUint32(file.Removed),
		})
	}
	return out
}

func protoTranscriptMetadata(metadata *app.TranscriptMetadata) *transcriptv1.TranscriptMetadata {
	var exitCode *int32
	if metadata.ExitCode != nil {
		value := safeInt32(*metadata.ExitCode)
		exitCode = &value
	}
	return &transcriptv1.TranscriptMetadata{
		Origin: metadata.Origin, Model: metadata.Model, ReasoningState: metadata.ReasoningState,
		Tool: metadata.Tool, Status: metadata.Status, ExitCode: exitCode, DurationMillis: metadata.DurationMillis,
		ChangeKind: metadata.ChangeKind, LinesAdded: metadata.LinesAdded, LinesRemoved: metadata.LinesRemoved,
		RequestKind: metadata.RequestKind, Decision: metadata.Decision, Decider: metadata.Decider, Receipt: metadata.Receipt,
		StepCount: metadata.StepCount, CurrentStep: metadata.CurrentStep, Source: metadata.Source, ProposalId: metadata.ProposalID,
		AgentCount: metadata.AgentCount, TaskId: metadata.TaskID, Agent: metadata.Agent,
		InputTokens: metadata.InputTokens, OutputTokens: metadata.OutputTokens, CacheTokens: metadata.CacheTokens,
		Cost: metadata.Cost, Account: metadata.Account, Window: metadata.Window, Used: metadata.Used,
		ResetAt: metadata.ResetAt, Level: metadata.Level, Code: metadata.Code,
		ContentTruncated: metadata.ContentTruncated, RawTruncated: metadata.RawTruncated,
		OriginalContentBytes: metadata.OriginalContentBytes, OriginalRawBytes: metadata.OriginalRawBytes,
	}
}

func protoTranscriptKind(kind app.TranscriptKind) transcriptv1.TranscriptKind {
	switch string(kind) {
	case "user_message":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USER_MESSAGE
	case "assistant_message":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_ASSISTANT_MESSAGE
	case "reasoning":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_REASONING
	case "tool_call":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TOOL_CALL
	case "file_change":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_FILE_CHANGE
	case "approval":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_APPROVAL
	case "plan":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_PLAN
	case "subagent":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_SUBAGENT
	case "command":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_COMMAND
	default:
		return protoTranscriptSpecialKind(string(kind))
	}
}

func protoTranscriptSpecialKind(kind string) transcriptv1.TranscriptKind {
	switch kind {
	case "team_proposal":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TEAM_PROPOSAL
	case "task_link":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_TASK_LINK
	case "usage":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_USAGE
	case "quota":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_QUOTA
	case "notice":
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_NOTICE
	default:
		return transcriptv1.TranscriptKind_TRANSCRIPT_KIND_UNSPECIFIED
	}
}

func safeUint32(value int) uint32 {
	if value <= 0 {
		return 0
	}
	if uint64(value) > uint64(^uint32(0)) {
		return ^uint32(0)
	}
	return uint32(value)
}

func safeInt32(value int) int32 {
	if value > int(^uint32(0)>>1) {
		return int32(^uint32(0) >> 1)
	}
	if value < -int(^uint32(0)>>1)-1 {
		return -int32(^uint32(0)>>1) - 1
	}
	return int32(value)
}

var _ transcriptv1connect.TranscriptServiceHandler = (*transcriptHandler)(nil)
