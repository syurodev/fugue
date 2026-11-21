package client_test

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/pkg/client"
	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
)

const AppStateConflictCode = "appstate.revision_conflict"

// errAppStateScripted is the static cause behind a scripted fake failure; it
// is wrapped, never constructed, at the call site.
var errAppStateScripted = errors.New("scripted app-state error")

// appStateFake is a scripted app-state host: in-memory CAS with the typed
// conflict detail, so the client's revision_conflict handling is exercised
// against the real wire shapes. failRPC names the methods that answer with a
// coded failure; scriptedWindows replaces the store listing verbatim.
type appStateFake struct {
	hostv1connect.UnimplementedHostServiceHandler
	appstatev1connect.UnimplementedAppStateServiceHandler

	mu              sync.Mutex
	windows         map[string]*appstatev1.SavedWindow
	prefsRev        uint64
	prefsDoc        []byte
	failRPC         map[string]bool
	scriptedWindows []*appstatev1.SavedWindow
}

func newAppStateFake() *appStateFake {
	return &appStateFake{windows: map[string]*appstatev1.SavedWindow{}}
}

func (f *appStateFake) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

// failErr is the scripted failure: a coded Connect error with the typed
// detail, so the client's code extraction sees the real wire shape.
func (f *appStateFake) failErr() error {
	cerr := connect.NewError(connect.CodeInternal, errAppStateScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: "internal"})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(detail)
	return cerr
}

func (f *appStateFake) conflict(current uint64) error {
	cerr := connect.NewError(connect.CodeFailedPrecondition, errClientInterScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: AppStateConflictCode, CurrentRevision: current})
	if derr != nil {
		return cerr
	}
	cerr.AddDetail(detail)
	return cerr
}

