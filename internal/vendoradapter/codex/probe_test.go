package codex

// The probe over the fake codex binary (this test binary re-execed, the
// internal/process pattern): version verdict, capability and the §8
// environment allowlist — the child sees only allowlisted variables, never a
// Fugue secret (VR-E09 side of the one-shot probe).

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
)

const fakeHome = "/home/REDACTED"

func fakeCodexBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func testEpoch() time.Time {
	return time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
}

func TestNewAdapterDefaultsToTheCodexBinary(t *testing.T) {
	t.Parallel()
	hostEnv := []string{"PATH=/usr/bin", "HOME=" + fakeHome}
	a := NewAdapter(hostEnv)
	if a.Binary != DefaultBinary {
		t.Fatalf("binary = %q, want %q", a.Binary, DefaultBinary)
	}
	if len(a.HostEnv) != len(hostEnv) {
		t.Fatalf("host env = %v, want the caller's snapshot", a.HostEnv)
	}
}

func TestAdapterOptionsConfigureFields(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(testEpoch())
	idGen := ids.NewSequence("test")
	probe := process.NewSystemdProbe()
	var escalationCalled bool
	onEsc := func(string) { escalationCalled = true }

	a := NewAdapter(nil,
		WithClock(clk),
		WithIDs(idGen),
		WithSystemdProbe(probe),
		WithOnEscalation(onEsc),
		WithXDGRuntimeDir("/run/user/1000"),
		WithBinary("/custom/codex"),
		WithProbeTimeout(5*time.Second),
	)

	if a.Clock != clk || a.IDs != idGen || a.SystemdProbe != probe || a.XDGRuntimeDir != "/run/user/1000" || a.Binary != "/custom/codex" || a.ProbeTimeout != 5*time.Second {
		t.Fatalf("unexpected adapter configuration: %+v", a)
	}
	if a.OnEscalation != nil {
		a.OnEscalation("test")
		if !escalationCalled {
			t.Fatal("expected escalation callback to be invoked")
		}
	}
}

func TestProbeReportsTheTestedVersionAndCapability(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeCodexBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI || !report.VersionTested || report.VersionWarning != "" {
		t.Fatalf("report = %+v", report)
	}
	if report.Path != a.Binary {
		t.Fatalf("path = %q, want the resolved binary %q", report.Path, a.Binary)
	}
	if report.Login != vendor.LoginUnknown {
		t.Fatalf("login = %s, want unknown", report.Login)
	}
	assertCodexCapability(t, report.Capability)
}

func assertCodexCapability(t *testing.T, capability vendor.Capability) {
	t.Helper()
	if !capability.PerRequestApproval || !capability.Interrupt || !capability.Steer || !capability.Resume ||
		!capability.PartialDeltas || !capability.DirectQuota || !capability.ModelList {
		t.Fatalf("capability = %+v", capability)
	}
	if capability.BlockAllToolCalls || capability.VendorSlashCommands {
		t.Fatalf("Codex adapter must not declare unsupported capabilities: %+v", capability)
	}
}

func TestProbeCommandFailureRunsUnverified(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeCodexBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeFailConfigDir})
	if err != nil {
		t.Fatalf("a failing version command is not a probe failure: %v", err)
	}
	if report.VersionTested || report.VersionWarning == "" {
		t.Fatalf("report = %+v, want an unverified warning", report)
	}
}

func TestProbeBinaryNotFoundFailsTyped(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: filepath.Join(t.TempDir(), "missing-codex")}
	_, err := a.Probe(t.Context(), vendor.AccountHome{})
	if errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	mode := os.FileMode(0o700)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestCodexProbeRunsThePATHBinaryNotTheWorkingDirectoryOne(t *testing.T) {
	// Not parallel: it changes the working directory and PATH.
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "planted-ran")
	planted := "#!/bin/sh\ntouch '" + marker + "'\necho 'codex-cli 0.0.1'\n"
	writeExecutable(t, filepath.Join(workspace, DefaultBinary), planted)
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, DefaultBinary)
	if err := os.Symlink(fakeCodexBinary(t), onPath); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	t.Setenv("PATH", pathDir)

	report, err := NewAdapter(nil).Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("the probe ran the codex planted in the working directory")
	}
	if !filepath.IsAbs(report.Path) || report.Path != onPath {
		t.Fatalf("path = %q, want the PATH binary %q", report.Path, onPath)
	}
	if report.Version != fixtureCLI || !report.VersionTested {
		t.Fatalf("report = %+v, want the PATH binary's version", report)
	}
}

