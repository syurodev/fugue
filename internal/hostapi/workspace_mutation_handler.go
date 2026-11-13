package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
)

func (h *workspaceHandler) AddRoot(ctx context.Context, req *connect.Request[workspacev1.AddRootRequest]) (*connect.Response[workspacev1.AddRootResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := svc.AddWorkspaceRoot(ctx, principal, req.Msg.Id, req.Msg.Name, req.Msg.Path, req.Msg.Access, req.Msg.ExpectedHash)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return workspaceMutationResponse(ctx, h, svc, principal, ws)
}

func (h *workspaceHandler) RemoveRoot(ctx context.Context, req *connect.Request[workspacev1.RemoveRootRequest]) (*connect.Response[workspacev1.RemoveRootResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := svc.RemoveWorkspaceRoot(ctx, principal, req.Msg.Id, req.Msg.Name, req.Msg.ExpectedHash)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.RemoveRootResponse{Workspace: protoWorkspace(ws, raw)}), nil
}

func (h *workspaceHandler) UpdateRoot(ctx context.Context, req *connect.Request[workspacev1.UpdateRootRequest]) (*connect.Response[workspacev1.UpdateRootResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	ws, err := svc.UpdateWorkspaceRoot(ctx, principal, req.Msg.Id, req.Msg.Name, req.Msg.NewName, req.Msg.Path, req.Msg.Access, req.Msg.ExpectedHash)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.UpdateRootResponse{Workspace: protoWorkspace(ws, raw)}), nil
}

func (h *workspaceHandler) DeleteWorkspace(ctx context.Context, req *connect.Request[workspacev1.DeleteWorkspaceRequest]) (*connect.Response[workspacev1.DeleteWorkspaceResponse], error) {
	if req == nil || req.Msg == nil {
		return nil, toConnectError(ctx, h.backend, errcode.New(errcode.InvalidArgument, "request is required"))
	}
	svc, principal, err := h.auth(ctx)
	if err != nil {
		return nil, err
	}
	if err := svc.DeleteWorkspace(ctx, principal, req.Msg.Id, req.Msg.ExpectedHash, req.Msg.ConfirmName); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.DeleteWorkspaceResponse{Id: req.Msg.Id}), nil
}

func workspaceMutationResponse(ctx context.Context, h *workspaceHandler, svc app.WorkspaceBackend, principal string, ws *app.WorkspaceView) (*connect.Response[workspacev1.AddRootResponse], error) {
	_, raw, err := svc.GetWorkspace(ctx, principal, ws.ID)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&workspacev1.AddRootResponse{Workspace: protoWorkspace(ws, raw)}), nil
}