func (f *appStateFake) ListWindows(context.Context, *connect.Request[appstatev1.ListWindowsRequest]) (*connect.Response[appstatev1.ListWindowsResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRPC["ListWindows"] {
		return nil, f.failErr()
	}
	if f.scriptedWindows != nil {
		return connect.NewResponse(&appstatev1.ListWindowsResponse{Windows: f.scriptedWindows}), nil
	}
	out := make([]*appstatev1.SavedWindow, 0, len(f.windows))
	for _, w := range f.windows {
		out = append(out, w)
	}
	return connect.NewResponse(&appstatev1.ListWindowsResponse{Windows: out}), nil
}

func (f *appStateFake) PutWindow(_ context.Context, req *connect.Request[appstatev1.PutWindowRequest]) (*connect.Response[appstatev1.PutWindowResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRPC["PutWindow"] {
		return nil, f.failErr()
	}
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
	if f.failRPC["DeleteWindow"] {
		return nil, f.failErr()
	}
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

func (f *appStateFake) GetPreferences(context.Context, *connect.Request[appstatev1.GetPreferencesRequest]) (*connect.Response[appstatev1.GetPreferencesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRPC["GetPreferences"] {
		return nil, f.failErr()
	}
	return connect.NewResponse(&appstatev1.GetPreferencesResponse{Revision: f.prefsRev, Preferences: f.prefsDoc}), nil
}

func (f *appStateFake) PutPreferences(_ context.Context, req *connect.Request[appstatev1.PutPreferencesRequest]) (*connect.Response[appstatev1.PutPreferencesResponse], error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failRPC["PutPreferences"] {
		return nil, f.failErr()
	}
	if f.prefsRev != req.Msg.GetExpectedRevision() {
		return nil, f.conflict(f.prefsRev)
	}
	f.prefsRev++
	f.prefsDoc = req.Msg.GetPreferences()
	return connect.NewResponse(&appstatev1.PutPreferencesResponse{Revision: f.prefsRev}), nil
}

// serveFakeOnUnixSocket serves mux over a real unix socket and dials it as
// the app client (host-api §7).
func serveFakeOnUnixSocket(t *testing.T, mux *http.ServeMux) *client.Client {
	t.Helper()
	socket := filepath.Join(shortDir(t), "fake.sock")
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", socket)
	if err != nil {
		t.Fatal(err)
	}
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
		t.Fatalf("dial the scripted host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func serveAppStateFake(t *testing.T, fake *appStateFake) *client.Client {
	t.Helper()
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(fake))
	mux.Handle(appstatev1connect.NewAppStateServiceHandler(fake))
	return serveFakeOnUnixSocket(t, mux)
}

const testAppStateWindow = "0d20ab17-0000-4000-8000-000000000042"

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
	assertWindowPut(t, ctx, c, "cmd-client-1", testAppStateWindow, 0, layout, 1)
	assertWindowPut(t, ctx, c, "cmd-client-3", testAppStateWindow, 1, layout, 2)
	assertListedWindow(t, ctx, c, layout)

	if delErr := c.DeleteAppStateWindow(ctx, "cmd-client-4", testAppStateWindow, 2); delErr != nil {
		t.Fatalf("DeleteAppStateWindow: %v", delErr)
	}
	assertAppStateDeleted(t, ctx, c)
}

// assertWindowPut submits one window layout and expects the given revision.
func assertWindowPut(t *testing.T, ctx context.Context, c *client.Client, commandID, windowID string, expected uint64, layout []byte, wantRev uint64) {
	t.Helper()
	rev, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: commandID, WindowID: windowID, ExpectedRevision: expected, Layout: layout,
	})
	if err != nil || rev != wantRev {
		t.Fatalf("PutAppStateWindow %s = (%d, %v), want (%d, nil)", commandID, rev, err, wantRev)
	}
}

// assertListedWindow asserts the one stored window round-trips verbatim.
func assertListedWindow(t *testing.T, ctx context.Context, c *client.Client, layout []byte) {
	t.Helper()
	windows, err := c.ListWindows(ctx)
	if err != nil || len(windows) != 1 {
		t.Fatalf("ListWindows = (%+v, %v)", windows, err)
	}
	if windows[0].WindowID != testAppStateWindow || windows[0].Revision != 2 || string(windows[0].Layout) != string(layout) {
		t.Fatalf("the layout did not round-trip verbatim: %+v", windows[0])
	}
}

// TestAppStatePreferencesOverRealSocket drives the shared preferences
// document over the socket.
func TestAppStatePreferencesOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := serveAppStateFake(t, newAppStateFake())

	prefsRaw := []byte(`{"schema_version":1,"theme":"dark"}`)
	prefs, err := c.GetAppStatePreferences(ctx)
	if err != nil || prefs.Revision != 0 {
		t.Fatalf("GetAppStatePreferences before any save = (%+v, %v)", prefs, err)
	}
	rev, err := c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{CommandID: "cmd-client-6", Preferences: prefsRaw})
	if err != nil || rev != 1 {
		t.Fatalf("PutAppStatePreferences create = (%d, %v), want (1, nil)", rev, err)
	}
	prefs, err = c.GetAppStatePreferences(ctx)
	if err != nil || prefs.Revision != 1 || string(prefs.Preferences) != string(prefsRaw) {
		t.Fatalf("GetAppStatePreferences = (%+v, %v), want revision 1 with the saved document", prefs, err)
	}
}

// assertAppStateDeleted asserts the second delete of the same window fails
// typed with the current (absent) revision.
func assertAppStateDeleted(t *testing.T, ctx context.Context, c *client.Client) {
	t.Helper()
	err := c.DeleteAppStateWindow(ctx, "cmd-client-5", testAppStateWindow, 2)
	code, ok := client.AppStateCodeOf(err)
	if !ok || code != AppStateConflictCode {
		t.Fatalf("DeleteAppStateWindow twice = %v, want revision_conflict", err)
	}
	if current, ok := client.AppStateCurrentRevision(err); !ok || current != 0 {
		t.Fatalf("the second delete names current revision (%d, %v), want (0, true)", current, ok)
	}
}

