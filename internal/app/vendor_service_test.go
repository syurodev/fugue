package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	testAppPrincipal = string(KindApp)
	testStateClosed  = "closed"
	testNoSession    = "no-such-session"
)

type vendorServiceFixture struct {
	svc *VendorService
	rt  *HostRuntime
	ws  string
}

func newVendorServiceFixture(t *testing.T) *vendorServiceFixture {
	t.Helper()
	return newVendorServiceFixtureWith(t, nil)
}

// newVendorServiceFixtureWith starts the production host wiring with the
// given test tools registered.
func newVendorServiceFixtureWith(t *testing.T, tools []*FugueTool) *vendorServiceFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Real{}
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: t.TempDir(), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-svc")),
		Transport: newScriptedTransport(), VendorBinary: exe, TestFugueTools: tools,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	if _, setupErr := hp.Vault().Setup(t.Context(), redact.NewSecret("vendor-service-password")); setupErr != nil {
		t.Fatal(setupErr)
	}
	rt := hp.Runtime()
	svc, err := rt.VendorService()
	if err != nil {
		t.Fatal(err)
	}
	f := &vendorServiceFixture{svc: svc, rt: rt}
	f.ws = f.addWorkspace(t, "svc-ws")
	return f
}

// addWorkspace creates a workspace with one root named "main".
func (f *vendorServiceFixture) addWorkspace(t *testing.T, name string) string {
	t.Helper()
	ws, err := f.rt.CreateWorkspace(t.Context(), testAppPrincipal, name)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("name = \"" + name + "\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(t.TempDir()) + "\"\naccess = \"rw\"\n")
	if _, err := f.rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, raw, ws.ConfigHash); err != nil {
		t.Fatal(err)
	}
	return ws.ID
}

func (f *vendorServiceFixture) start(t *testing.T, commandID string) VendorSessionView {
	t.Helper()
	view, err := f.svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: commandID, Vendor: VendorClaude, WorkspaceID: f.ws})
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func awaitSessionState(t *testing.T, sub *Subscription, sessionID, state string) EventSeq {
	t.Helper()
	for {
		event, ok := sub.Next(t.Context())
		if !ok {
			t.Fatalf("vendor-sessions stream ended before %s", state)
		}
		var change vendorSessionChange
		if err := json.Unmarshal(event.Payload, &change); err != nil {
			t.Fatal(err)
		}
		if change.SessionID == sessionID && change.State == state {
			return event.Seq
		}
	}
}

func requireCode(t *testing.T, err error, want errcode.Code) {
	t.Helper()
	if got := errcode.CodeOf(err); got != want {
		t.Fatalf("error code = %q, want %q (err: %v)", got, want, err)
	}
}

// The service drives a real adapter process through a turn, a steer, an
// interrupt and a close, publishing every state on the durable topic.
func TestVendorServiceRunsTurnsAndPublishesStates(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	sub, err := f.rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	probes, err := f.svc.Probe(t.Context(), testAppPrincipal, nil)
	if err != nil || len(probes) != 1 || probes[0].Version != fakeCLIVersion {
		t.Fatalf("probe = %+v, %v", probes, err)
	}
	session := f.start(t, "svc-start")
	created := awaitSessionState(t, sub, session.ID, "starting")
	awaitSessionState(t, sub, session.ID, "ready")
	f.driveTurns(t, sub, session.ID)
	done, err := f.svc.Close(t.Context(), testAppPrincipal, VendorStop{CommandID: "c1", SessionID: session.ID})
	if err != nil || !done {
		t.Fatalf("close = %v, %v", done, err)
	}
	awaitSessionState(t, sub, session.ID, testStateClosed)
	assertCatchUpFrom(t, f.rt, created, session.ID)
	sessions, seq, err := f.svc.List(t.Context(), testAppPrincipal)
	if err != nil || len(sessions) != 1 || sessions[0].State != testStateClosed || seq <= created {
		t.Fatalf("list = %+v at %d, %v", sessions, seq, err)
	}
}

