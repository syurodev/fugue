package client_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"
	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

// appStateFake is a scripted app-state host: in-memory CAS with the typed
// conflict detail, so the client's revision_conflict handling is exercised
// against the real wire shapes.
type appStateFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	appstatev1connect.UnimplementedAppStateServiceHandler

	mu       sync.Mutex
	windows  map[string]*appstatev1.SavedWindow
	prefsRev uint64
	prefsDoc []byte
}

func newAppStateFake() *appStateFake {
	return &appStateFake{windows: map[string]*appstatev1.SavedWindow{}}
}

func (f *appStateFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (f *appStateFake) conflict(current uint64) error {
	cerr := connect.NewError(connect.CodeFailedPrecondition, errClientInterScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: "appstate.revision_conflict", CurrentRevision: current})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(detail)
	return cerr
}

func (f *appStateFake) ListWindows(_ context.Context, _ *connect.Request[appstatev1.ListWindowsRequest]) (*connect.Response[appstatev1.ListWindowsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*appstatev1.SavedWindow, 0, len(f.windows))
	for _, w := range f.windows {
		out = append(out, w)
	}
	return connect.NewResponse(&appstatev1.ListWindowsResponse{Windows: out}), nil
}

func (f *appStateFake) PutWindow(_ context.Context, req *connect.Request[appstatev1.PutWindowRequest]) (*connect.Response[appstatev1.PutWindowResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	msg := req.Msg
	if cur, ok := f.windows[msg.GetWindowId()]; ok {
		if cur.GetRevision() != msg.GetExpectedRevision() || msg.GetExpectedRevision() == 0 {
			return nil, f.conflict(cur.GetRevision())
		}
		cur.Revision++
		cur.Layout = msg.GetLayout()
		return connect.NewResponse(&appstatev1.PutWindowResponse{Revision: cur.GetRevision()}), nil
	}
	if msg.GetExpectedRevision() != 0 {
		return nil, f.conflict(0)
	}
	f.windows[msg.GetWindowId()] = &appstatev1.SavedWindow{WindowId: msg.GetWindowId(), Revision: 1, Layout: msg.GetLayout()}
	return connect.NewResponse(&appstatev1.PutWindowResponse{Revision: 1}), nil
}

func (f *appStateFake) DeleteWindow(_ context.Context, req *connect.Request[appstatev1.DeleteWindowRequest]) (*connect.Response[appstatev1.DeleteWindowResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cur, ok := f.windows[req.Msg.GetWindowId()]
	if !ok || cur.GetRevision() != req.Msg.GetExpectedRevision() {
		var gone uint64
		if ok {
			gone = cur.GetRevision()
		}
		return nil, f.conflict(gone)
	}
	delete(f.windows, req.Msg.GetWindowId())
	return connect.NewResponse(&appstatev1.DeleteWindowResponse{}), nil
}

func (f *appStateFake) GetPreferences(_ context.Context, _ *connect.Request[appstatev1.GetPreferencesRequest]) (*connect.Response[appstatev1.GetPreferencesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return connect.NewResponse(&appstatev1.GetPreferencesResponse{Revision: f.prefsRev, Preferences: f.prefsDoc}), nil
}

func (f *appStateFake) PutPreferences(_ context.Context, req *connect.Request[appstatev1.PutPreferencesRequest]) (*connect.Response[appstatev1.PutPreferencesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.prefsRev == 0 {
		if req.Msg.GetExpectedRevision() != 0 {
			return nil, f.conflict(0)
		}
	} else if f.prefsRev != req.Msg.GetExpectedRevision() {
		return nil, f.conflict(f.prefsRev)
	}
	f.prefsRev++
	f.prefsDoc = req.Msg.GetPreferences()
	return connect.NewResponse(&appstatev1.PutPreferencesResponse{Revision: f.prefsRev}), nil
}

// serveAppStateFake serves the fake over a real unix socket and dials it as
// the app client.
func serveAppStateFake(t *testing.T, fake *appStateFake) *client.Client {
	t.Helper()
	socket := filepath.Join(shortDir(t), "appstate.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(appstatev1connect.NewAppStateServiceHandler(fake))
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)

	c, err := client.Dial(t.Context(), socket, appParams())
	if err != nil {
		t.Fatalf("dial the scripted app-state host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestAppStateClientMethodsOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := serveAppStateFake(t, newAppStateFake())

	// An empty store lists nothing.
	windows, err := c.ListWindows(ctx)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 0 {
		t.Fatalf("ListWindows on an empty store = %+v", windows)
	}

	layout := []byte(`{"schema_version":1,"slots":["main"],"pad":"  keep verbatim  "}`)
	win := "0d20ab17-0000-4000-8000-000000000042"
	rev, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: "cmd-client-1", WindowID: win, Layout: layout,
	})
	if err != nil || rev != 1 {
		t.Fatalf("PutAppStateWindow create = (%d, %v), want (1, nil)", rev, err)
	}

	// A stale revision fails typed with the current revision attached.
	_, err = c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: "cmd-client-2", WindowID: win, ExpectedRevision: 9, Layout: layout,
	})
	code, ok := client.AppStateCodeOf(err)
	if !ok || code != "appstate.revision_conflict" {
		t.Fatalf("AppStateCodeOf = (%q, %v), want appstate.revision_conflict", code, ok)
	}
	if current, ok := client.AppStateCurrentRevision(err); !ok || current != 1 {
		t.Fatalf("AppStateCurrentRevision = (%d, %v), want (1, true)", current, ok)
	}

	rev, err = c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: "cmd-client-3", WindowID: win, ExpectedRevision: 1, Layout: layout,
	})
	if err != nil || rev != 2 {
		t.Fatalf("PutAppStateWindow update = (%d, %v), want (2, nil)", rev, err)
	}
	windows, err = c.ListWindows(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("ListWindows = (%+v, %v)", windows, err)
	}
	if windows[0].WindowID != win || windows[0].Revision != 2 || string(windows[0].Layout) != string(layout) {
		t.Fatalf("the layout did not round-trip verbatim: %+v", windows[0])
	}

	if err := c.DeleteAppStateWindow(ctx, "cmd-client-4", win, 2); err != nil {
		t.Fatalf("DeleteAppStateWindow: %v", err)
	}
	err = c.DeleteAppStateWindow(ctx, "cmd-client-5", win, 2)
	if code, ok := client.AppStateCodeOf(err); !ok || code != "appstate.revision_conflict" {
		t.Fatalf("DeleteAppStateWindow twice = %v, want revision_conflict", err)
	}

	prefsRaw := []byte(`{"schema_version":1,"theme":"dark"}`)
	prefs, err := c.GetAppStatePreferences(ctx)
	if err != nil || prefs.Revision != 0 {
		t.Fatalf("GetAppStatePreferences before any save = (%+v, %v)", prefs, err)
	}
	rev, err = c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{CommandID: "cmd-client-6", Preferences: prefsRaw})
	if err != nil || rev != 1 {
		t.Fatalf("PutAppStatePreferences create = (%d, %v), want (1, nil)", rev, err)
	}
	_, err = c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{CommandID: "cmd-client-7", ExpectedRevision: 5, Preferences: prefsRaw})
	if code, ok := client.AppStateCodeOf(err); !ok || code != "appstate.revision_conflict" {
		t.Fatalf("PutAppStatePreferences stale = %v, want revision_conflict", err)
	}
	prefs, err = c.GetAppStatePreferences(ctx)
	if err != nil || prefs.Revision != 1 || string(prefs.Preferences) != string(prefsRaw) {
		t.Fatalf("GetAppStatePreferences = (%+v, %v), want revision 1 with the saved document", prefs, err)
	}
}

