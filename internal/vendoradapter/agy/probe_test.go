package agy

import (
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

const (
	fakeHome    = "/home/REDACTED"
	testPathEnv = "PATH=/usr/bin"
)

func fakeAgyBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func testEpoch() time.Time {
	return time.Date(2026, 11, 15, 10, 0, 0, 0, time.UTC)
}

func TestNewAdapterDefaultsToTheAgyBinary(t *testing.T) {
	t.Parallel()
	hostEnv := []string{testPathEnv, "HOME=" + fakeHome}
	a := NewAdapter(hostEnv)
	if a.Binary != DefaultBinary {
		t.Fatalf("binary = %q, want %q", a.Binary, DefaultBinary)
	}
	if len(a.HostEnv) != len(hostEnv) {
		t.Fatalf("host env = %v, want caller snapshot", a.HostEnv)
	}
}

func TestAdapterOptionsConfigureFields(t *testing.T) {
	t.Parallel()
	clk := clock.NewFake(testEpoch())
	idGen := ids.NewSequence("test")
	probe := process.NewSystemdProbe()
	var escalated bool
	a := NewAdapter(nil,
		WithClock(clk),
		WithIDs(idGen),
		WithSystemdProbe(probe),
		WithOnEscalation(func(string) { escalated = true }),
		WithXDGRuntimeDir("/run/user/1000"),
		WithBinary("/custom/agy"),
		WithClientVersion("1.2.3-test"),
		WithProbeTimeout(5*time.Second),
	)
	if a.Clock != clk || a.IDs != idGen || a.SystemdProbe != probe || a.XDGRuntimeDir != "/run/user/1000" || a.Binary != "/custom/agy" || a.ClientVersion != "1.2.3-test" || a.ProbeTimeout != 5*time.Second {
		t.Fatalf("unexpected adapter configuration: %+v", a)
	}
	a.OnEscalation("test")
	if !escalated {
		t.Fatal("expected escalation callback to be invoked")
	}
}

func TestProbeReportsTheTestedVersionAndCapability(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeAgyBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI || !report.VersionTested || report.VersionWarning != "" {
		t.Fatalf("report = %+v", report)
	}
	if report.Path != a.Binary || report.Login != vendor.LoginUnknown {
		t.Fatalf("path/login mismatch in %+v", report)
	}
	assertProbeCapabilities(t, report.Capability)
}

func assertProbeCapabilities(t *testing.T, cap vendor.Capability) {
	t.Helper()
	if !cap.Interrupt || !cap.Resume || !cap.PartialDeltas || !cap.ModelList {
		t.Fatalf("capability missing expected flags: %+v", cap)
	}
	if cap.PerRequestApproval || cap.VendorSlashCommands || cap.Steer || cap.DirectQuota {
		t.Fatalf("capability must not declare unsupported F9.8a flags: %+v", cap)
	}
}

func TestAgyVersionOutsideTheTestedRangeWarnsButRuns(t *testing.T) {
	t.Parallel()
	for _, ver := range []string{"1.5.8", "1.6.0", "2.0.0"} {
		parsed, ok := ParseVersion(ver)
		if !ok || VersionTested(parsed) {
			t.Fatalf("ParseVersion(%q) = %v, %v; want outside pinned 1.5.9 range", ver, parsed, ok)
		}
	}
	pinned, ok := ParseVersion("1.5.9")
	if !ok || !VersionTested(pinned) {
		t.Fatalf("1.5.9 must be in tested range")
	}
	a := &Adapter{Binary: fakeAgyBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeUntestedVersionConfigDir})
	if err != nil {
		t.Fatalf("untested version probe failed: %v", err)
	}
	if report.Version != untestedCLI || report.VersionTested || report.VersionWarning == "" {
		t.Fatalf("report = %+v, want untested version with warning", report)
	}
}

func writeExecutable(t *testing.T, path, content string) {
	t.Helper()
	mode := os.FileMode(0o700)
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
}

func TestAgyProbeRunsThePATHBinaryNotTheWorkingDirectoryOne(t *testing.T) {
	// Not parallel: changes working directory and PATH.
	workspace := t.TempDir()
	marker := filepath.Join(workspace, "planted-ran")
	writeExecutable(t, filepath.Join(workspace, DefaultBinary), "#!/bin/sh\ntouch '"+marker+"'\necho '0.0.1'\n")
	pathDir := t.TempDir()
	onPath := filepath.Join(pathDir, DefaultBinary)
	if err := os.Symlink(fakeAgyBinary(t), onPath); err != nil {
		t.Fatal(err)
	}
	t.Chdir(workspace)
	t.Setenv("PATH", pathDir)

	report, err := NewAdapter(nil).Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Fatal("probe ran the binary planted in the working directory")
	}
	if report.Path != onPath || report.Version != fixtureCLI || !report.VersionTested {
		t.Fatalf("report = %+v, want PATH binary %q version %s", report, onPath, fixtureCLI)
	}
}

func TestAgyProbeRefusesRelativeOrDotPath(t *testing.T) {
	// Not parallel: changes working directory and PATH.
	workspace := t.TempDir()
	writeExecutable(t, filepath.Join(workspace, DefaultBinary), "#!/bin/sh\n")
	t.Chdir(workspace)
	t.Setenv("PATH", ".")
	if _, err := NewAdapter(nil).Probe(t.Context(), vendor.AccountHome{}); errcode.CodeOf(err) != errcode.PermissionDenied {
		t.Fatalf("dot PATH err = %v, want permission_denied", err)
	}
	if _, err := (&Adapter{Binary: "./" + DefaultBinary}).Probe(t.Context(), vendor.AccountHome{}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("relative binary err = %v, want invalid_argument", err)
	}
}

func TestAgyProbeTimesOutWhenTheCLIHangs(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeAgyBinary(t), ProbeTimeout: 200 * time.Millisecond}
	_, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeHangConfigDir})
	if errcode.CodeOf(err) != errcode.DeadlineExceeded {
		t.Fatalf("err = %v, want deadline_exceeded", err)
	}
}

func TestAgyProbeOutputFloodIsBoundedAndUnverified(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeAgyBinary(t), ProbeTimeout: 10 * time.Second}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeFloodConfigDir})
	if err != nil {
		t.Fatalf("oversized version output is an unknown version, not a probe failure: %v", err)
	}
	if report.Version != "" || report.VersionTested || report.VersionWarning == "" {
		t.Fatalf("report = %+v, want unknown unverified version", report)
	}
}

func TestAgyProbeEnvIsAllowlistedWithoutHostSecrets(t *testing.T) {
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
		"GEMINI_API_KEY=gem-secret",
		"SSH_AGENT_PID=42",
		"TERM=xterm-256color",
	}
	env := ProbeEnv(hostEnv, vendor.AccountHome{})
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM":
		default:
			t.Errorf("%s leaked into probe env", key)
		}
	}
	if slices.ContainsFunc(env, func(e string) bool {
		return strings.Contains(e, "supersecret") || strings.Contains(e, "tok_123") || strings.Contains(e, "gem-secret")
	}) {
		t.Error("secret leaked into probe env")
	}
	a := &Adapter{Binary: fakeAgyBinary(t), HostEnv: hostEnv}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI {
		t.Fatalf("probe under secret-bearing hostEnv failed or leaked: %+v", report)
	}
}
