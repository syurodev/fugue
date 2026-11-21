package settings

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"connectrpc.com/connect"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/pkg/client"
	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

var errSettingsScripted = errors.New("scripted settings failure")

type fakeSettingsClient struct {
	mu           sync.Mutex
	ws           client.Workspace
	eff          client.EffectiveWorkspaceConfig
	prefsRev     uint64
	prefsRaw     []byte
	updateCalls  int
	putPrefCalls int
	conflictOnce bool
}

func (f *fakeSettingsClient) Workspace(context.Context, string) (client.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ws, nil
}

func (f *fakeSettingsClient) GetEffectiveConfig(context.Context, string) (client.EffectiveWorkspaceConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.eff, nil
}

func (f *fakeSettingsClient) UpdateWorkspaceConfig(_ context.Context, _ string, rawTOML []byte, expectedHash string) (client.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	if expectedHash != f.ws.ConfigHash {
		cerr := connect.NewError(connect.CodeFailedPrecondition, errSettingsScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.WorkspaceCodeConfigChanged}); err == nil {
			cerr.AddDetail(detail)
		}
		return client.Workspace{}, &client.WorkspaceConfigConflictError{Cause: cerr}
	}
	f.ws.RawTOML = rawTOML
	f.ws.ConfigHash = "hash-updated"
	f.ws.ConfigRevision++
	return f.ws, nil
}

func (f *fakeSettingsClient) GetAppStatePreferences(context.Context) (client.AppStatePreferences, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return client.AppStatePreferences{Revision: f.prefsRev, Preferences: f.prefsRaw}, nil
}

func (f *fakeSettingsClient) PutAppStatePreferences(_ context.Context, in *client.PutAppStatePreferencesInput) (uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.putPrefCalls++
	if f.conflictOnce || in.ExpectedRevision != f.prefsRev {
		f.conflictOnce = false
		cerr := connect.NewError(connect.CodeFailedPrecondition, errSettingsScripted)
		if detail, err := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: client.AppStateConflictCode, CurrentRevision: f.prefsRev}); err == nil {
			cerr.AddDetail(detail)
		}
		return 0, cerr
	}
	f.prefsRev++
	f.prefsRaw = in.Preferences
	return f.prefsRev, nil
}

func TestPreferencesDecodeEncodeAndHigherSchema(t *testing.T) {
	t.Parallel()
	prefs, higher, err := DecodePreferences(nil)
	if err != nil || higher || prefs != DefaultPreferences() {
		t.Fatalf("empty prefs = (%+v, %v, %v)", prefs, higher, err)
	}
	prefs, higher, err = DecodePreferences([]byte(`{"schema_version":99,"theme_mode":"dark","ui_scale":1.25}`))
	if err != nil || !higher || prefs != DefaultPreferences() {
		t.Fatalf("higher schema = (%+v, %v, %v)", prefs, higher, err)
	}
	raw, err := EncodePreferences(Preferences{ThemeMode: ThemeDark, UIScale: Scale125})
	if err != nil {
		t.Fatal(err)
	}
	decoded, higher, err := DecodePreferences(raw)
	if err != nil || higher || decoded.ThemeMode != ThemeDark || decoded.UIScale != Scale125 {
		t.Fatalf("round-trip = (%+v, %v, %v)", decoded, higher, err)
	}
}

const testSettingsWSID = "ws-1"

func newTestSettingsClient() *fakeSettingsClient {
	return &fakeSettingsClient{
		ws: client.Workspace{ID: testSettingsWSID, Name: "alpha", ConfigHash: "hash-1", ConfigRevision: 1, RawTOML: []byte("name = \"alpha\"\n")},
		eff: client.EffectiveWorkspaceConfig{
			Provenance: map[string]int64{FieldIdleDetachSeconds: client.ConfigTierGlobal, FieldApprovalMode: client.ConfigTierDefault},
			Settings: client.WorkspaceSettings{
				ApprovalMode: "auto", SandboxProfile: "workspace-write",
				SessionTeams: true, IdleDetachSeconds: 300, TaskDeadlineSeconds: 3600,
				TaskMaxTurns: 50, SilenceTimeoutSeconds: 600, TeamDeadlineSeconds: 14400, TeamMaxAgents: 12,
			},
		},
		prefsRev: 2,
	}
}