func TestCodexProbeRefusesAPATHEntryInsideTheWorkingDirectory(t *testing.T) {
	// Not parallel: it changes the working directory and PATH.
	workspace := t.TempDir()
	writeExecutable(t, filepath.Join(workspace, DefaultBinary), "#!/bin/sh\n")
	t.Chdir(workspace)
	t.Setenv("PATH", ".")

	_, err := NewAdapter(nil).Probe(t.Context(), vendor.AccountHome{})
	if errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("err = %v, want permission_denied for a binary found through the working directory", err)
	}
}

func TestCodexProbeRefusesARelativeBinaryPath(t *testing.T) {
	// Not parallel: it changes the working directory.
	workspace := t.TempDir()
	writeExecutable(t, filepath.Join(workspace, DefaultBinary), "#!/bin/sh\n")
	if err := os.Mkdir(filepath.Join(workspace, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(workspace, DefaultBinary), filepath.Join(workspace, "sub", DefaultBinary)); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)

	for _, binary := range []string{"./sub/" + DefaultBinary, "sub/" + DefaultBinary} {
		_, err := (&Adapter{Binary: binary}).Probe(t.Context(), vendor.AccountHome{})
		if errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Errorf("binary %q: err = %v, want invalid_argument", binary, err)
		}
	}
}

func TestCodexProbeTimesOutWhenTheCLIHangs(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeCodexBinary(t), ProbeTimeout: 200 * time.Millisecond}
	_, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeHangConfigDir})
	if errcode.CodeOf(err) != errcode.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
}

func TestProbeHonorsTheCallersDeadlineOverItsOwnTimeout(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	a := &Adapter{Binary: fakeCodexBinary(t), ProbeTimeout: time.Hour}
	_, err := a.Probe(ctx, vendor.AccountHome{ConfigDir: fakeHangConfigDir})
	if errcode.CodeOf(err) != errcode.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
}

func TestProbeCanceledContextFailsTyped(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := (&Adapter{Binary: fakeCodexBinary(t)}).Probe(ctx, vendor.AccountHome{})
	if errcode.CodeOf(err) != errcode.Canceled {
		t.Fatalf("err = %v, want canceled", err)
	}
}

func TestCodexProbeOutputFloodIsBoundedAndUnverified(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeCodexBinary(t), ProbeTimeout: 10 * time.Second}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeFloodConfigDir})
	if err != nil {
		t.Fatalf("an oversized version output is an unknown version, not a probe failure: %v", err)
	}
	if report.Version != "" || report.VersionTested || report.VersionWarning == "" {
		t.Fatalf("report = %+v, want an unknown, unverified version", report)
	}
}

func TestCodexProbeEnvIsAllowlistedWithoutHostSecrets(t *testing.T) {
	t.Parallel()
	hostEnv := []string{
		"PATH=/usr/bin",
		"HOME=" + fakeHome,
		"USER=user",
		"LOGNAME=user",
		"LANG=C",
		"FUGUE_VAULT_KEY=supersecret",
		"FUGUE_SESSION_TOKEN=tok_123",
		"OPENAI_API_KEY=sk-proj-123",
		"ANTHROPIC_API_KEY=sk-ant-123",
		"SSH_AGENT_PID=42",
		"TERM=xterm-256color",
	}
	env := ProbeEnv(hostEnv, vendor.AccountHome{ConfigDir: "/data/homes/codex-2"})
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "CODEX_HOME":
		default:
			t.Errorf("%s leaked into the probe environment", key)
		}
	}
	if value := envValue(env, "TERM"); value != "dumb" {
		t.Errorf("TERM = %q, want dumb", value)
	}
	if value := envValue(env, "CODEX_HOME"); value != "/data/homes/codex-2" {
		t.Errorf("CODEX_HOME = %q, want the account home", value)
	}
	if slices.ContainsFunc(env, func(e string) bool {
		return strings.Contains(e, "supersecret") || strings.Contains(e, "tok_123") || strings.Contains(e, "sk-")
	}) {
		t.Error("a Fugue or provider secret reached the probe environment")
	}
}

func TestCodexProbeRunsTheCLIWithTheFilteredEnv(t *testing.T) {
	t.Parallel()
	a := &Adapter{
		Binary: fakeCodexBinary(t),
		HostEnv: []string{
			"PATH=/usr/bin:/bin",
			"HOME=" + fakeHome,
			"LANG=C",
			"FUGUE_VAULT_KEY=supersecret",
			"FUGUE_TEST_OUTER_SECRET=never-leak",
			"OPENAI_API_KEY=sk-proj-123",
		},
	}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI || !report.VersionTested {
		t.Fatalf("report = %+v, want the clean-env version 0.167.0", report)
	}
}

func envValue(env []string, key string) string {
	for _, entry := range env {
		k, v, ok := strings.Cut(entry, "=")
		if ok && k == key {
			return v
		}
	}
	return ""
}
