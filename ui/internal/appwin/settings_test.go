package appwin

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io/fs"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"
	appstatev1 "github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/appstate/v1/appstatev1connect"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/host/v1/hostv1connect"
	vaultv1 "github.com/syurodev/fugue/pkg/pb/fugue/vault/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vault/v1/vaultv1connect"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

var errLockedAppStateScripted = errors.New("scripted appstate conflict")

type lockedVaultAppStateServer struct {
	hostv1connect.UnimplementedHostServiceHandler
	vaultv1connect.UnimplementedVaultServiceHandler
	appstatev1connect.UnimplementedAppStateServiceHandler

	mu       sync.Mutex
	prefsRev uint64
	prefsDoc []byte
}

func (s *lockedVaultAppStateServer) Hello(context.Context, *connect.Request[hostv1.HelloRequest]) (*connect.Response[hostv1.HelloResponse], error) {
	return connect.NewResponse(&hostv1.HelloResponse{HostVersion: "v1", ApiVersion: "v1"}), nil
}

func (s *lockedVaultAppStateServer) GetStatus(context.Context, *connect.Request[vaultv1.GetStatusRequest]) (*connect.Response[vaultv1.GetStatusResponse], error) {
	return connect.NewResponse(&vaultv1.GetStatusResponse{
		Status: &vaultv1.VaultStatus{State: client.VaultStateLocked, Revision: 1, Incarnation: "inc-locked"},
	}), nil
}

func (s *lockedVaultAppStateServer) GetPreferences(context.Context, *connect.Request[appstatev1.GetPreferencesRequest]) (*connect.Response[appstatev1.GetPreferencesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return connect.NewResponse(&appstatev1.GetPreferencesResponse{Revision: s.prefsRev, Preferences: s.prefsDoc}), nil
}

func (s *lockedVaultAppStateServer) PutPreferences(_ context.Context, req *connect.Request[appstatev1.PutPreferencesRequest]) (*connect.Response[appstatev1.PutPreferencesResponse], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.Msg.GetExpectedRevision() != s.prefsRev {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errLockedAppStateScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.AppStateConflictCode, CurrentRevision: s.prefsRev}); err == nil {
			cerr.AddDetail(detail)
		}
		return nil, cerr
	}
	s.prefsRev++
	s.prefsDoc = append([]byte(nil), req.Msg.GetPreferences()...)
	return connect.NewResponse(&appstatev1.PutPreferencesResponse{Revision: s.prefsRev}), nil
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	var dir string
	for idx := 0; ; idx++ {
		dir = filepath.Join(os.TempDir(), fmt.Sprintf("fg-ui-%x-%d-%d", sum[:4], os.Getpid(), idx))
		err := os.Mkdir(dir, 0o700)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) || idx == 100 {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if rmErr := os.RemoveAll(dir); rmErr != nil {
			t.Logf("cleanup %s: %v", dir, rmErr)
		}
	})
	return filepath.Join(dir, "host.sock")
}

