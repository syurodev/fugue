package hostapi

import (
	"context"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	sessiontoolsv1 "github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/sessiontools/v1/sessiontoolsv1connect"
)

// sessionToolHandler maps fugue.sessiontools.v1 onto the app session tool
// service. It is the whole surface of the session principal (host-api §7):
// the binding comes from the connection's attach, never from a request
// field, and the token is read once and kept nowhere.
type sessionToolHandler struct{ backend app.Backend }

type sessionToolProvider interface {
	SessionTools() (*app.SessionToolService, error)
}

func (h *sessionToolHandler) service() (*app.SessionToolService, error) {
	provider, ok := h.backend.(sessionToolProvider)
	if !ok {
		return nil, errcode.New(errcode.Unavailable, "the session tool service is unavailable")
	}
	svc, err := provider.SessionTools()
	if err != nil {
		return nil, errcode.Wrap(errcode.Unavailable, "the session tool service is unavailable", err)
	}
	return svc, nil
}

// attached returns the service and the binding of a session-principal
// connection; every other connection is denied.
func (h *sessionToolHandler) attached(ctx context.Context) (*app.SessionToolService, app.SessionBinding, error) {
	st, err := connectionState(ctx)
	if err != nil {
		return nil, app.SessionBinding{}, err
	}
	binding, ok := st.sessionBinding()
	if !ok {
		return nil, app.SessionBinding{}, ErrSessionAttachFirst
	}
	svc, err := h.service()
	return svc, binding, err
}

func (h *sessionToolHandler) Attach(ctx context.Context, req *connect.Request[sessiontoolsv1.AttachRequest]) (*connect.Response[sessiontoolsv1.AttachResponse], error) {
	st, err := connectionState(ctx)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	svc, err := h.service()
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	binding, err := svc.Attach(req.Msg.SessionToken)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	if err := st.attachSession(binding); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&sessiontoolsv1.AttachResponse{SessionId: binding.SessionID, Role: binding.Role}), nil
}

func (h *sessionToolHandler) ListTools(ctx context.Context, _ *connect.Request[sessiontoolsv1.ListToolsRequest]) (*connect.Response[sessiontoolsv1.ListToolsResponse], error) {
	svc, binding, err := h.attached(ctx)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	tools, err := svc.List(binding)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := &sessiontoolsv1.ListToolsResponse{}
	for i := range tools {
		out.Tools = append(out.Tools, &sessiontoolsv1.Tool{
			Name: tools[i].Name, Description: tools[i].Description, InputSchema: tools[i].InputSchema,
		})
	}
	return connect.NewResponse(out), nil
}

func (h *sessionToolHandler) CallTool(ctx context.Context, req *connect.Request[sessiontoolsv1.CallToolRequest]) (*connect.Response[sessiontoolsv1.CallToolResponse], error) {
	svc, binding, err := h.attached(ctx)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	res, err := svc.Call(ctx, binding, req.Msg.Name, req.Msg.Arguments)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&sessiontoolsv1.CallToolResponse{Text: res.Text, IsError: res.IsError}), nil
}

var _ sessiontoolsv1connect.SessionToolServiceHandler = (*sessionToolHandler)(nil)
