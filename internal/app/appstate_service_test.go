package app

// The F10.20a evidence: the app state works while the vault is locked, CAS
// fails typed, the document envelope is enforced, and only the app client
// kind may use the service (host-api §5.2, HA-E07).

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/appstate"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	// testAppStateMasterUnlock configures the vault so a restart starts
	// locked rather than unconfigured.
	testAppStateMasterUnlock = "app-state-test-unlock-phrase"
	testAppStateOwner        = "app"
	testStateConflictCode    = "appstate.revision_conflict"
)

// testWindowID is a deterministic valid window UUID.
func testWindowID(n int) string {
	return fmt.Sprintf("0d20a000-0000-4000-8000-%012d", n)
}

// startAppStateHost starts a host for the app-state tests; closeAppStateHost
// closes it at the end of the test.
func startAppStateHost(t *testing.T, dir string, clk *clock.Fake) *HostProcess {
	t.Helper()
	hp, err := StartHost(t.Context(), testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("start the host: %v", err)
	}
	return hp
}

// closeAppStateHost closes a host at the end of the test and reports a
// failed close.
func closeAppStateHost(t *testing.T, hp *HostProcess) {
	t.Helper()
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close the app-state host: %v", closeErr)
		}
	})
}

func stateServiceOf(t *testing.T, hp *HostProcess) *StateService {
	t.Helper()
	svc, err := hp.rt.AppStateService()
	if err != nil {
		t.Fatalf("read the app state service: %v", err)
	}
	return svc
}

func stateWindowCmd(commandID, windowID string, expected uint64, layout []byte) *PutWindowCommand {
	return &PutWindowCommand{
		Principal:        testAppStateOwner,
		CommandID:        commandID,
		WindowID:         windowID,
		ExpectedRevision: expected,
		Layout:           layout,
	}
}

func mustPutWindow(t *testing.T, ctx context.Context, svc *StateService, cmd *PutWindowCommand) uint64 {
	t.Helper()
	revision, err := svc.PutWindow(ctx, cmd)
	if err != nil {
		t.Fatalf("put window %s: %v", cmd.WindowID, err)
	}
	return revision
}

func mustPutPreferences(t *testing.T, ctx context.Context, svc *StateService, cmd *PutPreferencesCommand) uint64 {
	t.Helper()
	revision, err := svc.PutPreferences(ctx, cmd)
	if err != nil {
		t.Fatalf("put preferences %s: %v", cmd.CommandID, err)
	}
	return revision
}

func TestAppStateRestoresEveryWindowWhileTheVaultIsLocked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	dir := t.TempDir()

	first := startAppStateHost(t, dir, clk)
	// Configure the vault so the restart starts locked rather than
	// unconfigured: the layout store must work in both states.
	if _, err := first.Vault().SetupBytes(ctx, []byte(testAppStateMasterUnlock)); err != nil {
		t.Fatalf("configure the vault: %v", err)
	}
	first.Vault().Lock()
	svc := stateServiceOf(t, first)

	layoutMain := []byte(`{"schema_version":1,"slots":["main"],"ratio":0.5}`)
	layoutSide := []byte(`{"schema_version":1,"slots":["main","side"],"ratio":0.7}`)
	winMain, winSide := testWindowID(1), testWindowID(2)
	mustPutWindow(t, ctx, svc, stateWindowCmd("cmd-restore-1", winMain, 0, layoutMain))
	mustPutWindow(t, ctx, svc, stateWindowCmd("cmd-restore-2", winSide, 0, layoutSide))
	prefsRaw := []byte(`{"schema_version":1,"theme":"dark"}`)
	mustPutPreferences(t, ctx, svc, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-restore-prefs", Preferences: prefsRaw,
	})
	if err := first.Close(); err != nil {
		t.Fatalf("close the first host: %v", err)
	}

	second, err := StartHost(ctx, testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("start the second host: %v", err)
	}
	closeAppStateHost(t, second)
	if got := second.Vault().Snapshot().State; got != vault.StateLocked {
		t.Fatalf("vault state after restart = %q, want locked", got)
	}
	svc = stateServiceOf(t, second)

	windows, err := svc.ListWindows(ctx, testAppStateOwner)
	if err != nil {
		t.Fatalf("list the windows while the vault is locked: %v", err)
	}
	if len(windows) != 2 {
		t.Fatalf("restored %d windows, want 2", len(windows))
	}
	assertRestoredWindow(t, windows[0], winMain, 1, layoutMain)
	assertRestoredWindow(t, windows[1], winSide, 1, layoutSide)
	prefs, err := svc.GetPreferences(ctx, testAppStateOwner)
	if err != nil {
		t.Fatalf("read the preferences while the vault is locked: %v", err)
	}
	if prefs.Revision != 1 || string(prefs.Document) != string(prefsRaw) {
		t.Fatalf("restored preferences = (%d, %s), want (1, %s)", prefs.Revision, prefs.Document, prefsRaw)
	}
}