func TestModelDirtyPatchAndWorkerConflictReload(t *testing.T) {
	t.Parallel()
	fc := newTestSettingsClient()
	var wakes atomic.Int32
	w := NewWorker(func() Client { return fc }, func() { wakes.Add(1) })
	w.Start(t.Context())
	defer w.Stop()

	m := NewModel()
	m.SelectWorkspace(testSettingsWSID, "alpha")
	req := Request{Kind: OpLoadWorkspace, WorkspaceID: testSettingsWSID}
	w.Do(&req)
	res := <-w.Results()
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	m.ApplyWorkspaceLoaded(res.Workspace, res.Effective)

	patch := assertDirtyPatch(t, m)
	assertCASConflictReload(t, fc, w, m, patch)
	assertPrefsCASRetry(t, fc, w, m)
	if wakes.Load() == 0 {
		t.Fatal("expected worker onResult callback to fire")
	}
}

func assertDirtyPatch(t *testing.T, m *Model) *client.WorkspaceSettingsPatch {
	t.Helper()
	m.Draft.IdleDetachSeconds = 120
	m.Draft.ApprovalMode = "ask"
	if diff := cmp.Diff([]string{FieldApprovalMode, FieldIdleDetachSeconds}, m.DirtyFields()); diff != "" {
		t.Fatalf("dirty fields = %s", diff)
	}
	if diff := cmp.Diff([]string{FieldApprovalMode}, m.DirtyPolicyFields()); diff != "" {
		t.Fatalf("dirty policy fields = %s", diff)
	}
	patch, ok := m.BuildPatch()
	if !ok || patch.IdleDetachSeconds == nil || *patch.IdleDetachSeconds != 120 || patch.TaskMaxTurns != nil {
		t.Fatalf("unexpected patch: %+v ok=%v", patch, ok)
	}
	return patch
}

func assertCASConflictReload(t *testing.T, fc *fakeSettingsClient, w *Worker, m *Model, patch *client.WorkspaceSettingsPatch) {
	t.Helper()
	fc.mu.Lock()
	fc.ws.ConfigHash = "hash-external"
	fc.eff.Settings.TeamMaxAgents = 8
	fc.mu.Unlock()

	raw, err := client.PatchWorkspaceTOML(m.RawTOML, patch)
	if err != nil {
		t.Fatal(err)
	}
	saveReq := Request{Kind: OpSaveWorkspace, WorkspaceID: testSettingsWSID, RawTOML: raw, ExpectedHash: m.ConfigHash}
	w.Do(&saveReq)
	saveRes := <-w.Results()
	if !saveRes.CASConflict || saveRes.Workspace == nil || saveRes.Effective == nil {
		t.Fatalf("expected CAS conflict reload, got %+v", saveRes)
	}
	m.ApplyCASConflictReload(saveRes.Workspace, saveRes.Effective, saveRes.Err)
	if m.ConfigHash != "hash-external" || m.Draft.IdleDetachSeconds != 120 || m.Draft.ApprovalMode != "ask" {
		t.Fatalf("unsaved draft edits lost or hash not updated: hash=%q draft=%+v", m.ConfigHash, m.Draft)
	}
	if len(m.ChangedUnderneath) != 1 || m.ChangedUnderneath[0].ID != FieldTeamMaxAgents {
		t.Fatalf("ChangedUnderneath = %+v, want %s", m.ChangedUnderneath, FieldTeamMaxAgents)
	}
}

func assertPrefsCASRetry(t *testing.T, fc *fakeSettingsClient, w *Worker, m *Model) {
	t.Helper()
	fc.mu.Lock()
	fc.conflictOnce = true
	fc.mu.Unlock()
	prefReq := Request{Kind: OpSavePrefs, CommandID: m.Prefs.NextCommandID(), PrefsRevision: 1, Prefs: Preferences{ThemeMode: ThemeDark, UIScale: Scale125}}
	w.Do(&prefReq)
	prefRes := <-w.Results()
	if prefRes.Err != nil || prefRes.PrefsRevision != 3 {
		t.Fatalf("prefs save after CAS retry = (%d, %v)", prefRes.PrefsRevision, prefRes.Err)
	}
}

func TestParsersAndValidation(t *testing.T) {
	t.Parallel()
	if got := ParseShellAllow(" git status,\ngo test , "); !cmp.Equal(got, []string{"git status", "go test"}) {
		t.Fatalf("ParseShellAllow = %v", got)
	}
	tags, err := ParseEnvTags("*.prod=prod\nsecrets/*=sensitive")
	if err != nil || len(tags) != 2 || tags[0].Tag != "prod" {
		t.Fatalf("ParseEnvTags = (%+v, %v)", tags, err)
	}
	if _, err := ParseEnvTags("missing_equals"); err == nil {
		t.Fatal("expected invalid env tag error")
	}
	if _, err := ParseBoundedInt("999999", 0, MaxIdleDetachSeconds); err == nil {
		t.Fatal("expected out-of-range error")
	}
}
