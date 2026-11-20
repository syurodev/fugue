package app

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// teamProposalFixture is the F11.2a fixture: a host with the fake vendor CLI,
// a workspace whose catalog carries one read role, and a live Claude session
// holding a turn, so the propose path runs against a real actor.
type teamProposalFixture struct {
	hp *HostProcess
	rt *HostRuntime
	// clk is the fixture's fake clock; the wake tests advance it.
	clk     *clock.Fake
	svc     *VendorService
	tools   *SessionToolService
	session VendorSessionView
	binding SessionBinding
	wsID    string
	// root is the workspace root directory of the fixture.
	root string
}

// newTeamProposalFixture is the decide-path fixture: the team start path is
// off, so an approved team stays queued (Q-98).
func newTeamProposalFixture(t *testing.T, extraTOML string) *teamProposalFixture {
	t.Helper()
	return newTeamFixture(t, extraTOML, true)
}

// newTeamFixture builds the fixture; noDispatch keeps approved teams queued.
func newTeamFixture(t *testing.T, extraTOML string, noDispatch bool) *teamProposalFixture {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := clock.NewFake(time.Unix(200, 0))
	hp, err := StartHost(t.Context(), &HostDeps{
		DataDir: cheapVaultDataDir(t), WorkspaceEnv: config.MapEnv{config.EnvFugueHome: filepath.Join(t.TempDir(), "Fugue")},
		Config: testHostConfig(), Clock: clk, IDs: ids.NewUUIDv7(clk, ids.NewDeterministic("team-test")),
		Transport: newScriptedTransport(), VendorBinary: exe,
		Bridge: VendorBridge{Binary: exe, Socket: filepath.Join(t.TempDir(), "host.sock")}, TeamAdmitter: admitterFor(noDispatch),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// process.Stop waits on HostDeps.Clock for pipes/interrupts; advance
		// the fake clock while Close runs so it cannot deadlock on a frozen clock.
		done := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			testutil.Spin(t, "close host", func() bool {
				select {
				case <-done:
					return true
				default:
					clk.Advance(time.Millisecond)
					return false
				}
			})
		})
		defer func() {
			close(done)
			wg.Wait()
		}()
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	unlockCheapVault(t, hp)
	rt := hp.Runtime()
	svc, err := rt.VendorService()
	if err != nil {
		t.Fatal(err)
	}
	tools, err := rt.SessionTools()
	if err != nil {
		t.Fatal(err)
	}
	ws, err := rt.CreateWorkspace(t.Context(), testAppPrincipal, teamTestWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if _, cfgErr := rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, ws.ID, teamWorkspaceTOMLAt(t, extraTOML, root), ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
	session, binding := startTeamFixtureSession(t, rt, svc, tools, ws.ID)
	return &teamProposalFixture{hp: hp, rt: rt, svc: svc, tools: tools, session: session, binding: binding, wsID: ws.ID, root: root, clk: clk}
}

// startTeamFixtureSession starts one Claude session in the workspace and
// holds its turn, so the propose path runs against a live actor.
func startTeamFixtureSession(t *testing.T, rt *HostRuntime, svc *VendorService, tools *SessionToolService, workspaceID string) (VendorSessionView, SessionBinding) {
	t.Helper()
	sub, err := rt.Subscribe(t.Context(), SubscribeParams{Principal: testAppPrincipal, Topic: TopicVendorSessions})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	session, err := svc.Start(t.Context(), testAppPrincipal, &StartVendorSession{CommandID: "team-start", Vendor: VendorClaude, WorkspaceID: workspaceID})
	if err != nil {
		t.Fatal(err)
	}
	awaitSessionState(t, sub, session.ID, "ready")
	if sendErr := svc.Send(t.Context(), testAppPrincipal, VendorInput{CommandID: "team-turn", SessionID: session.ID, Text: fakeCLIHold}); sendErr != nil {
		t.Fatal(sendErr)
	}
	tools.tokens.mu.Lock()
	binding, ok := tools.tokens.bySession[session.ID]
	tools.tokens.mu.Unlock()
	if !ok {
		t.Fatalf("missing live session binding for %s", session.ID)
	}
	return session, binding
}

func teamWorkspaceTOML(t *testing.T, extra string) []byte {
	t.Helper()
	return teamWorkspaceTOMLAt(t, extra, t.TempDir())
}

func teamWorkspaceTOMLAt(t *testing.T, extra, dir string) []byte {
	t.Helper()
	root := filepath.ToSlash(dir)
	raw := "name = \"" + teamTestWorkspace + "\"\n" + extra +
		"[[roots]]\nname = \"main\"\npath = \"" + root + "\"\naccess = \"rw\"\n" +
		"[[roles]]\nname = \"" + teamTestRole + "\"\nvendor = \"claude\"\npermission = \"read\"\n"
	return []byte(raw)
}

// admitNothing is the Admitter of the decide-path tests: it admits no task,
// so an approved team stays queued (Q-98).
type admitNothing struct{}

func (admitNothing) Admit(context.Context, *vendorstore.TaskRecord, []vendorstore.TaskRecord) (bool, error) {
	return false, nil
}

// admitterFor returns the nothing-admitter for the decide-path fixtures and
// nil, the host's own, otherwise.
func admitterFor(noDispatch bool) Admitter {
	if noDispatch {
		return admitNothing{}
	}
	return nil
}

// setExtraTOML reconfigures the fixture's workspace with extraTOML so a test
// can check another workspace flag without starting a second host process.
func (f *teamProposalFixture) setExtraTOML(t *testing.T, extraTOML string) {
	t.Helper()
	ws, _, err := f.rt.GetWorkspace(t.Context(), testAppPrincipal, f.wsID)
	if err != nil {
		t.Fatal(err)
	}
	if _, cfgErr := f.rt.UpdateWorkspaceConfig(t.Context(), testAppPrincipal, f.wsID, teamWorkspaceTOMLAt(t, extraTOML, f.root), ws.ConfigHash); cfgErr != nil {
		t.Fatal(cfgErr)
	}
}
