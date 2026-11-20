package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/pkg/client"
)

const fakeE2EVendorVersion = "2.1.319"

// playFakeVendor lets the cmd/fugue test binary serve as the fake Claude CLI
// for HostDeps.VendorBinary in the DA-09 end-to-end tests (Q-140 A).
func playFakeVendor(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "--version":
		if _, err := os.Stdout.WriteString(fakeE2EVendorVersion + " (Claude Code)\n"); err != nil {
			os.Exit(1)
		}
		return true
	case "-p":
		initLine := `{"type":"system","subtype":"init","session_id":"3f2504e0-4f89-41d3-9a0c-0305e82c3301","tools":["Read"],"claude_code_version":"` +
			fakeE2EVendorVersion + `","model":"fake-model","permissionMode":"default","slash_commands":["clear"],"terminal_slash_commands":[]}` + "\n"
		if _, err := os.Stdout.WriteString(initLine); err != nil {
			os.Exit(1)
		}
		scanner := bufio.NewScanner(os.Stdin)
		for scanner.Scan() {
			// Hold stdin open until the host closes or stops the session.
			continue
		}
		return true
	default:
		return false
	}
}

type e2eHost struct {
	hp        *app.HostProcess
	srv       *hostapi.Server
	fugueHome string
	client    *client.Client
}

func e2eSocketDir(t *testing.T) string {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("fugue-e2e-%x-%d", sum[:4], os.Getpid()))
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			return
		}
	})
	return dir
}

