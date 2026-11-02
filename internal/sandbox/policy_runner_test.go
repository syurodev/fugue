package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestPolicyValidationAndDenyWriteRules(t *testing.T) {
	t.Parallel()

	t.Run("invalid policies and deny paths", func(t *testing.T) {
		t.Parallel()
		assertInvalidPoliciesRejected(t)
	})
	t.Run("deny-write invariants and socket punching", func(t *testing.T) {
		t.Parallel()
		assertDenyWriteAndSocketValidation(t)
	})
	t.Run("chdir, home and environment helpers", func(t *testing.T) {
		t.Parallel()
		assertPolicyHelpers(t)
	})
}

func assertInvalidPoliciesRejected(t *testing.T) {
	t.Helper()
	var nilPolicy *Policy
	if err := nilPolicy.Validate(); !errors.Is(err, ErrInvalidPolicy) || errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("nil Validate = %v, want ErrInvalidPolicy", err)
	}
	if nilPolicy.EffectiveDenyRead() != nil || nilPolicy.EffectiveChdir(Cmd{}) != "" || !nilPolicy.EffectivePrivateHome() {
		t.Fatal("nil Policy accessors must return safe defaults")
	}

	dir := t.TempDir()
	badPolicies := []Policy{
		{WorkDir: testRelativePath},
		{WorkDir: dir, Network: NetworkMode("unknown")},
		{WorkDir: dir, SessionTempDir: "/tmp/not-private"},
		{WorkDir: dir, SessionTempDir: "/private/tmp/not-dk"},
		{WorkDir: dir, AllowLoopbackListeners: true, Network: NetworkNone},
		{WorkDir: dir, AllowUnixSockets: []string{"rel.sock"}},
		{WorkDir: dir, ListenLoopbackPorts: []int{0}},
		{WorkDir: dir, ListenLoopbackPorts: []int{70000}},
		{WorkDir: dir, DenyWritePaths: []string{testRelativePath}},
	}
	for i := range badPolicies {
		if err := badPolicies[i].Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("case %d: Validate = %v, want ErrInvalidPolicy", i, err)
		}
	}
	p := Policy{WorkDir: dir}
	if err := p.ValidateCmd(Cmd{Path: ""}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("empty Cmd.Path: ValidateCmd = %v, want ErrInvalidPolicy", err)
	}
}

func assertDenyWriteAndSocketValidation(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	secretDir := filepath.Join(root, "secret")
	rulesDir := filepath.Join(root, "codex", "rules")
	for _, dir := range []string{workDir, secretDir, rulesDir} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	secretLink := filepath.Join(workDir, "secret-link")
	if err := os.Symlink(secretDir, secretLink); err != nil {
		t.Fatal(err)
	}
	rulesLink := filepath.Join(root, "codex", "rules-link")
	if err := os.Symlink(rulesDir, rulesLink); err != nil {
		t.Fatal(err)
	}

	symlinkRO := Policy{WorkDir: workDir, ReadOnlyPaths: []string{secretLink}, DenyReadPaths: []string{secretDir}}
	if err := symlinkRO.Validate(); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("symlink to denied path: Validate = %v, want ErrInvalidPolicy", err)
	}

	denyWriteCases := []Policy{
		{WorkDir: workDir, DenyWritePaths: []string{filepath.Join(root, "missing")}},
		{WorkDir: workDir, DenyWritePaths: []string{rulesLink}},
		{WorkDir: workDir, DenyWritePaths: []string{rulesDir}, ReadWritePaths: []string{filepath.Join(rulesDir, "sub")}},
		{WorkDir: rulesDir, DenyWritePaths: []string{rulesDir}},
	}
	for i := range denyWriteCases {
		if err := denyWriteCases[i].Validate(); !errors.Is(err, ErrInvalidPolicy) {
			t.Fatalf("deny-write case %d: Validate = %v, want ErrInvalidPolicy", i, err)
		}
	}

	sock := filepath.Join(secretDir, "mcp.sock")
	if err := os.WriteFile(sock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	validPolicy := Policy{
		WorkDir:          workDir,
		ReadWritePaths:   []string{filepath.Join(root, "codex")},
		DenyWritePaths:   []string{rulesDir},
		DenyReadPaths:    []string{secretDir},
		AllowUnixSockets: []string{sock},
	}
	if err := validPolicy.Validate(); err != nil {
		t.Fatalf("valid policy failed Validate: %v", err)
	}
}

func assertPolicyHelpers(t *testing.T) {
	t.Helper()
	workDir := t.TempDir()
	subDir := filepath.Join(workDir, "sub")
	fakeHome := filepath.Join(workDir, "fake-home")
	p := Policy{
		WorkDir: workDir,
		Env:     map[string]string{testEnvHome: fakeHome},
	}
	if got := p.EffectiveChdir(Cmd{Path: "sh", Dir: subDir}); got != subDir {
		t.Fatalf("EffectiveChdir(subDir) = %q, want %q", got, subDir)
	}
	if got := p.EffectiveChdir(Cmd{Path: "sh", Dir: "/outside"}); got != workDir {
		t.Fatalf("EffectiveChdir(outside) = %q, want %q", got, workDir)
	}
	denies := p.effectiveDenyRead("/Users/host")
	if !slices.Contains(denies, "/Users/host/.ssh") || !slices.Contains(denies, filepath.Join(fakeHome, ".ssh")) {
		t.Fatalf("effectiveDenyRead missing host or policy home .ssh: %v", denies)
	}
	if seatbeltProcessEnv(&Policy{WorkDir: workDir, PrivateHome: boolPtr(false)}, "/Users/host") != nil {
		t.Fatal("shared home with nil Env must leave seatbeltProcessEnv nil")
	}
	env := seatbeltProcessEnv(&p, "/Users/host")
	if !slices.Contains(env, "HOME="+fakeHome) || !slices.Contains(env, "TMPDIR=/tmp") {
		t.Fatalf("seatbeltProcessEnv = %v, missing HOME or TMPDIR", env)
	}
}