// TestAppStateClientReportsTheCurrentRevision pins the typed conflict
// detail: the code plus the revision the stored document carries now, so the
// caller can retry its CAS against it.
func TestAppStateClientReportsTheCurrentRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	c := serveAppStateFake(t, newAppStateFake())

	layout := []byte(`{"schema_version":1}`)
	if _, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: "cmd-conflict-1", WindowID: testAppStateWindow, Layout: layout,
	}); err != nil {
		t.Fatalf("create the window: %v", err)
	}
	_, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{
		CommandID: "cmd-conflict-2", WindowID: testAppStateWindow, ExpectedRevision: 9, Layout: layout,
	})
	code, ok := client.AppStateCodeOf(err)
	if !ok || code != AppStateConflictCode {
		t.Fatalf("AppStateCodeOf = (%q, %v), want %s", code, ok, AppStateConflictCode)
	}
	if current, ok := client.AppStateCurrentRevision(err); !ok || current != 1 {
		t.Fatalf("AppStateCurrentRevision = (%d, %v), want (1, true)", current, ok)
	}

	_, err = c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{CommandID: "cmd-conflict-3", ExpectedRevision: 5, Preferences: layout})
	if code, ok := client.AppStateCodeOf(err); !ok || code != AppStateConflictCode {
		t.Fatalf("PutAppStatePreferences stale = %v, want revision_conflict", err)
	}
}

func TestAppStateSnapshotDecoder(t *testing.T) {
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
}

func TestAppStateEventDecoder(t *testing.T) {
	t.Parallel()
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

// TestAppStateClientErrorPathsOverRealSocket drives every app-state method
// against a host that fails them, so the client's wrapping and the typed
// detail extraction are exercised on the failures too.
func TestAppStateClientErrorPathsOverRealSocket(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	fake := newAppStateFake()
	fake.mu.Lock()
	fake.failRPC = map[string]bool{
		"ListWindows": true, "PutWindow": true, "DeleteWindow": true,
		"GetPreferences": true, "PutPreferences": true,
	}
	fake.mu.Unlock()
	c := serveAppStateFake(t, fake)

	if _, err := c.ListWindows(ctx); !appStateScripted(err, "list the saved windows") {
		t.Fatalf("ListWindows = %v, want a wrapped scripted failure", err)
	}
	if _, err := c.PutAppStateWindow(ctx, &client.PutAppStateWindowInput{CommandID: "cmd-fail-1", WindowID: testAppStateWindow}); !appStateScripted(err, "put the window layout") {
		t.Fatalf("PutAppStateWindow = %v, want a wrapped scripted failure", err)
	}
	if err := c.DeleteAppStateWindow(ctx, "cmd-fail-2", testAppStateWindow, 1); !appStateScripted(err, "delete the saved window") {
		t.Fatalf("DeleteAppStateWindow = %v, want a wrapped scripted failure", err)
	}
	if _, err := c.GetAppStatePreferences(ctx); !appStateScripted(err, "read the app preferences") {
		t.Fatalf("GetAppStatePreferences = %v, want a wrapped scripted failure", err)
	}
	if _, err := c.PutAppStatePreferences(ctx, &client.PutAppStatePreferencesInput{CommandID: "cmd-fail-3"}); !appStateScripted(err, "put the app preferences") {
		t.Fatalf("PutAppStatePreferences = %v, want a wrapped scripted failure", err)
	}
	if code, ok := client.AppStateCodeOf(errors.Join(errAppStateScripted)); ok {
		t.Fatalf("AppStateCodeOf on a plain error = (%q, %v), want not ok", code, ok)
	}
}

// appStateScripted reports whether err wraps the scripted Connect failure
// with the client's own context.
func appStateScripted(err error, what string) bool {
	code, ok := client.AppStateCodeOf(err)
	return err != nil && ok && code == "internal" && strings.Contains(err.Error(), what)
}

// TestAppStateCodeOfRejectsPlainErrors pins the code extraction on errors
// that carry no detail or a detail of another code.
func TestAppStateCodeOfRejectsPlainErrors(t *testing.T) {
	t.Parallel()
	if _, ok := client.AppStateCodeOf(errAppStateScripted); ok {
		t.Fatal("AppStateCodeOf decoded a plain error")
	}
	if _, ok := client.AppStateCurrentRevision(errAppStateScripted); ok {
		t.Fatal("AppStateCurrentRevision decoded a plain error")
	}
	cerr := connect.NewError(connect.CodeResourceExhausted, errAppStateScripted)
	detail, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: "appstate.too_large"})
	if derr != nil {
		t.Fatal(derr)
	}
	cerr.AddDetail(detail)
	if code, ok := client.AppStateCodeOf(cerr); !ok || code != "appstate.too_large" {
		t.Fatalf("AppStateCodeOf = (%q, %v), want appstate.too_large", code, ok)
	}
	if _, ok := client.AppStateCurrentRevision(cerr); ok {
		t.Fatal("AppStateCurrentRevision decoded a conflict revision from another code")
	}
}