func TestAppStateTopicDecoders(t *testing.T) {
	t.Parallel()
	snap, err := client.DecodeAppStateSnapshot([]byte(
		`{"windows":[{"window_id":"0d20ab17-0000-4000-8000-000000000001","revision":2,"layout":{"schema_version":1},"updated_at":"2026-11-21T00:00:00Z"}],` +
			`"preferences":{"revision":1,"preferences":{"theme":"dark"}}}`))
	if err != nil {
		t.Fatalf("DecodeAppStateSnapshot: %v", err)
	}
	if snap.Sequence != 0 || len(snap.Windows) != 1 || snap.Preferences == nil {
		t.Fatalf("snapshot = %+v", snap)
	}
	if snap.Windows[0].WindowID != "0d20ab17-0000-4000-8000-000000000001" || snap.Windows[0].Revision != 2 ||
		string(snap.Windows[0].Layout) != `{"schema_version":1}` {
		t.Fatalf("snapshot window = %+v", snap.Windows[0])
	}
	if snap.Preferences.Revision != 1 || string(snap.Preferences.Preferences) != `{"theme":"dark"}` {
		t.Fatalf("snapshot preferences = %+v", snap.Preferences)
	}

	put, err := client.DecodeAppStateEvent(client.EventAppStateWindowPut, []byte(`{"window_id":"0d20ab17-0000-4000-8000-000000000001","revision":2}`))
	if err != nil || put.WindowID != "0d20ab17-0000-4000-8000-000000000001" || put.Revision != 2 {
		t.Fatalf("DecodeAppStateEvent window_put = (%+v, %v)", put, err)
	}
	deleted, err := client.DecodeAppStateEvent(client.EventAppStateWindowDeleted, []byte(`{"window_id":"w","revision":3}`))
	if err != nil || deleted.Type != client.EventAppStateWindowDeleted || deleted.Revision != 3 {
		t.Fatalf("DecodeAppStateEvent window_deleted = (%+v, %v)", deleted, err)
	}
	prefsEvent, err := client.DecodeAppStateEvent(client.EventAppStatePreferencesPut, []byte(`{"revision":4}`))
	if err != nil || prefsEvent.WindowID != "" || prefsEvent.Revision != 4 {
		t.Fatalf("DecodeAppStateEvent preferences_put = (%+v, %v)", prefsEvent, err)
	}
	if _, err := client.DecodeAppStateEvent("unknown", nil); err == nil {
		t.Fatal("an unknown event type decoded")
	}
}