// assertRestoredWindow asserts one restored window's identity, revision and
// exact layout bytes.
func assertRestoredWindow(t *testing.T, got StateWindowView, windowID string, revision uint64, layout []byte) {
	t.Helper()
	if got.WindowID != windowID || got.Revision != revision || string(got.Layout) != string(layout) {
		t.Fatalf("restored window = (%s, %d, %s), want (%s, %d, %s)",
			got.WindowID, got.Revision, got.Layout, windowID, revision, layout)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("the restored window carries no updated_at")
	}
}

func TestAppStatePutRejectsAStaleRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	closeAppStateHost(t, hp)
	svc := stateServiceOf(t, hp)

	win := testWindowID(3)
	rev, err := svc.PutWindow(ctx, stateWindowCmd("cmd-cas-create", win, 0, []byte(`{"schema_version":1}`)))
	if err != nil {
		t.Fatalf("create the window: %v", err)
	}
	if rev != 1 {
		t.Fatalf("create revision = %d, want 1", rev)
	}

	// A stale expected revision on update, create-on-existing and delete all
	// fail typed with the current revision attached.
	stale := rev + 7
	_, err = svc.PutWindow(ctx, stateWindowCmd("cmd-cas-stale", win, stale, []byte(`{"schema_version":1}`)))
	assertStateConflict(t, err, rev)
	_, err = svc.PutWindow(ctx, stateWindowCmd("cmd-cas-recreate", win, 0, []byte(`{"schema_version":1}`)))
	assertStateConflict(t, err, rev)
	err = svc.DeleteWindow(ctx, &DeleteWindowCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-del", WindowID: win, ExpectedRevision: stale,
	})
	assertStateConflict(t, err, rev)

	// The same CAS rule holds for the shared preferences.
	mustPutPreferences(t, ctx, svc, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-prefs", Preferences: []byte(`{"schema_version":1}`),
	})
	_, err = svc.PutPreferences(ctx, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-prefs-stale", ExpectedRevision: 99,
		Preferences: []byte(`{"schema_version":1}`),
	})
	assertStateConflict(t, err, 1)

	// The retry with the stale revision conflicts again; the retry with the
	// current revision succeeds.
	_, err = svc.PutWindow(ctx, stateWindowCmd("cmd-cas-stale", win, stale, []byte(`{"schema_version":1}`)))
	var conflict *appstate.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("retry with the stale revision = %v, want a conflict", err)
	}
	if _, err := svc.PutWindow(ctx, stateWindowCmd("cmd-cas-retry", win, rev, []byte(`{"schema_version":1,"slots":[]}`))); err != nil {
		t.Fatalf("retry with the current revision: %v", err)
	}
}

// assertStateConflict asserts a typed revision_conflict with the current
// revision attached.
func assertStateConflict(t *testing.T, err error, current uint64) {
	t.Helper()
	if err == nil {
		t.Fatal("the CAS write succeeded, want a conflict")
	}
	if errcode.CodeOf(err) != errcode.AppStateRevisionConflict {
		t.Fatalf("code = %q, want %s", errcode.CodeOf(err), testStateConflictCode)
	}
	var conflict *appstate.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("the conflict carries no typed current revision: %v", err)
	}
	if conflict.CurrentRevision() != current {
		t.Fatalf("current revision = %d, want %d", conflict.CurrentRevision(), current)
	}
}

