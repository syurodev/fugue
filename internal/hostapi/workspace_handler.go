package hostapi

import (
	"context"
	"slices"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

type workspaceHandler struct{ backend app.Backend }

func (h *workspaceHandler) auth(ctx context.Context) (app.WorkspaceBackend, string, error) {
	st, err := connectionState(ctx)
	if err != nil {
		return nil, "", toConnectError(ctx, h.backend, err)
	}
	if err := requireHelloed(ctx); err != nil {
		return nil, "", toConnectError(ctx, h.backend, err)
	}
	principal := string(st.principal())
	if !app.IsLocalPrincipal(principal) {
		return nil, "", toConnectError(ctx, h.backend, errcode.New(errcode.PermissionDenied, "only local principals may access workspaces"))
	}
	service, ok := h.backend.(app.WorkspaceBackend)
	if !ok {
		return nil, "", toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "workspace service is unavailable"))
	}
	return service, principal, nil
}

func (h *workspaceHandler) CreateWorkspace(ctx context.Context, req *connect.Request[workspacev1.CreateWorkspaceRequest]) (*connect.Response[workspacev1.CreateWorkspaceResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	ws, err := svc.CreateWorkspace(ctx, principal, req.Msg.Name)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.CreateWorkspaceResponse{Workspace: protoWorkspace(ws, raw)}), nil
}

func (h *workspaceHandler) ListWorkspaces(ctx context.Context, _ *connect.Request[workspacev1.ListWorkspacesRequest]) (*connect.Response[workspacev1.ListWorkspacesResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	items, err := svc.ListWorkspaces(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := &workspacev1.ListWorkspacesResponse{Workspaces: make([]*workspacev1.WorkspaceSummary, 0, len(items))}
	for _, ws := range items {
		out.Workspaces = append(out.Workspaces, protoWorkspaceSummary(ws))
	}
	return connect.NewResponse(out), nil
}

func (h *workspaceHandler) GetWorkspace(ctx context.Context, req *connect.Request[workspacev1.GetWorkspaceRequest]) (*connect.Response[workspacev1.GetWorkspaceResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	ws, raw, err := svc.GetWorkspace(ctx, principal, req.Msg.Id)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.GetWorkspaceResponse{Workspace: protoWorkspace(ws, raw)}), nil
}

func (h *workspaceHandler) GetEffectiveConfig(ctx context.Context, req *connect.Request[workspacev1.GetEffectiveConfigRequest]) (*connect.Response[workspacev1.GetEffectiveConfigResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	eff, _, err := svc.GetEffectiveWorkspaceConfig(ctx, principal, req.Msg.Id)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	value, err := structpb.NewStruct(eff.Values)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, errcode.Wrap(errcode.Internal, "encode effective workspace configuration", err))
	}
	return connect.NewResponse(&workspacev1.GetEffectiveConfigResponse{Values: value, Provenance: eff.Provenance}), nil
}

func (h *workspaceHandler) ValidateConfig(ctx context.Context, req *connect.Request[workspacev1.ValidateConfigRequest]) (*connect.Response[workspacev1.ValidateConfigResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	issue, err := svc.ValidateWorkspaceConfig(ctx, principal, req.Msg.RawToml)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	response := &workspacev1.ValidateConfigResponse{}
	if issue.Key != "" || issue.Line != 0 || issue.Column != 0 {
		response.Issues = append(response.Issues, &workspacev1.ConfigIssue{Key: issue.Key, Line: int64(issue.Line), Column: int64(issue.Column), Message: issue.Message})
	}
	return connect.NewResponse(response), nil
}

func (h *workspaceHandler) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[workspacev1.UpdateWorkspaceConfigRequest]) (*connect.Response[workspacev1.UpdateWorkspaceConfigResponse], error) {
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	ws, err := svc.UpdateWorkspaceConfig(ctx, principal, req.Msg.Id, req.Msg.RawToml, req.Msg.ExpectedHash)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.UpdateWorkspaceConfigResponse{Workspace: protoWorkspace(ws, raw)}), nil
}

func (h *workspaceHandler) ConfirmWorkspaceConfig(ctx context.Context, req *connect.Request[workspacev1.ConfirmWorkspaceConfigRequest]) (*connect.Response[workspacev1.ConfirmWorkspaceConfigResponse], error) {
	return handleWorkspaceDecision(ctx, h, req,
		func(msg *workspacev1.ConfirmWorkspaceConfigRequest) (string, string) { return msg.Id, msg.ExpectedHash }, true,
		func(item *workspacev1.Workspace) *workspacev1.ConfirmWorkspaceConfigResponse {
			return &workspacev1.ConfirmWorkspaceConfigResponse{Workspace: item}
		},
	)
}

func (h *workspaceHandler) RejectWorkspaceConfig(ctx context.Context, req *connect.Request[workspacev1.RejectWorkspaceConfigRequest]) (*connect.Response[workspacev1.RejectWorkspaceConfigResponse], error) {
	return handleWorkspaceDecision(ctx, h, req,
		func(msg *workspacev1.RejectWorkspaceConfigRequest) (string, string) { return msg.Id, msg.ExpectedHash }, false,
		func(item *workspacev1.Workspace) *workspacev1.RejectWorkspaceConfigResponse {
			return &workspacev1.RejectWorkspaceConfigResponse{Workspace: item}
		},
	)
}

func handleWorkspaceDecision[Request, Response any](
	ctx context.Context, h *workspaceHandler, req *connect.Request[Request],
	fields func(*Request) (string, string), confirm bool,
	response func(*workspacev1.Workspace) *Response,
) (*connect.Response[Response], error) {
	if req == nil || req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	id, expectedHash := fields(req.Msg)
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	var ws *app.WorkspaceView
	if confirm {
		ws, err = svc.ConfirmWorkspaceConfig(ctx, principal, id, expectedHash)
	} else {
		ws, err = svc.RejectWorkspaceConfig(ctx, principal, id, expectedHash)
	}
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(response(protoWorkspace(ws, raw))), nil
}

func protoWorkspace(ws *app.WorkspaceView, raw []byte) *workspacev1.Workspace {
	out := &workspacev1.Workspace{
		Id: ws.ID, Name: ws.Name, MissingRoots: slices.Clone(ws.MissingRoots), RawToml: slices.Clone(raw),
		ConfigHash: ws.ConfigHash, ConfigRevision: ws.ConfigRevision, PolicyRevision: ws.PolicyRevision,
		PendingPolicyConfirmation: ws.PendingPolicyConfirmation, CreatedAt: ws.CreatedAt,
	}
	for _, root := range ws.Roots {
		out.Roots = append(out.Roots, &workspacev1.Root{Name: root.Name, Path: root.Path, Access: root.Access, Missing: root.Missing})
	}
	for _, diff := range ws.PolicyDiff {
		out.PolicyDiff = append(out.PolicyDiff, &workspacev1.PolicyDiffEntry{Key: diff.Key, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	out.ConfigErrorKey, out.ConfigErrorLine, out.ConfigErrorColumn = ws.ConfigErrorKey, ws.ConfigErrorLine, ws.ConfigErrorColumn
	return out
}

func protoWorkspaceSummary(ws *app.WorkspaceView) *workspacev1.WorkspaceSummary {
	return &workspacev1.WorkspaceSummary{Id: ws.ID, Name: ws.Name, MissingRoots: slices.Clone(ws.MissingRoots), ConfigRevision: ws.ConfigRevision, PendingPolicyConfirmation: ws.PendingPolicyConfirmation}
}

var _ workspacev1connect.WorkspaceServiceHandler = (*workspaceHandler)(nil)