// driveTurns runs a held turn ended by a steer, then one ended by interrupt.
func (f *vendorServiceFixture) driveTurns(t *testing.T, sub *Subscription, sessionID string) {
	t.Helper()
	in := func(id, text string) VendorInput {
		return VendorInput{CommandID: id, SessionID: sessionID, Text: text}
	}
	mustSucceed := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	mustSucceed(f.svc.Send(t.Context(), testAppPrincipal, in("s1", fakeCLIHold)))
	mustSucceed(f.svc.Steer(t.Context(), testAppPrincipal, in("st1", "go on")))
	awaitSessionState(t, sub, sessionID, "ready")
	mustSucceed(f.svc.Send(t.Context(), testAppPrincipal, in("s2", fakeCLIHold)))
	done, err := f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "i1", SessionID: sessionID})
	if err != nil || !done {
		t.Fatalf("interrupt = %v, %v", done, err)
	}
	awaitSessionState(t, sub, sessionID, "ready")
	requireCode(t, f.svc.Steer(t.Context(), testAppPrincipal, in("st2", "late")), errcode.VendorTurnNotActive)
}

func assertCatchUpFrom(t *testing.T, rt *HostRuntime, after EventSeq, sessionID string) {
	t.Helper()
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions, Cursor: &EventCursor{Topic: TopicVendorSessions, Sequence: after}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	if _, _, ok := sub.Snapshot(); ok {
		t.Fatal("catch-up from a cursor sent a snapshot")
	}
	awaitSessionState(t, sub, sessionID, testStateClosed)
}

// A close whose caller has already given up reports pending and still runs;
// the capacity cap and every typed rejection hold at the service boundary.
func TestVendorServiceLimitsAndTypedRejections(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	first := f.start(t, "lim-1")
	f.start(t, "lim-2")
	_, err := f.svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "lim-3", Vendor: VendorClaude, WorkspaceID: f.ws})
	requireCode(t, err, errcode.VendorCapacity)
	gone, cancel := context.WithCancel(t.Context())
	cancel()
	done, err := f.svc.Close(gone, testAppPrincipal, VendorStop{CommandID: "lim-close", SessionID: first.ID})
	if err != nil || done {
		t.Fatalf("close with an expired caller = %v, %v; want pending", done, err)
	}
	_, err = f.svc.Start(gone, testAppPrincipal, &StartVendorSession{CommandID: "lim-late", Vendor: VendorClaude, WorkspaceID: f.ws})
	requireCode(t, err, errcode.DeadlineExceeded)
	_, err = f.svc.Probe(t.Context(), testAppPrincipal, []string{"codex"})
	requireCode(t, err, errcode.VendorUnsupported)
	_, err = f.svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "p", Vendor: VendorClaude, WorkspaceID: f.ws, Profile: VendorProfile{Effort: "turbo"}})
	requireCode(t, err, errcode.VendorInvalidProfile)
	_, err = f.svc.Resume(t.Context(), testAppPrincipal, &ResumeVendorSession{CommandID: "r", SessionID: first.ID, Profile: VendorProfile{ResumeSessionID: "x"}})
	requireCode(t, err, errcode.VendorInvalidProfile)
	_, err = f.svc.Resume(t.Context(), testAppPrincipal, &ResumeVendorSession{CommandID: "r2", SessionID: first.ID, WorkspaceID: f.ws})
	requireCode(t, err, errcode.VendorSessionState)
	for i, text := range []string{"/fugue:solo", " /fugue:team", "\n/fugue:x", "\u3000/fugue:account"} {
		want := errcode.VendorCommandUnavailable
		if i == 2 {
			want = errcode.VendorUnknownCommand
		}
		send := VendorInput{CommandID: "fs" + strconv.Itoa(i), SessionID: first.ID, Text: text}
		requireCode(t, f.svc.Send(t.Context(), testAppPrincipal, send), want)
		steer := VendorInput{CommandID: "ft" + strconv.Itoa(i), SessionID: first.ID, Text: text}
		requireCode(t, f.svc.Steer(t.Context(), testAppPrincipal, steer), want)
	}
	requireCode(t, f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "n", SessionID: testNoSession, Text: "hi"}), errcode.VendorSessionNotFound)
	_, err = f.svc.Interrupt(t.Context(), testAppPrincipal, VendorStop{CommandID: "i", SessionID: testNoSession})
	requireCode(t, err, errcode.VendorSessionNotFound)
}

