package hostapi

import (
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/platform/errcode"
	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
)

// The app-state RPCs over the real server: a connection that has not said
// hello, and the session principal, are both denied typed — the service is
// the app's private store (host-api §5.2).
func TestAppStateRPCsDenyUnhelloedAndSessionPrincipals(t *testing.T) {
	t.Parallel()
	v := startTokenHost(t, "")
	_, httpClient := newTestClient(t, v.sockPath)
	appStateClient := appstatev1connect.NewAppStateServiceClient(httpClient, testBaseURL)

	assertAllAppStateRPCsDenied(t, appStateClient)

	wsID := v.workspaceWithRoot(t, t.TempDir())
	_ = v.readySession(t, "appstate-deny-1", wsID, "")
	tokens := sessionTokens(t, 1)
	_, sessHTTP, err := attachSession(t, v.sockPath, tokens[0])
	if err != nil {
		t.Fatal(err)
	}
	assertAllAppStateRPCsDenied(t, appstatev1connect.NewAppStateServiceClient(sessHTTP, testBaseURL))
}

func assertAllAppStateRPCsDenied(t *testing.T, c appstatev1connect.AppStateServiceClient) {
	t.Helper()
	if _, err := c.ListWindows(t.Context(), connect.NewRequest(&appstatev1.ListWindowsRequest{})); err == nil {
		t.Fatal("ListWindows allowed an unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.PutWindow(t.Context(), connect.NewRequest(&appstatev1.PutWindowRequest{
		CommandId: "cmd-appstate-deny-1", WindowId: "0d20ab17-0000-4000-8000-000000000001",
		Layout: []byte(`{"schema_version":1}`),
	})); err == nil {
		t.Fatal("PutWindow allowed an unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.DeleteWindow(t.Context(), connect.NewRequest(&appstatev1.DeleteWindowRequest{
		CommandId: "cmd-appstate-deny-2", WindowId: "0d20ab17-0000-4000-8000-000000000001", ExpectedRevision: 1,
	})); err == nil {
		t.Fatal("DeleteWindow allowed an unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.GetPreferences(t.Context(), connect.NewRequest(&appstatev1.GetPreferencesRequest{})); err == nil {
		t.Fatal("GetPreferences allowed an unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
	if _, err := c.PutPreferences(t.Context(), connect.NewRequest(&appstatev1.PutPreferencesRequest{
		CommandId: "cmd-appstate-deny-3", Preferences: []byte(`{"schema_version":1}`),
	})); err == nil {
		t.Fatal("PutPreferences allowed an unauthorized principal")
	} else {
		assertConnectCode(t, err, connect.CodePermissionDenied, errcode.PermissionDenied)
	}
}
