package app

// The F10.20a evidence: the app state works while the vault is locked, CAS
// fails typed, the document envelope is enforced, and only the app client
// kind may use the service (host-api §5.2, HA-E07).

import (
	"bytes"
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
	testAppStatePass  = "app-state-test-passphrase"
	testAppStateOwner = "app"
)

// testWindowID is a deterministic valid window UUID.
func testWindowID(n int) string {
	return fmt.Sprintf("0d20a000-0000-4000-8000-%012d", n)
}

// startAppStateHost starts a host for the app-state tests.
func startAppStateHost(t *testing.T, dir string, clk *clock.Fake) *HostProcess {
	t.Helper()
	hp, err := StartHost(t.Context(), testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("start the host: %v", err)
	}
	return hp
}

func appStateService(t *testing.T, hp *HostProcess) *AppStateService {
	t.Helper()
	svc, err := hp.rt.AppStateService()
	if err != nil {
		t.Fatalf("read the app state service: %v", err)
	}
	return svc
}

func appStateWindowCmd(commandID, windowID string, expected uint64, layout []byte) *PutWindowCommand {
	return &PutWindowCommand{
		Principal:        testAppStateOwner,
		CommandID:        commandID,
		WindowID:         windowID,
		ExpectedRevision: expected,
		Layout:           layout,
	}
}

func TestAppStateRestoresEveryWindowWhileTheVaultIsLocked(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Unix(0, 0))
	dir := t.TempDir()

	first := startAppStateHost(t, dir, clk)
	// Configure the vault so the restart starts locked rather than
	// unconfigured: the layout store must work in both states.
	if _, err := first.Vault().SetupBytes(ctx, []byte(testAppStatePass)); err != nil {
		t.Fatalf("configure the vault: %v", err)
	}
	first.Vault().Lock()
	svc := appStateService(t, first)

	layoutMain := []byte(`{"schema_version":1,"slots":["main"],"ratio":0.5}`)
	layoutSide := []byte(`{"schema_version":1,"slots":["main","side"],"ratio":0.7}`)
	winMain, winSide := testWindowID(1), testWindowID(2)
	if _, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-restore-1", winMain, 0, layoutMain)); err != nil {
		t.Fatalf("put window 1: %v", err)
	}
	if _, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-restore-2", winSide, 0, layoutSide)); err != nil {
		t.Fatalf("put window 2: %v", err)
	}
	prefsRaw := []byte(`{"schema_version":1,"theme":"dark"}`)
	if _, err := svc.PutPreferences(ctx, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-restore-prefs", Preferences: prefsRaw,
	}); err != nil {
		t.Fatalf("put preferences: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close the first host: %v", err)
	}

	second, err := StartHost(ctx, testHostDeps(dir, clk, newScriptedTransport()))
	if err != nil {
		t.Fatalf("start the second host: %v", err)
	}
	t.Cleanup(func() { second.Close() })
	if got := second.Vault().Snapshot().State; got != vault.StateLocked {
		t.Fatalf("vault state after restart = %q, want locked", got)
	}
	svc = appStateService(t, second)

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
func assertRestoredWindow(t *testing.T, got AppStateWindowView, windowID string, revision uint64, layout []byte) {
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
	t.Cleanup(func() { hp.Close() })
	svc := appStateService(t, hp)

	win := testWindowID(3)
	rev, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-cas-create", win, 0, []byte(`{"schema_version":1}`)))
	if err != nil {
		t.Fatalf("create the window: %v", err)
	}
	if rev != 1 {
		t.Fatalf("create revision = %d, want 1", rev)
	}

	// A stale expected revision on update, create-on-existing and delete all
	// fail typed with the current revision attached.
	stale := rev + 7
	_, err = svc.PutWindow(ctx, appStateWindowCmd("cmd-cas-stale", win, stale, []byte(`{"schema_version":1}`)))
	assertAppStateConflict(t, err, rev)
	_, err = svc.PutWindow(ctx, appStateWindowCmd("cmd-cas-recreate", win, 0, []byte(`{"schema_version":1}`)))
	assertAppStateConflict(t, err, rev)
	err = svc.DeleteWindow(ctx, &DeleteWindowCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-del", WindowID: win, ExpectedRevision: stale,
	})
	assertAppStateConflict(t, err, rev)

	// The same CAS rule holds for the shared preferences.
	if _, err := svc.PutPreferences(ctx, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-prefs", Preferences: []byte(`{"schema_version":1}`),
	}); err != nil {
		t.Fatalf("create the preferences: %v", err)
	}
	_, err = svc.PutPreferences(ctx, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-cas-prefs-stale", ExpectedRevision: 99,
		Preferences: []byte(`{"schema_version":1}`),
	})
	assertAppStateConflict(t, err, 1)

	// The current revision from the conflict lets the caller retry, and the
	// retry succeeds.
	_, err = svc.PutWindow(ctx, appStateWindowCmd("cmd-cas-stale", win, stale, []byte(`{"schema_version":1}`)))
	if !errors.As(err, new(*appstate.ConflictError)) {
		t.Fatalf("retry with the stale revision = %v, want a conflict", err)
	}
	if _, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-cas-retry", win, rev, []byte(`{"schema_version":1,"slots":[]}`))); err != nil {
		t.Fatalf("retry with the current revision: %v", err)
	}
}