func serveAppStateSocket(t *testing.T, srvImpl *lockedVaultAppStateServer) string {
	t.Helper()
	sock := shortSocketPath(t)
	var lc net.ListenConfig
	ln, err := lc.Listen(t.Context(), "unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle(hostv1connect.NewHostServiceHandler(srvImpl))
	mux.Handle(vaultv1connect.NewVaultServiceHandler(srvImpl))
	mux.Handle(appstatev1connect.NewAppStateServiceHandler(srvImpl))
	srv := httptest.NewUnstartedServer(mux)
	srv.Listener = ln
	var protos http.Protocols
	protos.SetHTTP2(true)
	protos.SetUnencryptedHTTP2(true)
	srv.Config.Protocols = &protos
	srv.Start()
	t.Cleanup(srv.Close)
	return sock
}

func dialAppStateTestScreens(t *testing.T, sock string) (*screens, *client.Client, *kit.Env, *theme.Theme, *theme.Theme) {
	t.Helper()
	c, err := client.Dial(t.Context(), sock, client.Params{Kind: client.KindApp, Name: "fugue-app", Version: "1.0.0", APIVersions: []string{"v1"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateLocked, Revision: 1, Incarnation: "inc-locked"})
	w := settings.NewWorker(func() settings.Client { return c }, nil)
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.settingsWorker = w

	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	light, dark := theme.Light(sh), theme.Dark(sh)
	env := &kit.Env{Th: &light, Sh: sh, ReducedMotion: true}
	return sc, c, env, &light, &dark
}

func awaitSettingsResult(t *testing.T, w *settings.Worker) settings.Result {
	t.Helper()
	select {
	case res := <-w.Results():
		return res
	case <-t.Context().Done():
		t.Fatalf("timed out waiting for settings worker result: %v", t.Context().Err())
		return settings.Result{}
	}
}

// TestSettingsPersistsThemeAndScaleLocally verifies that theme mode and UI
// scale apply live, persist via fugue.appstate.v1 PutPreferences while the
// vault is locked, restore on restart, and reload on appstate preferences_put.
func TestSettingsPersistsThemeAndScaleLocally(t *testing.T) {
	t.Parallel()
	srv := &lockedVaultAppStateServer{}
	sock := serveAppStateSocket(t, srv)

	sc1, c1, env1, light1, dark1 := dialAppStateTestScreens(t, sock)
	if !sc1.vaultGate.active(sc1.vault) {
		t.Fatal("expected vault to start locked")
	}
	sc1.updatePreferences(settings.Preferences{ThemeMode: settings.ThemeDark, UIScale: settings.Scale125})
	sc1.applyLivePreferences(env1, light1, dark1)
	if env1.Th != dark1 || env1.UIScale != settings.Scale125 {
		t.Fatalf("live prefs not applied: th=%p (want dark %p) scale=%v", env1.Th, dark1, env1.UIScale)
	}
	saveRes := awaitSettingsResult(t, sc1.settingsWorker)
	sc1.applySettingsResults([]settings.Result{saveRes})
	if saveRes.Err != nil || sc1.settings.Prefs.Revision != 1 {
		t.Fatalf("save prefs while vault locked = (%d, %v)", sc1.settings.Prefs.Revision, saveRes.Err)
	}
	c1.Close()

	sc2, env2, light2, dark2 := assertPrefsRestoredAfterRestart(t, sock)
	assertAppStateEventReloadsWindow(t, srv, sc2, env2, light2, dark2)
}

func assertPrefsRestoredAfterRestart(t *testing.T, sock string) (*screens, *kit.Env, *theme.Theme, *theme.Theme) {
	t.Helper()
	sc2, _, env2, light2, dark2 := dialAppStateTestScreens(t, sock)
	loadReq := settings.Request{Kind: settings.OpLoadPrefs}
	sc2.settingsWorker.Do(&loadReq)
	loadRes := awaitSettingsResult(t, sc2.settingsWorker)
	sc2.applySettingsResults([]settings.Result{loadRes})
	sc2.applyLivePreferences(env2, light2, dark2)
	if env2.Th != dark2 || env2.UIScale != settings.Scale125 || sc2.settings.Prefs.Revision != 1 {
		t.Fatalf("restored prefs mismatch: theme=%v scale=%v rev=%d", sc2.settings.Prefs.Current.ThemeMode, env2.UIScale, sc2.settings.Prefs.Revision)
	}
	return sc2, env2, light2, dark2
}

func assertAppStateEventReloadsWindow(t *testing.T, srv *lockedVaultAppStateServer, sc *screens, env *kit.Env, light, dark *theme.Theme) {
	t.Helper()
	encoded, err := settings.EncodePreferences(settings.Preferences{ThemeMode: settings.ThemeLight, UIScale: settings.Scale090})
	if err != nil {
		t.Fatal(err)
	}
	srv.mu.Lock()
	srv.prefsRev = 2
	srv.prefsDoc = encoded
	srv.mu.Unlock()
	evPayload, err := json.Marshal(map[string]uint64{"revision": 2})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyUpdate(hostconn.Update{Topic: topicAppState, Event: &client.Event{Sequence: 2, Type: client.EventAppStatePreferencesPut, Payload: evPayload}})
	reloadRes := awaitSettingsResult(t, sc.settingsWorker)
	sc.applySettingsResults([]settings.Result{reloadRes})
	sc.applyLivePreferences(env, light, dark)
	if env.Th != light || env.UIScale != settings.Scale090 {
		t.Fatalf("topic preferences_put did not update window: theme=%v scale=%v", sc.settings.Prefs.Current.ThemeMode, env.UIScale)
	}
	_ = dark
}

type fakeAppSettingsClient struct {
	mu            sync.Mutex
	ws            client.Workspace
	eff           client.EffectiveWorkspaceConfig
	updateCalls   int
	validateCalls int
	confirmCalls  int
	rejectCalls   int
	issues        []client.WorkspaceConfigIssue
}

func (f *fakeAppSettingsClient) Workspace(context.Context, string) (client.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ws, nil
}

func (f *fakeAppSettingsClient) GetEffectiveConfig(context.Context, string) (client.EffectiveWorkspaceConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.eff, nil
}

func (f *fakeAppSettingsClient) UpdateWorkspaceConfig(_ context.Context, _ string, rawTOML []byte, expectedHash string) (client.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	if expectedHash != f.ws.ConfigHash {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errLockedAppStateScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.WorkspaceCodeConfigChanged}); err == nil {
			cerr.AddDetail(detail)
		}
		return client.Workspace{}, &client.WorkspaceConfigConflictError{Cause: cerr}
	}
	f.ws.RawTOML = rawTOML
	f.ws.ConfigHash = "hash-saved"
	f.ws.ConfigRevision++
	return f.ws, nil
}

