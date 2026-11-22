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

// ListTeams returns metadata-only team summaries, optionally filtered by
// workspace_id and/or session_id (host-api v0.19 §5.3). Succeeds while the
// vault is locked (INV-09).
func (h *tasksHandler) ListTeams(ctx context.Context, req *connect.Request[tasksv1.ListTeamsRequest]) (*connect.Response[tasksv1.ListTeamsResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	teams, err := svc.ListTeams(ctx, req.Msg.GetWorkspaceId(), req.Msg.GetSessionId())
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*tasksv1.TeamSummary, 0, len(teams))
	for i := range teams {
		out = append(out, protoTeamSummary(&teams[i]))
	}
	return connect.NewResponse(&tasksv1.ListTeamsResponse{Teams: out}), nil
}

// GetTeam returns the read model for one team (§5.3). When the vault is
// locked, it returns metadata-only TaskNodes with vault_locked = true (INV-09).
func (h *tasksHandler) GetTeam(ctx context.Context, req *connect.Request[tasksv1.GetTeamRequest]) (*connect.Response[tasksv1.GetTeamResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	view, err := svc.GetTeam(ctx, req.Msg.GetTeamId(), req.Msg.GetDiffTaskId())
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&tasksv1.GetTeamResponse{Team: protoTeamView(&view)}), nil
}

// RecoverTask applies a local user's recovery decision (resume, keep_diff, or
// cancel) to an unknown task with command_id deduplication (§5.3).
func (h *tasksHandler) RecoverTask(ctx context.Context, req *connect.Request[tasksv1.RecoverTaskRequest]) (*connect.Response[tasksv1.RecoverTaskResponse], error) {
	svc, err := h.service(ctx)
	if err != nil {
		return nil, err
	}
	node, err := svc.RecoverTask(ctx, &app.RecoverTaskCommand{
		CommandID: req.Msg.GetCommandId(),
		TeamID:    req.Msg.GetTeamId(),
		TaskID:    req.Msg.GetTaskId(),
		Decision:  req.Msg.GetDecision(),
		Reason:    req.Msg.GetReason(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&tasksv1.RecoverTaskResponse{Task: protoTaskNode(&node)}), nil
}

func protoTeamSummary(s *app.TeamSummaryView) *tasksv1.TeamSummary {
	out := &tasksv1.TeamSummary{
		TeamId:        s.TeamID,
		WorkspaceId:   s.WorkspaceID,
		SessionId:     s.SessionID,
		State:         s.State,
		WaitingReason: s.WaitingReason,
		TaskCount:     s.TaskCount,
		RunningCount:  s.RunningCount,
		AskingCount:   s.AskingCount,
		FailedCount:   s.FailedCount,
		LastSeq:       s.LastSeq,
	}
	if s.CreatedAt > 0 {
		out.CreatedAt = timestamppb.New(time.UnixMilli(s.CreatedAt).UTC())
	}
	if s.UpdatedAt > 0 {
		out.UpdatedAt = timestamppb.New(time.UnixMilli(s.UpdatedAt).UTC())
	}
	if s.EndedAt > 0 {
		out.EndedAt = timestamppb.New(time.UnixMilli(s.EndedAt).UTC())
	}
	return out
}

func protoTeamView(v *app.TeamView) *tasksv1.TeamView {
	out := &tasksv1.TeamView{
		Summary:          protoTeamSummary(&v.Summary),
		VaultLocked:      v.VaultLocked,
		Title:            v.Title,
		Brief:            v.Brief,
		PendingRequestId: v.PendingRequestID,
		Tasks:            make([]*tasksv1.TaskNode, 0, len(v.Tasks)),
		DiffText:         v.DiffText,
		DiffTruncated:    v.DiffTruncated,
	}
	if v.Budget != nil {
		out.Budget = &tasksv1.TeamBudget{
			MaxAgents:          v.Budget.MaxAgents,
			MaxDurationSeconds: v.Budget.MaxDurationSeconds,
		}
	}
	for i := range v.Tasks {
		out.Tasks = append(out.Tasks, protoTaskNode(&v.Tasks[i]))
	}
	return out
}

func protoTaskNode(n *app.TaskNodeView) *tasksv1.TaskNode {
	return &tasksv1.TaskNode{
		TaskId:         n.TaskID,
		ParentTaskId:   n.ParentTaskID,
		RetryOf:        n.RetryOf,
		Attempt:        n.Attempt,
		HierarchyRole:  n.HierarchyRole,
		CatalogRole:    n.CatalogRole,
		Vendor:         n.Vendor,
		Model:          n.Model,
		AccountId:      n.AccountID,
		State:          n.State,
		FailureCode:    n.FailureCode,
		SessionId:      n.SessionID,
		Goal:           n.Goal,
		WriteScope:     n.WriteScope,
		DependsOn:      n.DependsOn,
		LastChannelSeq: n.LastChannelSeq,
		ElapsedMs:      n.ElapsedMs,
	}
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
