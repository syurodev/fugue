package app

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
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
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.Real{}
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: t.TempDir(), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("vendor-svc")),
		Transport: newScriptedTransport(), VendorBinary: exe,
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
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, "svc-ws")
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("name = \"svc-ws\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(t.TempDir()) + "\"\naccess = \"rw\"\n")
	if _, err := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, raw, ws.ConfigHash); err != nil {
		t.Fatal(err)
	}
	return &vendorServiceFixture{svc: svc, rt: rt, ws: ws.ID}
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
	requireCode(t, f.svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "f", SessionID: first.ID, Text: "/fugue:solo"}), errcode.VendorUnknownCommand)
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
