package hostapi

import (
	"context"
	"math"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	tasksv1 "github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/tasks/v1/tasksv1connect"
)

// tasksHandler maps fugue.tasks.v1 onto the app TasksService (host-api v0.17
// §5.3). Only local principals pass: vendor session principals are denied by
// the transport's session phase before the handler runs, and the service
// re-checks the vault for every content read, so every path fails closed.
type tasksHandler struct{ backend app.Backend }

type tasksProvider interface {
	TasksService() (*app.TasksService, error)
}

func (h *tasksHandler) service(ctx context.Context) (*app.TasksService, error) {
	if _, authErr := localSessionPrincipal(ctx, h.backend, "the team channel"); authErr != nil {
		return nil, authErr
	}
	provider, ok := h.backend.(tasksProvider)
	if !ok {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the tasks service is unavailable"))
	}
	svc, err := provider.TasksService()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return svc, nil
}

// PageChannel pages one team's channel: without thread_task_id the general
// channel, with it that task's thread; newest first with before/since as
// exclusive cursors and a limit of at most 500 (§5.3).
func (h *tasksHandler) PageChannel(ctx context.Context, req *connect.Request[tasksv1.PageChannelRequest]) (*connect.Response[tasksv1.PageChannelResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	lines, err := svc.PageChannel(ctx, req.Msg.GetTeamId(), req.Msg.GetThreadTaskId(),
		req.Msg.GetBefore(), req.Msg.GetSince(), int(req.Msg.GetLimit()))
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*tasksv1.ChannelLine, 0, len(lines))
	for i := range lines {
		out = append(out, protoChannelLine(&lines[i]))
	}
	return connect.NewResponse(&tasksv1.PageChannelResponse{Lines: out}), nil
}

// PostChannelMessage records one §6.7 user post and delivers it to the routed
// agent immediately; command_id dedup makes a replay return the same `#n`
// without a second line (§5.3).
func (h *tasksHandler) PostChannelMessage(ctx context.Context, req *connect.Request[tasksv1.PostChannelMessageRequest]) (*connect.Response[tasksv1.PostChannelMessageResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	result, err := svc.PostChannelMessage(ctx, &app.PostChannelCommand{
		CommandID:    req.Msg.GetCommandId(),
		TeamID:       req.Msg.GetTeamId(),
		ThreadTaskID: req.Msg.GetThreadTaskId(),
		ToTaskID:     req.Msg.GetToTaskId(),
		Text:         req.Msg.GetText(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&tasksv1.PostChannelMessageResponse{ChannelSeq: result.ChannelSeq}), nil
}

// PruneWorktrees removes ended-team task worktrees and branches past the
// retention window (orchestration §9, F11.11).
func (h *tasksHandler) PruneWorktrees(ctx context.Context, req *connect.Request[tasksv1.PruneWorktreesRequest]) (*connect.Response[tasksv1.PruneWorktreesResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	sec := req.Msg.GetRetentionSeconds()
	if sec < 0 || sec > math.MaxInt64/int64(time.Second) {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "retention_seconds is out of range"))
	}
	retention := time.Duration(sec) * time.Second
	res, err := svc.PruneWorktrees(ctx, retention)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	adminEntries := clampInt32(res.RemovedAdminEntries)
	return connect.NewResponse(&tasksv1.PruneWorktreesResponse{
		RemovedWorktrees:    res.RemovedWorktrees,
		RemovedBranches:     res.RemovedBranches,
		RemovedAdminEntries: adminEntries,
	}), nil
}

func clampInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < math.MinInt32 {
		return math.MinInt32
	}
	return int32(n)
}

func protoChannelLine(v *app.ChannelLineView) *tasksv1.ChannelLine {
	return &tasksv1.ChannelLine{
		ChannelSeq:       v.ChannelSeq,
		Kind:             v.Kind,
		SenderKind:       v.SenderKind,
		SenderTaskId:     v.SenderTaskID,
		RecipientTaskId:  v.RecipientTaskID,
		ThreadRootTaskId: v.ThreadRoot,
		Attempt:          v.Attempt,
		SourceSeq:        v.SourceSeq,
		CreatedAt:        timestamppb.New(time.UnixMilli(v.CreatedAt).UTC()),
		Content:          v.Content,
	}
}

var _ tasksv1connect.TaskServiceHandler = (*tasksHandler)(nil)