// The document kinds a case submits through.
const (
	kindLayout      = "layout"
	kindPreferences = "preferences"
)

// documentCase is one invalid or oversized document the host must reject.
type documentCase struct {
	name, doc string
	kind      string
	badID     bool
	fill      bool
	want      errcode.Code
}

func invalidDocumentCases() []documentCase {
	bigLayout := `{"schema_version":1,"pad":"` + strings.Repeat("x", appstate.MaxLayoutBytes) + `"}`
	bigPrefs := `{"schema_version":1,"pad":"` + strings.Repeat("x", appstate.MaxPreferencesBytes) + `"}`
	return []documentCase{
		{name: "not json", kind: kindLayout, doc: `{"schema_version":`, want: errcode.AppStateInvalidDocument},
		{name: "missing schema_version", kind: kindLayout, doc: `{"slots":[]}`, want: errcode.AppStateInvalidDocument},
		{name: "schema_version zero", kind: kindLayout, doc: `{"schema_version":0}`, want: errcode.AppStateInvalidDocument},
		{name: "schema_version not a number", kind: kindLayout, doc: `{"schema_version":"1"}`, want: errcode.AppStateInvalidDocument},
		{name: "schema_version fractional", kind: kindLayout, doc: `{"schema_version":1.5}`, want: errcode.AppStateInvalidDocument},
		{name: "layout too large", kind: kindLayout, doc: bigLayout, want: errcode.AppStateTooLarge},
		{name: "preferences too large", kind: kindPreferences, doc: bigPrefs, want: errcode.AppStateTooLarge},
		{name: "window id not a uuid", kind: kindLayout, doc: `{"schema_version":1}`, badID: true, want: errcode.InvalidArgument},
		{name: "too many windows", kind: kindLayout, doc: `{"schema_version":1}`, fill: true, want: errcode.AppStateTooManyWindows},
	}
}

// putDocument submits one case's document through the RPC it names.
func putDocument(t *testing.T, ctx context.Context, svc *StateService, tc documentCase, seq int) error {
	t.Helper()
	doc := []byte(tc.doc)
	windowID := testWindowID(300 + seq)
	if tc.badID {
		windowID = "not-a-uuid"
	}
	switch tc.kind {
	case kindPreferences:
		_, err := svc.PutPreferences(ctx, &PutPreferencesCommand{
			Principal: testAppStateOwner, CommandID: fmt.Sprintf("cmd-bad-%d", seq), Preferences: doc,
		})
		return err
	default:
		_, err := svc.PutWindow(ctx, stateWindowCmd(fmt.Sprintf("cmd-bad-%d", seq), windowID, 0, doc))
		return err
	}
}

func TestAppStateRejectsInvalidOrOversizedDocuments(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	closeAppStateHost(t, hp)
	svc := stateServiceOf(t, hp)

	for seq, tc := range invalidDocumentCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if tc.fill {
				for n := 1; n <= appstate.MaxWindows; n++ {
					mustPutWindow(t, ctx, svc, stateWindowCmd(fmt.Sprintf("cmd-fill-%d", n), testWindowID(100+n), 0, []byte(`{"schema_version":1}`)))
				}
			}
			err := putDocument(t, ctx, svc, tc, seq)
			if errcode.CodeOf(err) != tc.want {
				t.Fatalf("code = %q, want %q (err %v)", errcode.CodeOf(err), tc.want, err)
			}
		})
	}
}

func TestAppStateDeniesNonAppPrincipals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	closeAppStateHost(t, hp)
	svc := stateServiceOf(t, hp)

	win := testWindowID(9)
	denied := []string{
		string(KindTUI), string(KindCLI), string(KindAppBackground),
		PrincipalSession, "", "guard", "unknown-kind",
	}
	for _, principal := range denied {
		t.Run("principal "+principal, func(t *testing.T) {
			t.Parallel()
			assertAppStateOpsDenied(t, ctx, svc, principal, win)
		})
	}

	// The app client kind works, including after all those denials.
	mustPutWindow(t, ctx, svc, stateWindowCmd("cmd-allow", win, 0, []byte(`{"schema_version":1}`)))
}

