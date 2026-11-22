package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/hostapi"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/pkg/client"
)

const (
	crashServeArg   = "test-crash-serve"
	crashLeadRole   = "lead-dev"
	crashWorkerRole = "worker-dev"
)

// runCrashServeSubprocess runs a real fugue serve stack inside a test-binary
// subprocess when invoked with crashServeArg. Config is loaded via
// config.FromProcess so SYNX_FUGUE_CRASH_POINT is armed exclusively through
// internal/config.
func runCrashServeSubprocess(args []string) bool {
	if len(args) >= 2 && args[1] == bridgeCommand {
		if socket, ok := bridgeArguments(args[1:]); ok {
			os.Exit(RunBridge(context.Background(), os.Stdin, os.Stdout, config.ProcessEnv{}, socket, testVersion))
		}
	}
	if len(args) < 2 || args[1] != crashServeArg {
		return false
	}
	cfg, err := config.FromProcess()
	if err != nil {
		os.Exit(exitUsage)
	}
	exe, err := os.Executable()
	if err != nil {
		os.Exit(exitFailure)
	}
	os.Exit(serveCrashSubprocess(cfg, exe))
	return true
}

func serveCrashSubprocess(cfg config.Config, exe string) int {
	ctx := context.Background()
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(sigs)

	sock, err := hostapi.ListenSocket(ctx, hostapi.SocketOptions{
		DataDir:    cfg.DataDir,
		RuntimeDir: cfg.RuntimeDir,
	})
	if err != nil {
		return exitFailure
	}
	clk := testutil.OSClock()
	srv := hostapi.NewServer(hostapi.ServerConfig{
		Socket:  sock,
		DataDir: cfg.DataDir,
		IDs:     ids.NewUUIDv7(clk, ids.CryptoRandom{}),
		HostUID: int64(os.Getuid()),
	})
	deps := hostDeps(cfg, "test-crash", 0, clk, srv, app.VendorBridge{Binary: exe, Socket: sock.Path})
	deps.VendorBinary = exe
	hp, err := app.StartHost(ctx, deps)
	if err != nil {
		if tErr := teardownSocket(sock); tErr != nil {
			return exitFailure
		}
		return exitFailure
	}
	if err := srv.Start(ctx, hp.Runtime()); err != nil {
		if tErr := teardownHost(hp, cfg.DataDir, sock); tErr != nil {
			return exitFailure
		}
		return exitFailure
	}
	return runUntilStopped(ctx, hp, sigs)
}

type mcpConfigFile struct {
	MCPServers map[string]struct {
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env"`
	} `json:"mcpServers"`
}

func parseMCPBridgeArgs(args []string) ([]string, bool) {
	var rawJSON string
	for i := range len(args) - 1 {
		if args[i] == "--mcp-config" {
			rawJSON = args[i+1]
			break
		}
	}
	if rawJSON == "" {
		return nil, false
	}
	var cfg mcpConfigFile
	if err := json.Unmarshal([]byte(rawJSON), &cfg); err != nil {
		return nil, false
	}
	srv, ok := cfg.MCPServers["fugue"]
	if !ok {
		return nil, false
	}
	return srv.Args, true
}