func (f *fakeAppSettingsClient) PatchWorkspaceConfig(ctx context.Context, in *client.PatchWorkspaceConfigInput) (client.Workspace, error) {
	return f.UpdateWorkspaceConfig(ctx, in.WorkspaceID, f.ws.RawTOML, in.ExpectedHash)
}

func (f *fakeAppSettingsClient) GetAppStatePreferences(context.Context) (client.AppStatePreferences, error) {
	return client.AppStatePreferences{}, nil
}

func (f *fakeAppSettingsClient) PutAppStatePreferences(context.Context, *client.PutAppStatePreferencesInput) (uint64, error) {
	return 1, nil
}

func (f *fakeAppSettingsClient) ValidateConfig(_ context.Context, _ string, _ []byte) ([]client.WorkspaceConfigIssue, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.validateCalls++
	return f.issues, nil
}

func (f *fakeAppSettingsClient) ConfirmWorkspaceConfig(_ context.Context, _, expectedHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.confirmCalls++
	if expectedHash != f.ws.ConfigHash {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errLockedAppStateScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.WorkspaceCodeConfigChanged}); err == nil {
			cerr.AddDetail(detail)
		}
		return &client.WorkspaceConfigConflictError{Cause: cerr}
	}
	f.ws.PendingPolicyConfirmation = false
	f.ws.PolicyDiff = nil
	f.ws.PolicyRevision++
	return nil
}

func (f *fakeAppSettingsClient) RejectWorkspaceConfig(_ context.Context, _, expectedHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rejectCalls++
	if expectedHash != f.ws.ConfigHash {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errLockedAppStateScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.WorkspaceCodeConfigChanged}); err == nil {
			cerr.AddDetail(detail)
		}
		return &client.WorkspaceConfigConflictError{Cause: cerr}
	}
	f.ws.PendingPolicyConfirmation = false
	f.ws.PolicyDiff = nil
	return nil
}

// TestSettingsCASConflictKeepsUnsavedEdits verifies policy confirm gating,
// CAS conflict reload keeping unsaved form edits visible with changed-underneath
// reporting, Cmd/Ctrl+, shortcut toggling, and IME/Return non-submit behavior.
func TestSettingsCASConflictKeepsUnsavedEdits(t *testing.T) {
	t.Parallel()
	fc := &fakeAppSettingsClient{
		ws: client.Workspace{ID: testSettingsWSID, Name: testRawWSName, ConfigHash: "hash-v1", ConfigRevision: 1, RawTOML: []byte("name = \"alpha\"\n")},
		eff: client.EffectiveWorkspaceConfig{
			Provenance: map[string]int64{settings.FieldIdleDetachSeconds: client.ConfigTierDefault, settings.FieldApprovalMode: client.ConfigTierDefault},
			Settings: client.WorkspaceSettings{
				ApprovalMode: testApprovalAuto, SandboxProfile: "workspace-write", SessionTeams: true,
				IdleDetachSeconds: 300, TaskDeadlineSeconds: 3600, TaskMaxTurns: 50,
				SilenceTimeoutSeconds: 600, TeamDeadlineSeconds: 14400, TeamMaxAgents: 12,
			},
		},
	}
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: testSettingsInc})
	sc.workspaces.ApplyList([]workspace.Summary{{ID: testSettingsWSID, Name: testRawWSName, ConfigRevision: 1}})
	w := settings.NewWorker(func() settings.Client { return fc }, nil)
	w.Start(t.Context())
	t.Cleanup(w.Stop)
	sc.settingsWorker = w

	assertShortcutAndIMENonSubmit(t, sc, w, fc)
	assertPolicyConfirmAndConflictReload(t, sc, w, fc)
	assertRetrySaveAfterConflict(t, sc, w)
}

