package hostconn

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/pkg/client"
)

// DA-E05 (desktop-app §6, §10; activation §3, §5): every failure UX row the
// app can act on runs against a real `fugue serve` whose vendor is a fake
// CLI — this test binary re-executed as `claude` on the serve's PATH, the
// fixture replaying the vendor's stream-json. The rows here are the probe
// inputs the window's banners render (F10.5e): a missing CLI fails the probe
// as a typed error, an untested version warns with the tested range, a
// signed-out CLI reports logged_out, a crashed host leaves sessions detached
// with resume and close as the actions, and a locked vault suspends sessions
// until the unlock. The `task unknown` row awaits teams.v1 (F11.7).

// The fake's wire constants: the tested version, an old one outside the
// tested range, and the session id every init line carries.
const (
	fakeVendorVersion    = "2.1.319"
	fakeVendorOldVersion = "1.0.5"
	fakeVendorSessionID  = "3f2504e0-4f89-41d3-9a0c-0305e82c3301"
	fakeVaultPassphrase  = "failure-ux-pass"
)

// The session states the E2E waits on, local so the package never imports
// the app window.
const (
	stateE2EReady    = "ready"
	stateE2EDetached = "detached"
	stateE2EClosed   = "closed"
	stateE2EWaiting  = "waiting"

	reasonE2EVaultLocked = "vault_locked"
)

// playFakeVendorCLI answers the vendor CLI argv shapes the host uses: the
// version probe, the login status probe, and one stream-json session.
func playFakeVendorCLI(args []string) bool {
	if len(args) < 2 {
		return false
	}
	behavior := fakeVendorBehavior()
	switch args[1] {
	case "--version":
		fakeOut(behavior.version + " (Claude Code)\n")
		return true
	case "auth":
		if behavior.loggedIn {
			fakeOut(`{"loggedIn":true}` + "\n")
		} else {
			fakeOut(`{"loggedIn":false}` + "\n")
		}
		return true
	case "-p":
		runFakeVendorTurn()
		return true
	}
	return false
}

// fakeVendorAnswers is what the fake CLI reports: its version and whether
// the account is signed in.
type fakeVendorAnswers struct {
	version  string
	loggedIn bool
}

// fakeVendorBehavior reads the fake's answers from the directory the test
// linked it into: f105e-<version>-in names the tested signed-in fake,
// f105e-<version>-out the signed-out one. No shared file, no environment:
// the spawned fake inherits the serve's filtered env only.
func fakeVendorBehavior() fakeVendorAnswers {
	base := filepath.Base(filepath.Dir(os.Args[0]))
	rest := strings.TrimPrefix(base, "f105e-")
	i := strings.LastIndex(rest, "-")
	if i <= 0 {
		return fakeVendorAnswers{version: fakeVendorVersion, loggedIn: true}
	}
	return fakeVendorAnswers{version: rest[:i], loggedIn: rest[i+1:] != "out"}
}

// runFakeVendorTurn plays one stream-json session: the init line, then an
// assistant message and a result for every user line, so turns end and the
// session lands ready.
func runFakeVendorTurn() {
	behavior := fakeVendorBehavior()
	fakeOut(`{"type":"system","subtype":"init","session_id":"` + fakeVendorSessionID +
		`","tools":["Read"],"claude_code_version":"` + behavior.version +
		`","model":"fake-model","permissionMode":"default","slash_commands":["clear","terminal","compact"],"terminal_slash_commands":["terminal"]}` + "\n")
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		if !strings.Contains(scanner.Text(), `"type":"user"`) {
			continue
		}
		fakeOut(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"reply"}]},"session_id":"` +
			fakeVendorSessionID + `"}` + "\n")
		fakeOut(`{"type":"result","subtype":"success","is_error":false,"result":"done","session_id":"` +
			fakeVendorSessionID + `"}` + "\n")
	}
}

func fakeOut(s string) {
	if _, err := os.Stdout.WriteString(s); err != nil {
		os.Exit(1)
	}
}