func callCrashProposeViaMCPBridge(args []string) {
	bridgeArgs, ok := parseMCPBridgeArgs(args)
	if !ok {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	reqs := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"fake-claude","version":"1.0"}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"fugue_team_propose","arguments":{"title":"Crash proposal","brief":"Test pending card","tasks":[{"id":"t1","team_role":"lead","role":"lead-dev","goal":"Coordinate","deliverable":"report","write_scope":[]},{"id":"t2","parent_task_id":"t1","team_role":"member","role":"worker-dev","goal":"Implement","deliverable":"diff","write_scope":["README.md"]}]}}}`,
	}, "\n") + "\n"

	pr, pw := io.Pipe()
	cmd := exec.CommandContext(context.Background(), "sh")
	cmd.Path = exe
	cmd.Args = append([]string{exe}, bridgeArgs...)
	cmd.Stdin = pr
	stdout, pipeErr := cmd.StdoutPipe()
	if pipeErr != nil {
		return
	}
	if startErr := cmd.Start(); startErr != nil {
		return
	}
	supervise.Go(context.Background(), "test.mcp-bridge-write", func(context.Context) error {
		if _, wErr := io.WriteString(pw, reqs); wErr != nil {
			return fmt.Errorf("write mcp-bridge requests: %w", wErr)
		}
		return nil
	})
	readUntilRPCID2(stdout)
	if closeErr := pw.Close(); closeErr != nil {
		return
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		return
	}
}

func readUntilRPCID2(r io.Reader) {
	buf := make([]byte, 4096)
	var acc []byte
	for !bytes.Contains(acc, []byte(`"id":2`)) {
		n, rErr := r.Read(buf)
		if n > 0 {
			acc = append(acc, buf[:n]...)
		}
		if rErr != nil {
			break
		}
	}
}

func crashTestDirs(t *testing.T) (dataDir, fugueHome string) {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name()))
	dataDir = filepath.Join(os.TempDir(), fmt.Sprintf("fugue-cr-%x-%d", sum[:4], os.Getpid()))
	if err := os.RemoveAll(dataDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dataDir); err != nil {
			t.Logf("cleanup %s: %v", dataDir, err)
		}
	})
	cheapvault.SeedDataDir(t, dataDir)
	fugueHome = filepath.Join(t.TempDir(), "Fugue")
	return dataDir, fugueHome
}

func startCrashSubprocess(t *testing.T, ctx context.Context, dataDir, fugueHome, crashPoint string) <-chan error {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, "sh")
	cmd.Path = exe
	cmd.Args = []string{exe, crashServeArg}
	cmd.Env = append(os.Environ(),
		"HOME="+t.TempDir(),
		config.EnvDataDir+"="+dataDir,
		config.EnvFugueHome+"="+fugueHome,
		config.EnvCrashPoint+"="+crashPoint,
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start crash subprocess: %v", err)
	}
	done := make(chan error, 1)
	supervise.Go(ctx, "test.crash-wait", func(context.Context) error {
		waitErr := cmd.Wait()
		if waitErr != nil && stderr.Len() > 0 {
			done <- fmt.Errorf("%w (stderr: %s)", waitErr, strings.TrimSpace(stderr.String()))
			return nil
		}
		done <- waitErr
		return nil
	})
	return done
}

func dialCrashClient(t *testing.T, ctx context.Context, dataDir string, done <-chan error) *client.Client {
	t.Helper()
	sockPath := socketPath(dataDir)
	var c *client.Client
	testutil.Spin(t, "subprocess host ready", func() bool {
		select {
		case err := <-done:
			t.Fatalf("subprocess exited before serving: %v", err)
		default:
		}
		var err error
		c, err = client.Dial(ctx, sockPath, client.Params{
			Kind: client.KindApp, Name: "crash-test", Version: "0", APIVersions: []string{"v1"},
		})
		return err == nil
	})
	return c
}

func awaitCrashExit(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != config.CrashExitCode {
			t.Fatalf("subprocess exit = %v, want exit code %d", err, config.CrashExitCode)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for subprocess crash: %v", ctx.Err())
	}
}

func awaitCleanExit(t *testing.T, ctx context.Context, c *client.Client, done <-chan error) {
	t.Helper()
	if err := c.Shutdown(ctx, "crash-test-stop"); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	c.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("subprocess clean shutdown = %v, want nil", err)
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for clean subprocess shutdown: %v", ctx.Err())
	}
}

func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "config", "user.email", "test@example.com")
	runGit(t, dir, "config", "user.name", "Crash Test")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("seed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "README.md")
	runGit(t, dir, "commit", "-m", "initial")
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "git")
	cmd.Args = append([]string{"git"}, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func setupCrashTeamWorkspace(t *testing.T, ctx context.Context, c *client.Client, repoDir string) client.Workspace {
	t.Helper()
	ws := createWorkspaceAndAddRoot(t, ctx, c, "crash-ws", "repo", repoDir)
	tomlCfg := fmt.Sprintf(`name = %q

[session]
teams = true

[[roots]]
name = "repo"
path = %q
access = "rw"

[[roles]]
name = %q
description = "Team lead"
vendor = "claude"
permission = "write"
is_lead_eligible = true

[[roles]]
name = %q
description = "Team worker"
vendor = "claude"
permission = "write"
`, ws.Name, filepath.ToSlash(repoDir), crashLeadRole, crashWorkerRole)
	updated, err := c.UpdateWorkspaceConfig(ctx, ws.ID, []byte(tomlCfg), ws.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig: %v", err)
	}
	if updated.PendingPolicyConfirmation {
		if cErr := c.ConfirmWorkspaceConfig(ctx, ws.ID, updated.ConfigHash); cErr != nil {
			t.Fatalf("ConfirmWorkspaceConfig: %v", cErr)
		}
		updated, err = c.Workspace(ctx, ws.ID)
		if err != nil {
			t.Fatalf("Workspace after confirm: %v", err)
		}
	}
	return updated
}