// assertAppStateConflict asserts a typed revision_conflict with the current
// revision attached.
func assertAppStateConflict(t *testing.T, err error, current uint64) {
	t.Helper()
	if err == nil {
		t.Fatal("the CAS write succeeded, want appstate.revision_conflict")
	}
	if errcode.CodeOf(err) != errcode.AppStateRevisionConflict {
		t.Fatalf("code = %q, want appstate.revision_conflict", errcode.CodeOf(err))
	}
	var conflict *appstate.ConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("the conflict carries no typed current revision: %v", err)
	}
	if conflict.CurrentRevision() != current {
		t.Fatalf("current revision = %d, want %d", conflict.CurrentRevision(), current)
	}
}

func TestAppStateRejectsInvalidOrOversizedDocuments(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	t.Cleanup(func() { hp.Close() })
	svc := appStateService(t, hp)

	// Sixteen valid windows fill the store, so the window-count case can run
	// last.
	fill := func(t *testing.T) {
		t.Helper()
		for n := 1; n <= appstate.MaxWindows; n++ {
			if _, err := svc.PutWindow(ctx, appStateWindowCmd(fmt.Sprintf("cmd-fill-%d", n), testWindowID(100+n), 0, []byte(`{"schema_version":1}`))); err != nil {
				t.Fatalf("put fill window %d: %v", n, err)
			}
		}
	}

	cases := []struct {
		name string
		code errcode.Code
		put  func(t *testing.T) error
	}{
		{
			name: "not json",
			code: errcode.AppStateInvalidDocument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-1", testWindowID(1), 0, []byte(`{"schema_version":`)))
				return err
			},
		},
		{
			name: "missing schema_version",
			code: errcode.AppStateInvalidDocument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-2", testWindowID(2), 0, []byte(`{"slots":[]}`)))
				return err
			},
		},
		{
			name: "schema_version zero",
			code: errcode.AppStateInvalidDocument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-3", testWindowID(3), 0, []byte(`{"schema_version":0}`)))
				return err
			},
		},
		{
			name: "schema_version not a number",
			code: errcode.AppStateInvalidDocument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-4", testWindowID(4), 0, []byte(`{"schema_version":"1"}`)))
				return err
			},
		},
		{
			name: "schema_version fractional",
			code: errcode.AppStateInvalidDocument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-5", testWindowID(5), 0, []byte(`{"schema_version":1.5}`)))
				return err
			},
		},
		{
			name: "layout too large",
			code: errcode.AppStateTooLarge,
			put: func(t *testing.T) error {
				big := []byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", appstate.MaxLayoutBytes) + `"}`)
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-6", testWindowID(6), 0, big))
				return err
			},
		},
		{
			name: "preferences too large",
			code: errcode.AppStateTooLarge,
			put: func(t *testing.T) error {
				big := []byte(`{"schema_version":1,"pad":"` + strings.Repeat("x", appstate.MaxPreferencesBytes) + `"}`)
				_, err := svc.PutPreferences(ctx, &PutPreferencesCommand{
					Principal: testAppStateOwner, CommandID: "cmd-bad-7", Preferences: big,
				})
				return err
			},
		},
		{
			name: "window id not a uuid",
			code: errcode.InvalidArgument,
			put: func(t *testing.T) error {
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-8", "not-a-uuid", 0, []byte(`{"schema_version":1}`)))
				return err
			},
		},
		{
			name: "too many windows",
			code: errcode.AppStateTooManyWindows,
			put: func(t *testing.T) error {
				fill(t)
				_, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-bad-9", testWindowID(200), 0, []byte(`{"schema_version":1}`)))
				return err
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := tc.put(t)
			if errcode.CodeOf(err) != tc.code {
				t.Fatalf("code = %q, want %q (err %v)", errcode.CodeOf(err), tc.code, err)
			}
		})
	}
}

func TestAppStateDeniesNonAppPrincipals(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	t.Cleanup(func() { hp.Close() })
	svc := appStateService(t, hp)

	win := testWindowID(9)
	denied := []string{
		string(KindTUI), string(KindCLI), string(KindAppBackground),
		PrincipalSession, "", "guard", "unknown-kind",
	}
	for _, principal := range denied {
		principal := principal
		t.Run("principal "+principal, func(t *testing.T) {
			t.Parallel()
			if _, err := svc.ListWindows(ctx, principal); !appStateDenied(err) {
				t.Fatalf("ListWindows principal %q = %v, want permission_denied", principal, err)
			}
			if _, err := svc.PutWindow(ctx, &PutWindowCommand{Principal: principal, CommandID: "cmd-deny-1", WindowID: win, Layout: []byte(`{"schema_version":1}`)}); !appStateDenied(err) {
				t.Fatalf("PutWindow principal %q = %v, want permission_denied", principal, err)
			}
			if err := svc.DeleteWindow(ctx, &DeleteWindowCommand{Principal: principal, CommandID: "cmd-deny-2", WindowID: win, ExpectedRevision: 1}); !appStateDenied(err) {
				t.Fatalf("DeleteWindow principal %q = %v, want permission_denied", principal, err)
			}
			if _, err := svc.GetPreferences(ctx, principal); !appStateDenied(err) {
				t.Fatalf("GetPreferences principal %q = %v, want permission_denied", principal, err)
			}
			if _, err := svc.PutPreferences(ctx, &PutPreferencesCommand{Principal: principal, CommandID: "cmd-deny-3", Preferences: []byte(`{"schema_version":1}`)}); !appStateDenied(err) {
				t.Fatalf("PutPreferences principal %q = %v, want permission_denied", principal, err)
			}
		})
	}

	// The app client kind works, including after all those denials.
	if _, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-allow", win, 0, []byte(`{"schema_version":1}`))); err != nil {
		t.Fatalf("PutWindow as the app client: %v", err)
	}
}

// appStateDenied reports whether err is the app-state principal denial.
func appStateDenied(err error) bool {
	return err != nil && errcode.CodeOf(err) == errcode.PermissionDenied
}

func TestAppStateTopicEventsCarryNoDocumentContent(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	hp := startAppStateHost(t, t.TempDir(), clock.NewFake(time.Unix(0, 0)))
	t.Cleanup(func() { hp.Close() })
	svc := appStateService(t, hp)
	hub := hp.rt.hub

	sub, err := hub.Subscribe(ctx, SubscribeParams{Principal: testAppStateOwner, Topic: TopicAppState})
	if err != nil {
		t.Fatalf("subscribe to the appstate topic: %v", err)
	}
	t.Cleanup(sub.Close)

	layout := []byte(`{"schema_version":1,"secret-marker":"layout-9f31c2"}`)
	prefs := []byte(`{"schema_version":1,"secret-marker":"prefs-7b04de"}`)
	if _, err := svc.PutWindow(ctx, appStateWindowCmd("cmd-topic-1", testWindowID(10), 0, layout)); err != nil {
		t.Fatalf("put the window: %v", err)
	}
	if _, err := svc.PutPreferences(ctx, &PutPreferencesCommand{
		Principal: testAppStateOwner, CommandID: "cmd-topic-2", Preferences: prefs,
	}); err != nil {
		t.Fatalf("put the preferences: %v", err)
	}
	if err := svc.DeleteWindow(ctx, &DeleteWindowCommand{
		Principal: testAppStateOwner, CommandID: "cmd-topic-3", WindowID: testWindowID(10), ExpectedRevision: 1,
	}); err != nil {
		t.Fatalf("delete the window: %v", err)
	}

	events := collectN(t, ctx, sub, 3)
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
