package hostapi

import (
	"context"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
)

// appstateHandler maps fugue.appstate.v1 onto the app AppStateService. The
// service re-checks the principal itself: only the app client kind may use
// the app state (host-api §5.2), and every check here fails closed.
type appstateHandler struct{ backend app.Backend }

type appStateProvider interface {
	AppStateService() (*app.StateService, error)
}

func (h *appstateHandler) service(ctx context.Context) (string, *app.StateService, error) {
	principal, authErr := localSessionPrincipal(ctx, h.backend, "app state")
	if authErr != nil {
		return "", nil, authErr
	}
	provider, ok := h.backend.(appStateProvider)
	if !ok {
		return "", nil, toConnectError(ctx, h.backend, errcode.New(errcode.Unavailable, "the app state service is unavailable"))
	}
	svc, err := provider.AppStateService()
	if err != nil {
		return "", nil, toConnectError(ctx, h.backend, err)
	}
	return principal, svc, nil
}

func (h *appstateHandler) ListWindows(ctx context.Context, _ *connect.Request[appstatev1.ListWindowsRequest]) (*connect.Response[appstatev1.ListWindowsResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	windows, err := svc.ListWindows(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	out := make([]*appstatev1.SavedWindow, 0, len(windows))
	for i := range windows {
		out = append(out, protoSavedWindow(&windows[i]))
	}
	return connect.NewResponse(&appstatev1.ListWindowsResponse{Windows: out}), nil
}

func (h *appstateHandler) PutWindow(ctx context.Context, req *connect.Request[appstatev1.PutWindowRequest]) (*connect.Response[appstatev1.PutWindowResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	revision, err := svc.PutWindow(ctx, &app.PutWindowCommand{
		Principal:        principal,
		CommandID:        req.Msg.GetCommandId(),
		WindowID:         req.Msg.GetWindowId(),
		ExpectedRevision: req.Msg.GetExpectedRevision(),
		Layout:           req.Msg.GetLayout(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&appstatev1.PutWindowResponse{Revision: revision}), nil
}

func (h *appstateHandler) DeleteWindow(ctx context.Context, req *connect.Request[appstatev1.DeleteWindowRequest]) (*connect.Response[appstatev1.DeleteWindowResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	if err := svc.DeleteWindow(ctx, &app.DeleteWindowCommand{
		Principal:        principal,
		CommandID:        req.Msg.GetCommandId(),
		WindowID:         req.Msg.GetWindowId(),
		ExpectedRevision: req.Msg.GetExpectedRevision(),
	}); err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&appstatev1.DeleteWindowResponse{}), nil
}

func (h *appstateHandler) GetPreferences(ctx context.Context, _ *connect.Request[appstatev1.GetPreferencesRequest]) (*connect.Response[appstatev1.GetPreferencesResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	prefs, err := svc.GetPreferences(ctx, principal)
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&appstatev1.GetPreferencesResponse{
		Revision:    prefs.Revision,
		Preferences: prefs.Document,
	}), nil
}

func (h *appstateHandler) PutPreferences(ctx context.Context, req *connect.Request[appstatev1.PutPreferencesRequest]) (*connect.Response[appstatev1.PutPreferencesResponse], error) {
	principal, svc, authErr := h.service(ctx)
	if authErr != nil {
		return nil, authErr
	}
	revision, err := svc.PutPreferences(ctx, &app.PutPreferencesCommand{
		Principal:        principal,
		CommandID:        req.Msg.GetCommandId(),
		ExpectedRevision: req.Msg.GetExpectedRevision(),
		Preferences:      req.Msg.GetPreferences(),
	})
	if err != nil {
		return nil, toConnectError(ctx, h.backend, err)
	}
	return connect.NewResponse(&appstatev1.PutPreferencesResponse{Revision: revision}), nil
}

func protoSavedWindow(w *app.StateWindowView) *appstatev1.SavedWindow {
	return &appstatev1.SavedWindow{
		WindowId:  w.WindowID,
		Revision:  w.Revision,
		Layout:    w.Layout,
		UpdatedAt: timestamppb.New(w.UpdatedAt),
	}
}

var _ appstatev1connect.AppStateServiceHandler = (*appstateHandler)(nil)
