//go:build darwin

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSeatbeltBackendUnitAndSmoke(t *testing.T) {
	t.Parallel()

	t.Run("unit backend start and piped", func(t *testing.T) {
		t.Parallel()
		assertMissingSeatbeltBackend(t)
		assertFakeSeatbeltBackend(t)
	})
	t.Run("live seatbelt smoke", func(t *testing.T) {
		t.Parallel()
		assertSeatbeltLiveSmoke(t)
	})
}

func assertMissingSeatbeltBackend(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if DefaultRunner() == nil || NewSeatbeltBackend().Name() != "seatbelt" {
		t.Fatal("unexpected default runner or backend name")
	}

	missing := &SeatbeltBackend{
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}
	if !errors.Is(missing.Available(ctx), ErrUnavailable) {
		t.Fatal("missing sandbox-exec must return ErrUnavailable")
	}
	if _, err := missing.Start(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testBinTrue}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing Start = %v, want ErrUnavailable", err)
	}
	if _, err := missing.StartPiped(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testBinTrue}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing StartPiped = %v, want ErrUnavailable", err)
	}
	if _, err := missing.Start(ctx, &Policy{WorkDir: testRelPath}, Cmd{Path: testBinTrue}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid Start = %v, want ErrInvalidPolicy", err)
	}
	if _, err := missing.StartPiped(ctx, &Policy{WorkDir: testRelPath}, Cmd{Path: testBinTrue}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid StartPiped = %v, want ErrInvalidPolicy", err)
	}
}

func assertFakeSeatbeltBackend(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeBackend := &SeatbeltBackend{
		lookPath: func(string) (string, error) {
			return exe, nil
		},
		gate: &unixProfileGate{runner: &fakeProfileRunner{}},
	}
	if availErr := fakeBackend.Available(ctx); availErr != nil {
		t.Fatalf("fake Available: %v", availErr)
	}
	proc, err := fakeBackend.Start(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: "ok", Args: []string{fakeSandboxExecFlag}})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
	out, _ := proc.Output()
	if string(out) != "stdout-ok" {
		t.Fatalf("stdout = %q, want stdout-ok", out)
	}

	pipedProc, err := fakeBackend.StartPiped(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testEchoStdin, Args: []string{fakeSandboxExecFlag}})
	if err != nil {
		t.Fatal(err)
	}
	if _, writeErr := io.WriteString(pipedProc.StdinWriter(), "seatbelt-piped"); writeErr != nil {
		t.Fatal(writeErr)
	}
	closePipeQuietly(pipedProc.StdinWriter())
	pipedOut, err := io.ReadAll(pipedProc.StdoutReader())
	if err != nil || string(pipedOut) != "seatbelt-piped" {
		t.Fatalf("piped stdout = %q (%v), want seatbelt-piped", pipedOut, err)
	}
	if waitErr := pipedProc.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
}

func canRunHostSeatbelt(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "/usr/bin/sandbox-exec")
	cmd.Path = "/usr/bin/sandbox-exec"
	cmd.Args = []string{"/usr/bin/sandbox-exec", "-p", "(version 1)(allow default)", "--", "/usr/bin/true"}
	return cmd.Run() == nil
}

type seatbeltSmokeFixture struct {
	policy     Policy
	root       string
	home       string
	codexDir   string
	configFile string
}

func prepareSeatbeltSmokeFixture(t *testing.T) seatbeltSmokeFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "home")
	sshDir := filepath.Join(home, ".ssh")
	codexDir := filepath.Join(home, "codex")
	rulesDir := filepath.Join(codexDir, "rules")
	for _, dir := range []string{sshDir, rulesDir} {
		if mkdirErr := os.MkdirAll(dir, 0o700); mkdirErr != nil {
			t.Fatal(mkdirErr)
		}
	}
	authKeys := filepath.Join(sshDir, "authorized_keys")
	if writeErr := os.WriteFile(authKeys, []byte("SECRET_SSH"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	configFile := filepath.Join(codexDir, "config.toml")
	if writeErr := os.WriteFile(configFile, []byte("# pinned\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	return seatbeltSmokeFixture{
		policy: Policy{
			WorkDir:        root,
			ReadWritePaths: []string{codexDir},
			DenyWritePaths: []string{rulesDir, configFile},
			DenyReadPaths:  []string{sshDir},
			Env:            map[string]string{testEnvHome: home},
			PrivateHome:    boolPtr(false),
			Network:        NetworkNone,
		},
		root:       root,
		home:       home,
		codexDir:   codexDir,
		configFile: configFile,
	}
}

func assertSeatbeltLiveSmoke(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if !canRunHostSeatbelt(ctx) {
		return
	}
	backend := NewSeatbeltBackend()
	fx := prepareSeatbeltSmokeFixture(t)
	movedHome := filepath.Join(fx.root, "home-moved")
	script := strings.Join([]string{
		`cat "$HOME/.ssh/authorized_keys" 2>/dev/null || true`,
		`! printf pwned >> "$HOME/.ssh/authorized_keys" 2>/dev/null`,
		`! mv "` + fx.home + `" "` + movedHome + `" 2>/dev/null`,
		`! printf x >> "` + fx.configFile + `" 2>/dev/null`,
		`! mv "` + fx.codexDir + `" "` + fx.codexDir + `.moved" 2>/dev/null`,
		`printf ok > "` + filepath.Join(fx.home, "notes.txt") + `"`,
	}, "; ")

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	proc, err := backend.Start(runCtx, &fx.policy, Cmd{Path: testBinSh, Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		stdout, stderr := proc.Output()
		t.Fatalf("live seatbelt shell failed: %v (stdout=%q stderr=%q)", waitErr, stdout, stderr)
	}
	stdout, stderr := proc.Output()
	if bytes.Contains(stdout, []byte("SECRET_SSH")) || bytes.Contains(stderr, []byte("SECRET_SSH")) {
		t.Fatalf("ssh secret leaked under seatbelt: stdout=%q stderr=%q", stdout, stderr)
	}
	assertSeatbeltLoopbackListenersSmoke(t, ctx, backend)
}

func assertSeatbeltLoopbackListenersSmoke(t *testing.T, ctx context.Context, backend *SeatbeltBackend) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	proc, err := backend.Start(runCtx, &Policy{
		WorkDir:                t.TempDir(),
		Network:                NetworkHost,
		AllowLoopbackListeners: true,
		ReadOnlyPaths:          []string{exe},
	}, Cmd{Path: exe, Args: []string{loopbackListenerFlag}})
	if err != nil {
		t.Fatal(err)
	}
	waitErr := proc.Wait()
	stdout, stderr := proc.Output()
	if waitErr != nil || !strings.Contains(string(stdout), "LOOPBACK_LISTENERS_OK") {
		t.Fatalf("seatbelt loopback listener failed: %v\nstdout=%s\nstderr=%s", waitErr, stdout, stderr)
	}
}