// fakeVendorDir builds the fake CLI's directory: the test binary linked as
// `claude`, its behavior encoded in the parent directory's name.
func fakeVendorDir(t *testing.T, version string, loggedIn bool) string {
	t.Helper()
	name := "f105e-" + version + "-in"
	if !loggedIn {
		name = "f105e-" + version + "-out"
	}
	dir := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, filepath.Join(dir, "claude")); err != nil {
		t.Fatal(err)
	}
	return dir
}

// startServeWithFakeVendor serves a fresh data directory whose PATH holds
// only the fake dir; the missing-CLI row passes an empty directory instead.
func startServeWithFakeVendor(t *testing.T, ctx context.Context, bin, fakeDir string) *testHost {
	t.Helper()
	base := shortDir(t)
	dataDir := filepath.Join(base, "data")
	runtime := filepath.Join(base, "rt")
	for _, dir := range []string{dataDir, runtime} {
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return serveWithFakeVendorOn(t, ctx, bin, dataDir, runtime, fakeDir)
}

// restartServeWithFakeVendor restarts a stopped host on its own data and
// runtime directories with the fake still first on PATH: restartServe serves
// with the ambient PATH, and the restarted host must resolve the same fake
// the first one did.
func (h *testHost) restartServeWithFakeVendor(t *testing.T, ctx context.Context, bin, fakeDir string) *testHost {
	t.Helper()
	return serveWithFakeVendorOn(t, ctx, bin, h.dataDir, h.runtime, fakeDir)
}

// serveWithFakeVendorOn runs `fugue serve` on the given directories with the
// fake dir as its whole PATH: the serve resolves the vendor CLI on PATH
// alone, and nothing else on it may shadow the fake.
func serveWithFakeVendorOn(t *testing.T, ctx context.Context, bin, dataDir, runtime, fakeDir string) *testHost {
	t.Helper()
	cmd := exec.CommandContext(ctx, bin, "serve")
	cmd.Env = append(os.Environ(),
		"SYNX_FUGUE_DATA_DIR="+dataDir,
		"XDG_RUNTIME_DIR="+runtime,
		"PATH="+fakeDir,
	)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cmd.ProcessState == nil {
			if kerr := cmd.Process.Kill(); kerr != nil {
				return
			}
		}
		// Wait reaps the process and ends exec's watch goroutine, even after
		// a kill.
		if werr := cmd.Wait(); werr != nil {
			return
		}
	})
	return &testHost{cmd: cmd, dataDir: dataDir, runtime: runtime}
}

// killServe simulates a host crash: SIGKILL, no drain, no stop proof, the
// exact precondition the detached row names.
func (h *testHost) killServe(t *testing.T) {
	t.Helper()
	if h.cmd.ProcessState != nil {
		return
	}
	if err := h.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the host: %v", err)
	}
	// The exit status is the kill signal itself; reaping matters, the
	// verdict does not.
	if err := h.cmd.Wait(); err != nil {
		t.Logf("the killed host exited: %v", err)
	}
}

// e2eNow reads the wall clock through the port, the only wall-clock reader
// the real-host waits use; every wait stays deadline-bounded.
func e2eNow() time.Time { return RealClock{}.Now() }

// e2eTimer arms one poll interval through the port.
func e2eTimer() Timer { return RealClock{}.NewTimer(pollTick) }

// pollTick is one poll interval of the real-host waits.
const pollTick = 20 * time.Millisecond

// waitHostReady polls the discovery file until the booting serve has written
// it: startServe returns at once, the manager's retry loop is what normally
// waits for readiness (DA-E04), and a direct dial must wait here instead.
func waitHostReady(t *testing.T, ctx context.Context, h *testHost) string {
	t.Helper()
	deadline := e2eNow().Add(45 * time.Second)
	for e2eNow().Before(deadline) {
		if hf, err := client.ReadHostFile(h.dataDir); err == nil {
			return hf.SocketPath
		}
		timer := e2eTimer()
		select {
		case <-ctx.Done():
			t.Fatalf("the host never became ready before the deadline: %v", ctx.Err())
		case <-timer.C():
		}
	}
	t.Fatal("the host never became ready in 45 s")
	return ""
}