// startE2EHost starts an in-process real app stack on a real Unix socket with
// an unlocked cheap vault and the test binary wired as VendorBinary (Q-140 A).
func startE2EHost(t *testing.T) *e2eHost {
	t.Helper()
	dir := e2eSocketDir(t)
	cheapvault.SeedDataDir(t, dir)
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	clk := testutil.OSClock()
	sock, err := hostapi.ListenSocket(t.Context(), hostapi.SocketOptions{DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	srv := hostapi.NewServer(hostapi.ServerConfig{
		Socket: sock, DataDir: dir,
		IDs:     ids.NewUUIDv7(clk, ids.NewDeterministic("e2e-conn")),
		HostUID: int64(os.Getuid()),
	})
	hp, err := app.StartHost(t.Context(), &app.HostDeps{
		DataDir:      dir,
		WorkspaceEnv: config.MapEnv{config.EnvFugueHome: fugueHome},
		Config:       app.HostConfig{HostVersion: "test-e2e", APIVersions: []string{"v1"}},
		Clock:        clk,
		IDs:          ids.NewUUIDv7(clk, ids.NewDeterministic("e2e-host")),
		Transport:    srv,
		VendorBinary: exe,
	})
	if err != nil {
		t.Fatal(err)
	}
	if startErr := srv.Start(t.Context(), hp.Runtime()); startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if drainErr := srv.Drain(context.WithoutCancel(t.Context()), true); drainErr != nil {
			t.Errorf("cleanup drain: %v", drainErr)
		}
		if closeErr := hp.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	cheapvault.Unlock(t, hp.Vault())

	c, err := client.Dial(t.Context(), sock.Path, client.Params{
		Kind: client.KindApp, Name: "e2e-app", Version: "0", APIVersions: []string{"v1"},
	})
	if err != nil {
		t.Fatalf("dial host: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return &e2eHost{hp: hp, srv: srv, fugueHome: fugueHome, client: c}
}

func e2eCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func createWorkspaceAndAddRoot(t *testing.T, ctx context.Context, c *client.Client, wsName, rootName, rootDir string) client.Workspace {
	t.Helper()
	if err := c.CreateWorkspace(ctx, wsName); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	list, err := c.ListWorkspaces(ctx)
	if err != nil {
		t.Fatalf("list workspaces: %v", err)
	}
	if len(list) != 1 || list[0].Name != wsName {
		t.Fatalf("workspaces = %+v, want [%s]", list, wsName)
	}
	wsID := list[0].ID
	det, err := c.Workspace(ctx, wsID)
	if err != nil {
		t.Fatalf("read workspace: %v", err)
	}
	if det.ConfigHash == "" {
		t.Fatal("workspace detail returned empty ConfigHash")
	}
	if addErr := c.AddRoot(ctx, wsID, rootName, rootDir, client.RootAccessRW, det.ConfigHash); addErr != nil {
		t.Fatalf("add root: %v", addErr)
	}
	updated, err := c.Workspace(ctx, wsID)
	if err != nil {
		t.Fatalf("read updated workspace: %v", err)
	}
	if len(updated.Roots) != 1 || updated.Roots[0].Name != rootName || updated.ConfigHash == det.ConfigHash {
		t.Fatalf("updated workspace = %+v, want root %s and refreshed hash", updated, rootName)
	}
	return updated
}

func assertStartBlockedUnconfirmed(t *testing.T, ctx context.Context, c *client.Client, wsID string) {
	t.Helper()
	_, err := c.StartSession(ctx, "blocked-start-1", client.VendorClaude, wsID, "", client.SessionProfile{})
	if err == nil {
		t.Fatal("StartSession succeeded while policy change was unconfirmed")
	}
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("blocked StartSession connect code = %v, want FailedPrecondition (%v)", connect.CodeOf(err), err)
	}
	code, ok := client.WorkspaceCodeOf(err)
	if !ok || code != string(errcode.WorkspaceConfigUnconfirmed) {
		t.Fatalf("blocked StartSession code = %q (%v), want %q", code, ok, errcode.WorkspaceConfigUnconfirmed)
	}
}

// TestCreateWorkspaceAddRootAndStartASessionInIt is DA-09 (Q-140 A): over a
// real socket through pkg/client against an in-process host with an unlocked
// cheap vault and fake vendor binary, create a workspace, add a root, and
// start a session in that root.
func TestCreateWorkspaceAddRootAndStartASessionInIt(t *testing.T) {
	t.Parallel()
	h := startE2EHost(t)
	ctx := e2eCtx(t)

	rootDir := t.TempDir()
	ws := createWorkspaceAndAddRoot(t, ctx, h.client, "e2e-ws", "main", rootDir)

	sess, err := h.client.StartSession(ctx, "e2e-start-1", client.VendorClaude, ws.ID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("start session in root: %v", err)
	}
	if sess.ID == "" || sess.Vendor != client.VendorClaude {
		t.Fatalf("started session = %+v, want valid claude session", sess)
	}

	listed, err := h.client.ListSessions(ctx)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID != sess.ID || listed.Sessions[0].WorkspaceID != ws.ID {
		t.Fatalf("listed sessions = %+v, want session %s bound to workspace %s", listed.Sessions, sess.ID, ws.ID)
	}
}

// TestUnconfirmedPolicyChangeBlocksSessionStartUntilConfirm is DA-09 (Q-64,
// Q-140 A): an out-of-band edit touching a policy key blocks StartSession with
// workspace.config_unconfirmed until ConfirmWorkspaceConfig runs with the
// reviewed ConfigHash from pkg/client.Workspace, after which StartSession
// succeeds.
func TestUnconfirmedPolicyChangeBlocksSessionStartUntilConfirm(t *testing.T) {
	t.Parallel()
	h := startE2EHost(t)
	ctx := e2eCtx(t)

	rootDir := t.TempDir()
	ws := createWorkspaceAndAddRoot(t, ctx, h.client, "policy-ws", "main", rootDir)

	// Out-of-band edit touching policy.approval_mode on disk.
	diskPath := filepath.Join(h.fugueHome, "workspaces", ws.ID, "fugue.toml")
	pendingTOML := fmt.Sprintf("name = %q\n\n[policy]\napproval_mode = \"full\"\n\n[[roots]]\nname = \"main\"\npath = %q\naccess = \"rw\"\n",
		ws.Name, filepath.ToSlash(rootDir))
	if err := os.WriteFile(diskPath, []byte(pendingTOML), 0o600); err != nil {
		t.Fatalf("write out-of-band fugue.toml: %v", err)
	}

	pending, err := h.client.Workspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("read pending workspace: %v", err)
	}
	if !pending.PendingPolicyConfirmation || len(pending.PolicyDiff) == 0 || pending.ConfigHash == "" {
		t.Fatalf("pending workspace = %+v, want pending confirmation, diff and hash", pending)
	}

	assertStartBlockedUnconfirmed(t, ctx, h.client, ws.ID)

	if confirmErr := h.client.ConfirmWorkspaceConfig(ctx, ws.ID, pending.ConfigHash); confirmErr != nil {
		t.Fatalf("confirm policy change: %v", confirmErr)
	}
	confirmed, err := h.client.Workspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("read confirmed workspace: %v", err)
	}
	if confirmed.PendingPolicyConfirmation || len(confirmed.PolicyDiff) != 0 {
		t.Fatalf("confirmed workspace = %+v, want confirmation cleared", confirmed)
	}

	sess, err := h.client.StartSession(ctx, "allowed-start-1", client.VendorClaude, ws.ID, "", client.SessionProfile{})
	if err != nil {
		t.Fatalf("StartSession after Confirm failed: %v", err)
	}
	if sess.ID == "" {
		t.Fatal("started session has empty ID")
	}
}
