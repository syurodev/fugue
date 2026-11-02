//go:build linux

package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func runPlatformChildHelper(args []string) (int, bool) {
	if !slices.Contains(args, landlockChildFlag) {
		return 0, false
	}
	return runLandlockChildHelper(), true
}

func runLandlockChildHelper() int {
	workDir, err := os.MkdirTemp("", "fugue-landlock-probe-")
	if err != nil {
		writeQuietly(os.Stderr, err.Error()+"\n")
		return 2
	}
	probeFile := filepath.Join(workDir, "probe")
	if writeErr := os.WriteFile(probeFile, []byte("ok"), 0o600); writeErr != nil {
		removeAllQuietly(workDir)
		writeQuietly(os.Stderr, writeErr.Error()+"\n")
		return 2
	}

	restrictErr := RestrictSelf(&Policy{WorkDir: workDir})
	if errors.Is(restrictErr, ErrLandlockSkipped) {
		removeAllQuietly(workDir)
		writeQuietly(os.Stdout, "landlock: skipped\n")
		return 0
	}
	if restrictErr != nil {
		removeAllQuietly(workDir)
		writeQuietly(os.Stderr, restrictErr.Error()+"\n")
		return 2
	}
	if !LandlockAvailable() {
		writeQuietly(os.Stderr, "RestrictSelf succeeded but LandlockAvailable is false\n")
		return 2
	}
	if _, statErr := os.Stat(probeFile); statErr != nil {
		writeQuietly(os.Stderr, statErr.Error()+"\n")
		return 2
	}
	removeAllQuietly(probeFile)
	writeQuietly(os.Stdout, "landlock: applied\n")
	return 0
}

func TestBwrapBackendUnitAndSmoke(t *testing.T) {
	t.Parallel()

	t.Run("unit backend start and piped", func(t *testing.T) {
		t.Parallel()
		assertMissingBwrapBackend(t)
		assertFakeBwrapBackend(t)
	})
	t.Run("live bwrap smoke", func(t *testing.T) {
		t.Parallel()
		assertBwrapLiveSmoke(t)
	})
}