// dialApp dials the host as the app does: it waits for the discovery file,
// then retries the dial until the restarted host has bound the socket, all
// bounded by the test's deadline.
func dialApp(t *testing.T, ctx context.Context, h *testHost) *client.Client {
	t.Helper()
	socket := waitHostReady(t, ctx, h)
	deadline := e2eNow().Add(45 * time.Second)
	for {
		c, err := client.Dial(ctx, socket, client.Params{
			Kind: client.KindApp, Name: "fugue-e05", Version: "0", APIVersions: []string{"v1"},
		})
		if err == nil {
			t.Cleanup(func() { c.Close() })
			return c
		}
		if e2eNow().After(deadline) {
			t.Fatalf("dial the host: %v", err)
		}
		timer := e2eTimer()
		select {
		case <-ctx.Done():
			t.Fatalf("dial the host before the deadline: %v", ctx.Err())
		case <-timer.C():
		}
	}
}

// unlockAndSeedWorkspace sets the vault up, unlocks it, and creates a
// workspace with one usable root, the least a session start needs. It
// returns the workspace id.
func unlockAndSeedWorkspace(t *testing.T, ctx context.Context, c *client.Client) string {
	t.Helper()
	// A fresh slice per call: the client clears the passphrase bytes it
	// sends, so reusing one slice would unlock with an emptied key.
	if _, err := c.SetupVault(ctx, []byte(fakeVaultPassphrase)); err != nil {
		t.Fatalf("set the vault up: %v", err)
	}
	if _, err := c.UnlockVault(ctx, []byte(fakeVaultPassphrase)); err != nil {
		t.Fatalf("unlock the vault: %v", err)
	}
	if err := c.CreateWorkspace(ctx, "failureux"); err != nil {
		t.Fatalf("create the workspace: %v", err)
	}
	list, err := c.ListWorkspaces(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	for _, w := range list {
		if w.Name == "failureux" {
			id = w.ID
		}
	}
	if id == "" {
		t.Fatalf("workspace list = %+v, want failureux", list)
	}
	ws, err := c.Workspace(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AddRoot(ctx, id, "root", t.TempDir(), "rw", ws.ConfigHash); err != nil {
		t.Fatalf("add the root: %v", err)
	}
	return id
}

// waitSessionState polls the session list until the session reports the
// wanted state, bounded by the test's deadline. The states arrive on the
// topic asynchronously; the list read is the same read the sidebar makes.
func waitSessionState(t *testing.T, ctx context.Context, c *client.Client, id, want string) {
	t.Helper()
	deadline := e2eNow().Add(45 * time.Second)
	var last client.Session
	for e2eNow().Before(deadline) {
		s, ok := currentSession(t, ctx, c, id)
		if ok {
			last = s
			if s.State == want {
				return
			}
		}
		timer := e2eTimer()
		select {
		case <-ctx.Done():
			t.Fatalf("session %s never reached %q before the deadline: %v (last %+v)", id, want, ctx.Err(), last)
		case <-timer.C():
		}
	}
	t.Fatalf("session %s never reached %q in 45 s (last %+v)", id, want, last)
}

// currentSession reads one session off the list read.
func currentSession(t *testing.T, ctx context.Context, c *client.Client, id string) (client.Session, bool) {
	t.Helper()
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list the sessions: %v", err)
	}
	for _, s := range list.Sessions {
		if s.ID == id {
			return s, true
		}
	}
	return client.Session{}, false
}

// TestFailureUXMissingVendorCLIProbesAsTypedError is the missing-CLI row: a
// serve whose PATH holds no vendor CLI answers the probe with the typed
// public error the window's danger banner renders, install guidance and all.
func TestFailureUXMissingVendorCLIProbesAsTypedError(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	host := startServeWithFakeVendor(t, ctx, bin, t.TempDir())
	c := dialApp(t, ctx, host)
	if _, err := c.ProbeVendors(ctx); err == nil {
		t.Fatal("the probe answered without any vendor CLI on the PATH")
	} else if !strings.Contains(err.Error(), "not_found") {
		t.Fatalf("probe error = %v, want the typed not-found answer", err)
	}
}

// TestFailureUXUnsupportedVersionAndLoggedOutProbeOverRealHost is the
// unsupported-version and needs-login rows: the fake reports an old version
// and a signed-out CLI, and the probe carries both verbatim — the version
// outside the tested range with the range warning, the login logged_out.
func TestFailureUXUnsupportedVersionAndLoggedOutProbeOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	host := startServeWithFakeVendor(t, ctx, bin, fakeVendorDir(t, fakeVendorOldVersion, false))
	c := dialApp(t, ctx, host)
	probes, err := c.ProbeVendors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %+v, want one", probes)
	}
	p := probes[0]
	if p.Version != fakeVendorOldVersion || p.VersionTested || p.VersionWarning == "" {
		t.Fatalf("probe = %+v, want the old version untested with a range warning", p)
	}
	if p.Login != client.AccountLoginLoggedOut {
		t.Fatalf("login = %q, want logged_out", p.Login)
	}
}