// TestAppStateDecodersRejectMalformedPayloads pins the decoders' failure
// paths: a payload that is not the topic's JSON shape never decodes.
func TestAppStateDecodersRejectMalformedPayloads(t *testing.T) {
	t.Parallel()
	if _, err := client.DecodeAppStateSnapshot([]byte(`{"windows":`)); err == nil {
		t.Fatal("a truncated snapshot decoded")
	}
	if _, err := client.DecodeAppStateSnapshot([]byte(`[]`)); err == nil {
		t.Fatal("an array snapshot decoded")
	}
	if _, err := client.DecodeAppStateSnapshot([]byte(`null`)); err == nil {
		t.Fatal("a null snapshot decoded")
	}
	if _, err := client.DecodeAppStateEvent(client.EventAppStateWindowPut, []byte(`{`)); err == nil {
		t.Fatal("a truncated window event decoded")
	}
	if _, err := client.DecodeAppStateEvent(client.EventAppStatePreferencesPut, []byte(`5`)); err == nil {
		t.Fatal("a number preferences event decoded")
	}
	if _, err := client.DecodeAppStateEvent(client.EventAppStateWindowDeleted, []byte(`null`)); err == nil {
		t.Fatal("a null window event decoded")
	}
	if _, err := client.DecodeAppStateEvent(client.EventAppStatePreferencesPut, []byte(`null`)); err == nil {
		t.Fatal("a null preferences event decoded")
	}
}

// TestAppStateListMapsNilProtoFields pins the nil handling of the wire
// mapping: a nil window element maps to the zero view and a missing
// timestamp maps to the zero time, never a panic.
func TestAppStateListMapsNilProtoFields(t *testing.T) {
	t.Parallel()
	fake := newAppStateFake()
	stamp := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	stamped := timestamppb.New(stamp)
	fake.mu.Lock()
	fake.scriptedWindows = []*appstatev1.SavedWindow{nil, {
		WindowId: testAppStateWindow, Revision: 2, Layout: []byte(`{"schema_version":1}`),
	}, {
		WindowId: testAppStateWindow, Revision: 3, Layout: []byte(`{"schema_version":1}`), UpdatedAt: stamped,
	}}
	fake.mu.Unlock()
	c := serveAppStateFake(t, fake)

	windows, err := c.ListWindows(t.Context())
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("ListWindows = %d windows, want 3", len(windows))
	}
	if windows[0].WindowID != "" || windows[0].Revision != 0 || windows[0].Layout != nil || !windows[0].UpdatedAt.IsZero() {
		t.Fatalf("a nil wire window mapped to %+v, want the zero view", windows[0])
	}
	if windows[1].WindowID != testAppStateWindow || windows[1].Revision != 2 || !windows[1].UpdatedAt.IsZero() {
		t.Fatalf("a window without a timestamp mapped to %+v", windows[1])
	}
	if windows[2].Revision != 3 || !windows[2].UpdatedAt.Equal(stamp) {
		t.Fatalf("a window with a timestamp mapped to %+v, want %v", windows[2], stamp)
	}
}