func assertShortcutAndIMENonSubmit(t *testing.T, sc *screens, w *settings.Worker, fc *fakeAppSettingsClient) {
	t.Helper()
	env := newInboxTestEnv(t)
	router := new(input.Router)
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	router.Queue(key.Event{Name: key.Name(","), Modifiers: key.ModShortcut, State: key.Press})
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if !sc.hasSlotKind(KindSettings) {
		t.Fatal("ModShortcut+, did not open the settings slot")
	}
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w), awaitSettingsResult(t, w)})

	sc.settings.Draft.IdleDetachSeconds = 120
	sc.settings.Draft.ApprovalMode = testApprovalAsk
	sc.settingsView.syncFromModel(sc.settings)
	var ops op.Ops
	focusGtx := layout.Context{
		Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(image.Pt(shotWindowW, shotWindowH)), Source: router.Source(),
	}
	env.Begin(focusGtx.Now)
	env.TrackInput(&focusGtx)
	focusGtx.Execute(key.FocusCmd{Tag: sc.settingsView.shellAllowField.Tag()})
	drawWindow(&focusGtx, env, sc)
	env.FrameEnd(&focusGtx)
	router.Frame(&ops)
	router.Queue(key.EditEvent{Text: "go test"}, key.Event{Name: key.NameReturn, State: key.Press})
	runAppFrame(env, sc, router, image.Pt(shotWindowW, shotWindowH))
	if fc.updateCalls != 0 {
		t.Fatalf("Return in settings field triggered unexpected save (%d calls)", fc.updateCalls)
	}
}

func assertPolicyConfirmAndConflictReload(t *testing.T, sc *screens, w *settings.Worker, fc *fakeAppSettingsClient) {
	t.Helper()
	sc.submitWorkspaceSettings(false)
	if !sc.settings.PolicyConfirmArmed || fc.updateCalls != 0 {
		t.Fatalf("expected policy confirm step without immediate save: armed=%v calls=%d", sc.settings.PolicyConfirmArmed, fc.updateCalls)
	}
	fc.mu.Lock()
	fc.ws.ConfigHash = "hash-external"
	fc.eff.Settings.TeamMaxAgents = 8
	fc.mu.Unlock()

	sc.submitWorkspaceSettings(true)
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	if sc.settings.ConfigHash != "hash-external" || sc.settings.Draft.IdleDetachSeconds != 120 || sc.settings.Draft.ApprovalMode != testApprovalAsk {
		t.Fatalf("CAS conflict did not reload hash or preserve unsaved edits: hash=%q draft=%+v", sc.settings.ConfigHash, sc.settings.Draft)
	}
	if got := sc.settingsView.ensureIntField(settings.FieldIdleDetachSeconds).field.Text(); got != "120" {
		t.Fatalf("field text = %q, want unsaved \"120\"", got)
	}
	if len(sc.settings.ChangedUnderneath) != 1 || sc.settings.ChangedUnderneath[0].ID != settings.FieldTeamMaxAgents {
		t.Fatalf("ChangedUnderneath = %+v, want %s", sc.settings.ChangedUnderneath, settings.FieldTeamMaxAgents)
	}
}

func assertRetrySaveAfterConflict(t *testing.T, sc *screens, w *settings.Worker) {
	t.Helper()
	sc.submitWorkspaceSettings(true)
	sc.applySettingsResults([]settings.Result{awaitSettingsResult(t, w)})
	if sc.settings.SaveErr != "" || sc.settings.ConfigHash != "hash-saved" || len(sc.settings.DirtyFields()) != 0 {
		t.Fatalf("retry save after CAS conflict failed: err=%q hash=%q dirty=%v", sc.settings.SaveErr, sc.settings.ConfigHash, sc.settings.DirtyFields())
	}
}
