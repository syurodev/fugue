package claude

// The probe over the fake claude binary (this test binary re-execed, the
// internal/process pattern): version verdict, capability and the §8
// environment allowlist — the child sees only allowlisted variables, never a
// Fugue secret (VR-E09 side of the one-shot probe; the session spawn env
// test rides F9.5b).

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
)

// fakeClaudeBinary returns this test binary, which answers --version as the
// recorded CLI would when its environment is clean (main_test.go).
func fakeClaudeBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

func TestNewAdapterDefaultsToTheClaudeBinary(t *testing.T) {
	t.Parallel()
	hostEnv := []string{"PATH=/usr/bin", "HOME=/home/user"}
	a := NewAdapter(hostEnv)
	if a.Binary != DefaultBinary {
		t.Fatalf("binary = %q, want %q", a.Binary, DefaultBinary)
	}
	if len(a.HostEnv) != len(hostEnv) {
		t.Fatalf("host env = %v, want the caller's snapshot", a.HostEnv)
	}
}

func TestProbeReportsTheTestedVersionAndCapability(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeClaudeBinary(t)}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI || !report.VersionTested || report.VersionWarning != "" {
		t.Fatalf("report = %+v", report)
	}
	if report.Login != vendor.LoginUnknown {
		t.Fatalf("login = %s, want unknown: the CLI reports login only during a turn", report.Login)
	}
	assertClaudeCapability(t, report.Capability)
}

func assertClaudeCapability(t *testing.T, capability vendor.Capability) {
	t.Helper()
	if !capability.Interrupt || !capability.Steer || !capability.Resume ||
		!capability.PartialDeltas || !capability.VendorSlashCommands || !capability.DirectQuota {
		t.Fatalf("capability = %+v", capability)
	}
	if capability.PerRequestApproval || capability.BlockAllToolCalls || capability.ModelList {
		t.Fatalf("F9.5 must not declare capabilities it does not have: %+v", capability)
	}
}

func TestProbeCommandFailureRunsUnverified(t *testing.T) {
	t.Parallel()
	a := &Adapter{Binary: fakeClaudeBinary(t)}
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
	a := &Adapter{Binary: filepath.Join(t.TempDir(), "missing-claude")}
	_, err := a.Probe(t.Context(), vendor.AccountHome{})
	if errcode.CodeOf(err) != errcode.NotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
}

func TestProbeEnvIsAllowlistedWithoutHostSecrets(t *testing.T) {
	t.Parallel()
	hostEnv := []string{
		"PATH=/usr/bin",
		"HOME=/home/user",
		"USER=user",
		"LOGNAME=user",
		"LANG=C",
		"FUGUE_VAULT_KEY=supersecret",
		"FUGUE_SESSION_TOKEN=tok_123",
		"ANTHROPIC_API_KEY=sk-ant-123",
		"SSH_AGENT_PID=42",
		"TERM=xterm-256color",
	}
	env := ProbeEnv(hostEnv, vendor.AccountHome{ConfigDir: "/data/homes/claude-2"})
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		switch key {
		case "PATH", "HOME", "USER", "LOGNAME", "LANG", "TERM", "CLAUDE_CONFIG_DIR":
		default:
			t.Errorf("%s leaked into the probe environment", key)
		}
	}
	if value := envValue(env, "TERM"); value != "dumb" {
		t.Errorf("TERM = %q, want dumb", value)
	}
	if value := envValue(env, "CLAUDE_CONFIG_DIR"); value != "/data/homes/claude-2" {
		t.Errorf("CLAUDE_CONFIG_DIR = %q, want the account home", value)
	}
	if slices.ContainsFunc(env, func(e string) bool {
		return strings.Contains(e, "supersecret") || strings.Contains(e, "tok_123") || strings.Contains(e, "sk-ant")
	}) {
		t.Error("a Fugue or provider secret reached the probe environment")
	}
}

func TestProbeRunsTheCLIWithTheFilteredEnv(t *testing.T) {
	t.Parallel()
	// The fake reports the tested version only when its own environment is
	// clean (main_test.go): a leaked secret or a non-dumb TERM surfaces as an
	// unparseable version. The child inherits nothing besides the filtered
	// snapshot.
	a := &Adapter{
		Binary: fakeClaudeBinary(t),
		HostEnv: []string{
			"PATH=/usr/bin:/bin",
			"HOME=/home/user",
			"LANG=C",
			"FUGUE_VAULT_KEY=supersecret",
			"FUGUE_TEST_OUTER_SECRET=never-leak",
			"ANTHROPIC_API_KEY=sk-ant-123",
		},
	}
	report, err := a.Probe(t.Context(), vendor.AccountHome{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Version != fixtureCLI || !report.VersionTested {
		t.Fatalf("report = %+v, want the clean-env version 2.1.319", report)
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