type fakeBackend struct {
	available  error
	start      func(ctx context.Context, p *Policy, c Cmd) (*Proc, error)
	startPiped func(ctx context.Context, p *Policy, c Cmd) (*Proc, error)
}

func (*fakeBackend) Name() string { return "fake" }

func (f *fakeBackend) Available(context.Context) error { return f.available }

func (f *fakeBackend) Start(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	return f.start(ctx, p, c)
}

func (f *fakeBackend) StartPiped(ctx context.Context, p *Policy, c Cmd) (*Proc, error) {
	return f.startPiped(ctx, p, c)
}

func TestRunnerAndProcLifecycle(t *testing.T) {
	t.Parallel()

	t.Run("runner validation and delegation", func(t *testing.T) {
		t.Parallel()
		assertNilBackendRunner(t)
		assertFakeBackendRunner(t)
	})
	t.Run("proc output, piped io and concurrent wait", func(t *testing.T) {
		t.Parallel()
		assertProcLifecycle(t)
	})
}

func assertNilBackendRunner(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	nilRunner := NewRunner(nil)
	if !errors.Is(nilRunner.Available(ctx), ErrUnavailable) {
		t.Fatal("nil backend Available must return ErrUnavailable")
	}
	if _, err := nilRunner.Start(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testCmdTrue}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil backend Start = %v, want ErrUnavailable", err)
	}
	if _, err := nilRunner.StartPiped(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testCmdTrue}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil backend StartPiped = %v, want ErrUnavailable", err)
	}
}

func assertFakeBackendRunner(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	var saw Policy
	fake := &fakeBackend{
		start: func(ctx context.Context, p *Policy, _ Cmd) (*Proc, error) {
			saw = *p
			exe, err := os.Executable()
			if err != nil {
				return nil, fmt.Errorf("executable: %w", err)
			}
			cmd := exec.CommandContext(ctx, testBinSh)
			cmd.Path = exe
			cmd.Args = []string{exe, fakeSandboxExecFlag, "--", "ok"}
			stdout := &bytes.Buffer{}
			stderr := &bytes.Buffer{}
			proc := NewProcWithBuffers(cmd, stdout, stderr)
			if startErr := cmd.Start(); startErr != nil {
				return nil, fmt.Errorf("start: %w", startErr)
			}
			return proc, nil
		},
		startPiped: func(_ context.Context, _ *Policy, _ Cmd) (*Proc, error) {
			return nil, ErrUnavailable
		},
	}
	r := NewRunner(fake)
	if err := r.Available(ctx); err != nil {
		t.Fatalf("Available: %v", err)
	}
	if _, err := r.Start(ctx, &Policy{WorkDir: testRelPath}, Cmd{Path: testCmdTrue}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid policy Start = %v, want ErrInvalidPolicy", err)
	}
	proc, err := r.Start(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: testCmdTrue})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
	if saw.Network != NetworkNone || !saw.EffectivePrivateHome() {
		t.Fatalf("defaults not applied: %+v", saw)
	}
	stdout, stderr := proc.Output()
	if string(stdout) != "stdout-ok" || string(stderr) != "stderr-ok" {
		t.Fatalf("Output = (%q, %q), want (stdout-ok, stderr-ok)", stdout, stderr)
	}
}

func assertProcLifecycle(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.CommandContext(ctx, testBinSh)
	cmd.Path = exe
	cmd.Args = []string{exe, fakeSandboxExecFlag, "--", testEchoStdin}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	proc := NewProcPiped(cmd, stdin, stdout, &bytes.Buffer{})
	if proc.ContainsDescendants() || proc.PID() != 0 {
		t.Fatal("unstarted Proc must have PID 0 and ContainsDescendants false")
	}
	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}
	if proc.PID() == 0 || proc.Stdin() == nil || proc.Stdout() == nil || proc.Stderr() == nil {
		t.Fatal("started Proc accessors must be populated")
	}
	assertProcPipedIOAndWait(t, ctx, proc)
}

func assertProcPipedIOAndWait(t *testing.T, ctx context.Context, proc *Proc) {
	t.Helper()
	select {
	case <-proc.Exited():
		t.Fatal("Exited closed before Wait")
	default:
	}
	if _, writeErr := io.WriteString(proc.StdinWriter(), "hello-piped"); writeErr != nil {
		t.Fatal(writeErr)
	}
	closePipeQuietly(proc.StdinWriter())
	readBytes, err := io.ReadAll(proc.StdoutReader())
	if err != nil || string(readBytes) != "hello-piped" {
		t.Fatalf("StdoutReader = %q (%v), want hello-piped", readBytes, err)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		supervise.Go(ctx, "sandbox-proc-wait", func(context.Context) error {
			defer wg.Done()
			return proc.Wait()
		})
	}
	wg.Wait()
	<-proc.Exited()
	if killErr := proc.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
		t.Fatalf("proc.Kill = %v", killErr)
	}
	var emptyProc Proc
	if killErr := emptyProc.Kill(); killErr != nil {
		t.Fatalf("emptyProc.Kill = %v", killErr)
	}
}