// assertAppStateOpsDenied runs every app-state operation as principal and
// expects the typed denial on each.
func assertAppStateOpsDenied(t *testing.T, ctx context.Context, svc *StateService, principal, win string) {
	t.Helper()
	if _, err := svc.ListWindows(ctx, principal); !stateDenied(err) {
		t.Fatalf("ListWindows principal %q = %v, want permission_denied", principal, err)
	}
	if _, err := svc.PutWindow(ctx, &PutWindowCommand{Principal: principal, CommandID: "cmd-deny-1", WindowID: win, Layout: []byte(`{"schema_version":1}`)}); !stateDenied(err) {
		t.Fatalf("PutWindow principal %q = %v, want permission_denied", principal, err)
	}
	if err := svc.DeleteWindow(ctx, &DeleteWindowCommand{Principal: principal, CommandID: "cmd-deny-2", WindowID: win, ExpectedRevision: 1}); !stateDenied(err) {
		t.Fatalf("DeleteWindow principal %q = %v, want permission_denied", principal, err)
	}
	if _, err := svc.GetPreferences(ctx, principal); !stateDenied(err) {
		t.Fatalf("GetPreferences principal %q = %v, want permission_denied", principal, err)
	}
	if _, err := svc.PutPreferences(ctx, &PutPreferencesCommand{Principal: principal, CommandID: "cmd-deny-3", Preferences: []byte(`{"schema_version":1}`)}); !stateDenied(err) {
		t.Fatalf("PutPreferences principal %q = %v, want permission_denied", principal, err)
	}
}

// stateDenied reports whether err is the app-state principal denial.
func stateDenied(err error) bool {
	return err != nil && errcode.CodeOf(err) == errcode.PermissionDenied
}

func TestAppStateTopicEventsCarryNoDocumentContent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	closeAppStateHost(t, hp)
	svc := stateServiceOf(t, hp)

	sub, err := hp.rt.hub.Subscribe(ctx, SubscribeParams{Principal: testAppStateOwner, Topic: TopicAppState})
	if err != nil {
		t.Fatalf("subscribe to the appstate topic: %v", err)
	}
	t.Cleanup(sub.Close)

	layout := []byte(`{"schema_version":1,"secret-marker":"layout-9f31c2"}`)
	prefs := []byte(`{"schema_version":1,"secret-marker":"prefs-7b04de"}`)
	mustPutWindow(t, ctx, svc, stateWindowCmd("cmd-topic-1", testWindowID(10), 0, layout))
	mustPutPreferences(t, ctx, svc, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-topic-2", Preferences: prefs,
	})
	if err := svc.DeleteWindow(ctx, &DeleteWindowCommand{
		Principal: testAppStateOwner, CommandID: "cmd-topic-3", WindowID: testWindowID(10), ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("delete the window: %v", err)
	}

	events := collectN(t, ctx, sub, 3)
	assertTopicEventsCarryNoDocuments(t, events, layout, prefs)
}

// assertTopicEventsCarryNoDocuments asserts the topic events' order and that
// none of them carries a document.
func assertTopicEventsCarryNoDocuments(t *testing.T, events []Event, layout, prefs []byte) {
	t.Helper()
	wantTypes := []string{EventAppStateWindowPut, EventAppStatePreferencesPut, EventAppStateWindowDeleted}
	for i, ev := range events {
		if ev.Type != wantTypes[i] {
			t.Fatalf("event %d type = %q, want %q", i, ev.Type, wantTypes[i])
		}
		if bytes.Contains(ev.Payload, []byte("secret-marker")) || bytes.Contains(ev.Payload, layout) || bytes.Contains(ev.Payload, prefs) {
			t.Fatalf("event %d carries document content: %s", i, ev.Payload)
		}
	}
	// The payloads are exactly the identity-and-revision shape the client
	// decodes: the put and the delete name the window and the revision, the
	// preferences event carries the revision alone.
	want := []string{
		`{"window_id":"` + testWindowID(10) + `","revision":1}`,
		`{"revision":1}`,
		`{"window_id":"` + testWindowID(10) + `","revision":1}`,
	}
	for i, ev := range events {
		if string(ev.Payload) != want[i] {
			t.Fatalf("event %d payload = %s, want %s", i, ev.Payload, want[i])
		}
	}
}