func TestVendorServiceDeniesNonLocalPrincipal(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	const session = "session"
	_, err := f.svc.Probe(t.Context(), session, nil)
	requireCode(t, err, errcode.PermissionDenied)
	_, err = f.svc.Start(t.Context(), session, &StartVendorSession{CommandID: "a", Vendor: VendorClaude})
	requireCode(t, err, errcode.PermissionDenied)
	_, err = f.svc.Resume(t.Context(), session, &ResumeVendorSession{CommandID: "b"})
	requireCode(t, err, errcode.PermissionDenied)
	requireCode(t, f.svc.Send(t.Context(), session, VendorInput{CommandID: "c"}), errcode.PermissionDenied)
	_, err = f.svc.Interrupt(t.Context(), session, VendorStop{CommandID: "d"})
	requireCode(t, err, errcode.PermissionDenied)
	_, err = f.svc.Close(t.Context(), session, VendorStop{CommandID: "e"})
	requireCode(t, err, errcode.PermissionDenied)
	_, _, err = f.svc.List(t.Context(), session)
	requireCode(t, err, errcode.PermissionDenied)
	_, err = f.rt.Subscribe(t.Context(), SubscribeParams{Principal: session, Topic: TopicVendorSessions})
	requireCode(t, err, errcode.PermissionDenied)
}

// A session records its workspace root at Start; Resume reuses it, and a
// client that names another workspace or root is refused before spawn.
func TestVendorResumeIsBoundToTheSessionWorkspace(t *testing.T) {
	t.Parallel()
	f := newVendorServiceFixture(t)
	started := f.start(t, "bind-start")
	info, err := f.svc.store.Snapshot(t.Context(), started.ID)
	if err != nil || info.WorkspaceID != f.ws || info.WorkspaceRoot != "main" {
		t.Fatalf("started session binding = %q/%q, %v; want %q/main", info.WorkspaceID, info.WorkspaceRoot, err, f.ws)
	}
	other := f.addWorkspace(t, "other-ws")
	detached := &vendorstore.Session{
		ID: "bound-session", Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
		State: vendorstore.StateDetached, ResumeHandle: "123e4567-e89b-12d3-a456-426614174000",
		WorkspaceID: f.ws, WorkspaceRoot: "main",
	}
	if err := f.svc.store.CreateSession(t.Context(), detached); err != nil {
		t.Fatal(err)
	}
	resume := func(id, workspaceID, root string) error {
		_, resumeErr := f.svc.Resume(t.Context(), testAppPrincipal, &ResumeVendorSession{CommandID: id, SessionID: detached.ID, WorkspaceID: workspaceID, RootName: root})
		return resumeErr
	}
	requireCode(t, resume("bind-other-ws", other, ""), errcode.VendorWorkspaceMismatch)
	requireCode(t, resume("bind-other-root", f.ws, "elsewhere"), errcode.VendorWorkspaceMismatch)
	if still, snapErr := f.svc.store.Snapshot(t.Context(), detached.ID); snapErr != nil || still.State != vendorstore.StateDetached || still.ProcessPID != 0 {
		t.Fatalf("refused resume changed the session: %+v, %v", still, snapErr)
	}
	if err := resume("bind-same", f.ws, "main"); err != nil {
		t.Fatalf("resume in the bound workspace = %v", err)
	}
	if resumed, snapErr := f.svc.store.Snapshot(t.Context(), detached.ID); snapErr != nil || resumed.State != vendorstore.StateReady {
		t.Fatalf("resumed session = %+v, %v; want ready", resumed, snapErr)
	}
}