// TestFailureUXDetachedSessionResumesAndClosesOverRealHost is the detached
// row: a live session survives a crashed host as detached, the resume brings
// it back to ready on its own handle, and the close ends it for good.
func TestFailureUXDetachedSessionResumesAndClosesOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	fakeDir := fakeVendorDir(t, fakeVendorVersion, true)
	host := startServeWithFakeVendor(t, ctx, bin, fakeDir)
	c := dialApp(t, ctx, host)
	wsID := unlockAndSeedWorkspace(t, ctx, c)
	started, err := c.StartSession(ctx, "e05-detach-start", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("start the session: %v", err)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EReady)

	// The crash: SIGKILL, no stop proof, recovery marks the session detached
	// on the restarted host.
	host.killServe(t)
	host = host.restartServeWithFakeVendor(t, ctx, bin, fakeDir)
	c2 := dialApp(t, ctx, host)
	waitSessionState(t, ctx, c2, started.ID, stateE2EDetached)

	// The restarted host locked its vault again (the invariant), so the
	// resume the detached banner offers needs the unlock first — the §3
	// order: open the vault, then resume.
	if _, unlockErr := c2.UnlockVault(ctx, []byte(fakeVaultPassphrase)); unlockErr != nil {
		t.Fatalf("unlock the restarted host's vault: %v", unlockErr)
	}
	if _, resumeErr := c2.ResumeSession(ctx, "e05-detach-resume", started.ID, "", "", client.SessionProfile{}); resumeErr != nil {
		t.Fatalf("resume the session: %v", resumeErr)
	}
	waitSessionState(t, ctx, c2, started.ID, stateE2EReady)
	done, err := c2.CloseSession(ctx, "e05-detach-close", started.ID)
	if err != nil {
		t.Fatalf("close the session: %v", err)
	}
	if !done {
		t.Fatal("the close missed its deadline")
	}
	waitSessionState(t, ctx, c2, started.ID, stateE2EClosed)
}

// TestFailureUXVaultLockSuspendsAndResumesSessionOverRealHost is the
// vault_locked row: locking suspends a ready session behind vault_locked,
// the state the vault gate counts, and unlocking brings it back to ready.
func TestFailureUXVaultLockSuspendsAndResumesSessionOverRealHost(t *testing.T) {
	t.Parallel()
	bin := buildFugue(t)
	ctx := testCtx(t)
	fakeDir := fakeVendorDir(t, fakeVendorVersion, true)
	host := startServeWithFakeVendor(t, ctx, bin, fakeDir)
	c := dialApp(t, ctx, host)
	wsID := unlockAndSeedWorkspace(t, ctx, c)
	started, err := c.StartSession(ctx, "e05-vault-start", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("start the session: %v", err)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EReady)

	if _, err := c.LockVault(ctx); err != nil {
		t.Fatalf("lock the vault: %v", err)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EWaiting)
	s, ok := currentSession(t, ctx, c, started.ID)
	if !ok || s.WaitingReason != reasonE2EVaultLocked {
		t.Fatalf("waiting session = %+v, want the vault_locked reason", s)
	}

	if _, err := c.UnlockVault(ctx, []byte(fakeVaultPassphrase)); err != nil {
		t.Fatalf("unlock the vault: %v", err)
	}
	waitSessionState(t, ctx, c, started.ID, stateE2EReady)
}