func assertMissingBwrapBackend(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if DefaultRunner() == nil || NewBwrapBackend().Name() != "bwrap" {
		t.Fatal("unexpected default runner or backend name")
	}

	missing := &BwrapBackend{
		lookPath: func(string) (string, error) {
			return "", exec.ErrNotFound
		},
	}
	if !errors.Is(missing.Available(ctx), ErrUnavailable) {
		t.Fatal("missing bwrap must return ErrUnavailable")
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

func assertFakeBwrapBackend(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeBackend := &BwrapBackend{
		lookPath: func(string) (string, error) {
			return exe, nil
		},
	}
	if availErr := fakeBackend.Available(ctx); availErr != nil {
		t.Fatalf("fake Available: %v", availErr)
	}
	proc, err := fakeBackend.Start(ctx, &Policy{WorkDir: t.TempDir()}, Cmd{Path: "ok", Args: []string{fakeSandboxExecFlag}})
	if err != nil {
		t.Fatal(err)
	}
	if !proc.ContainsDescendants() {
		t.Fatal("bwrap Proc must claim descendant containment")
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
	if _, writeErr := io.WriteString(pipedProc.StdinWriter(), "piped-roundtrip"); writeErr != nil {
		t.Fatal(writeErr)
	}
	closePipeQuietly(pipedProc.StdinWriter())
	pipedOut, err := io.ReadAll(pipedProc.StdoutReader())
	if err != nil || string(pipedOut) != "piped-roundtrip" {
		t.Fatalf("piped stdout = %q (%v), want piped-roundtrip", pipedOut, err)
	}
	if waitErr := pipedProc.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}
}

func canRunHostBwrap(ctx context.Context) bool {
	bwrapPath, err := exec.LookPath("bwrap")
	if err != nil {
		return false
	}
	probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(probeCtx, "/usr/bin/bwrap")
	cmd.Path = bwrapPath
	cmd.Args = []string{
		bwrapPath,
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--unshare-net",
		"--die-with-parent",
		"--ro-bind", "/", "/",
		"--", testBinTrue,
	}
	return cmd.Run() == nil
}

type bwrapSmokeFixture struct {
	policy     Policy
	workDir    string
	codexDir   string
	configFile string
	outside    string
}

func prepareBwrapSmokeFixture(t *testing.T) bwrapSmokeFixture {
	t.Helper()
	root := t.TempDir()
	workDir := filepath.Join(root, "proj")
	home := filepath.Join(root, "home")
	sshDir := filepath.Join(home, ".ssh")
	codexDir := filepath.Join(home, ".config", "codex")
	rulesDir := filepath.Join(codexDir, "rules")
	for _, dir := range []string{workDir, sshDir, rulesDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_rsa"), []byte("SECRET_KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(codexDir, "config.toml")
	if err := os.WriteFile(configFile, []byte("# pinned\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside.txt")
	if err := os.WriteFile(outside, []byte("OUTSIDE_SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bwrapSmokeFixture{
		policy: Policy{
			WorkDir:        workDir,
			ReadWritePaths: []string{codexDir},
			DenyWritePaths: []string{rulesDir, configFile},
			DenyReadPaths:  []string{sshDir},
			Env:            map[string]string{testEnvHome: home, "PATH": "/usr/bin:/bin"},
			Network:        NetworkNone,
		},
		workDir:    workDir,
		codexDir:   codexDir,
		configFile: configFile,
		outside:    outside,
	}
}

func assertBwrapLiveSmoke(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	if !canRunHostBwrap(ctx) {
		return
	}
	fx := prepareBwrapSmokeFixture(t)
	insideFile := filepath.Join(fx.workDir, "inside.txt")
	script := strings.Join([]string{
		`cat "$HOME/.ssh/id_rsa" 2>/dev/null || true`,
		`cat "` + fx.outside + `" 2>/dev/null || true`,
		`printf inside-ok > "` + insideFile + `"`,
		`cat "` + insideFile + `"`,
		`! printf x >> "` + fx.configFile + `" 2>/dev/null`,
		`! mv "` + fx.codexDir + `" "` + fx.codexDir + `.moved" 2>/dev/null`,
	}, "; ")

	runCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	proc, err := NewBwrapBackend().Start(runCtx, &fx.policy, Cmd{Path: testBinSh, Args: []string{"-c", script}})
	if err != nil {
		t.Fatal(err)
	}
	if waitErr := proc.Wait(); waitErr != nil {
		stdout, stderr := proc.Output()
		t.Fatalf("live bwrap shell failed: %v (stdout=%q stderr=%q)", waitErr, stdout, stderr)
	}
	stdout, stderr := proc.Output()
	if bytes.Contains(stdout, []byte("SECRET")) || bytes.Contains(stderr, []byte("SECRET")) {
		t.Fatalf("secret leaked inside bwrap: stdout=%q stderr=%q", stdout, stderr)
	}
	if !bytes.Contains(stdout, []byte("inside-ok")) {
		t.Fatalf("inside-ok missing in stdout=%q", stdout)
	}
	if _, statErr := os.Stat(insideFile); statErr != nil {
		t.Fatalf("inside.txt stat: %v", statErr)
	}
}

func TestRestrictSelfSkippedOrApplies(t *testing.T) {
	t.Parallel()

	t.Run("unit landlock operations", func(t *testing.T) {
		t.Parallel()
		assertLandlockOpsUnit(t)
	})
	t.Run("live child process", func(t *testing.T) {
		t.Parallel()
		assertLandlockChildProcess(t)
	})
}

func assertLandlockOpsUnit(t *testing.T) {
	t.Helper()
	_ = LandlockAvailable()
	if err := RestrictSelf(&Policy{WorkDir: testRelativePath}); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("invalid policy RestrictSelf = %v, want ErrInvalidPolicy", err)
	}

	if landlockAvailableWith(landlockOps{probeABI: func() (int, error) { return 0, unix.ENOSYS }}) {
		t.Fatal("ENOSYS probe must report unavailable")
	}
	if !landlockAvailableWith(landlockOps{probeABI: func() (int, error) { return 3, nil }}) {
		t.Fatal("ABI 3 probe must report available")
	}

	workDir := t.TempDir()
	roDir := filepath.Join(workDir, "ro")
	if err := os.MkdirAll(roDir, 0o750); err != nil {
		t.Fatal(err)
	}
	p := Policy{WorkDir: workDir, ReadWritePaths: []string{workDir}, ReadOnlyPaths: []string{roDir, ""}}

	skippedCreate := defaultLandlockOps()
	skippedCreate.createRuleset = func(*unix.LandlockRulesetAttr, uintptr, uint32) (int, error) {
		return -1, unix.EOPNOTSUPP
	}
	if err := restrictSelfWith(&p, skippedCreate); !errors.Is(err, ErrLandlockSkipped) {
		t.Fatalf("EOPNOTSUPP createRuleset = %v, want ErrLandlockSkipped", err)
	}

	failCreate := defaultLandlockOps()
	failCreate.createRuleset = func(*unix.LandlockRulesetAttr, uintptr, uint32) (int, error) {
		return -1, unix.EINVAL
	}
	if err := restrictSelfWith(&p, failCreate); !errors.Is(err, unix.EINVAL) {
		t.Fatalf("EINVAL createRuleset = %v, want EINVAL", err)
	}

	assertLandlockRuleAndRestrictBranches(t, &p)
}

func assertLandlockRuleAndRestrictBranches(t *testing.T, p *Policy) {
	t.Helper()
	var added []string
	okOps := landlockOps{
		probeABI: func() (int, error) { return 1, nil },
		createRuleset: func(*unix.LandlockRulesetAttr, uintptr, uint32) (int, error) {
			return 42, nil
		},
		addPathBeneath: func(_ int, path string, _ uint64) error {
			added = append(added, path)
			return nil
		},
		restrictCurrent: func(int, uint32) error { return nil },
		closeFD:         func(int) {},
	}
	if err := restrictSelfWith(p, okOps); err != nil || len(added) != 2 {
		t.Fatalf("okOps restrictSelfWith = %v (added=%v), want nil and 2 paths", err, added)
	}

	skippedRestrict := okOps
	skippedRestrict.restrictCurrent = func(int, uint32) error { return unix.ENOSYS }
	if err := restrictSelfWith(p, skippedRestrict); !errors.Is(err, ErrLandlockSkipped) {
		t.Fatalf("ENOSYS restrictCurrent = %v, want ErrLandlockSkipped", err)
	}

	failRestrict := okOps
	failRestrict.restrictCurrent = func(int, uint32) error { return unix.EIO }
	if err := restrictSelfWith(p, failRestrict); !errors.Is(err, unix.EIO) {
		t.Fatalf("EIO restrictCurrent = %v, want EIO", err)
	}

	failAdd := okOps
	failAdd.addPathBeneath = func(int, string, uint64) error { return unix.ENOENT }
	if err := restrictSelfWith(p, failAdd); !errors.Is(err, unix.ENOENT) {
		t.Fatalf("ENOENT addPathBeneath = %v, want ENOENT", err)
	}
	if landlockSkippedErr(nil) {
		t.Fatal("nil error must not be classified as skipped")
	}
}

func assertLandlockChildProcess(t *testing.T) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), testBinSh)
	cmd.Path = exe
	cmd.Args = []string{exe, landlockChildFlag}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("landlock child failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "landlock: applied") && !strings.Contains(string(out), "landlock: skipped") {
		t.Fatalf("unexpected child output: %s", out)
	}
}

func removeAllQuietly(path string) {
	if err := os.RemoveAll(path); err != nil {
		return
	}
}
