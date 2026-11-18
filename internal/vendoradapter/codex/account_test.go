package codex

// F9.10a: the login probe over the fake binary (recorded 0.147.0 status
// output) and the recorded rateLimits lines feeding the vendor's account. The
// recorded fixture is the real CLI's output; the exhausted-plan and failed
// turns are those lines with one field changed (named "derived" below),
// because a real exhausted or logged-out account cannot be recorded on demand.

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/testutil"
	"github.com/syurodev/fugue/internal/vendor"
)

const quotaFixture = "codex-0.167.0-session3-approval.jsonl"

func TestLoginProbeReadsOnlyWhatTheCLIStates(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		configDir string
		want      vendor.LoginState
	}{
		"logged in":           {fakeLoginLoggedInConfigDir, vendor.LoginActive},
		"logged out":          {fakeLoginLoggedOutConfigDir, vendor.LoginMissing},
		"unrelated failure":   {fakeLoginGarbageConfigDir, vendor.LoginUnknown},
		"no status command":   {"", vendor.LoginUnknown},
		"status command hung": {fakeLoginHangConfigDir, vendor.LoginUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := &Adapter{Binary: fakeCodexBinary(t), ProbeTimeout: 2 * time.Second}
			report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: tc.configDir})
			if err != nil {
				t.Fatal(err)
			}
			if report.Login != tc.want {
				t.Fatalf("login = %s, want %s", report.Login, tc.want)
			}
			if report.Version != fixtureCLI {
				t.Fatalf("version = %q: the login step must not disturb the version verdict", report.Version)
			}
		})
	}
}

func TestLoginProbeEnvCarriesNoHostSecret(t *testing.T) {
	t.Parallel()
	a := &Adapter{
		Binary: fakeCodexBinary(t),
		HostEnv: []string{
			"HOME=/home/REDACTED",
			"FUGUE_VAULT_KEY=secret", "OPENAI_API_KEY=secret", "FUGUE_TEST_OUTER_SECRET=secret",
		},
	}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeLoginLoggedInConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	if report.Login != vendor.LoginActive {
		t.Fatalf("login = %s: the status command saw a Fugue secret in its environment", report.Login)
	}
}

// TestLoginProcessEnvCarriesNoHostSecret is VR-E09 for the login process
// (F9.10b): the CLI's own login command runs with the §8 allowlist the probe
// uses, so no Fugue secret and no other host variable reaches it.
func TestLoginProcessEnvCarriesNoHostSecret(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "fake-login")
	testutil.WriteExecutable(t, path, []byte("#!/bin/sh\n/bin/sleep 0.5\nexec /usr/bin/env\n"))
	a := &Adapter{
		Binary: path,
		HostEnv: []string{
			"HOME=" + fakeHome,
			"FUGUE_VAULT_KEY=secret", "OPENAI_API_KEY=secret", "FUGUE_TEST_OUTER_SECRET=secret",
		},
	}
	run, err := a.StartLogin(t.Context(), vendor.AccountHome{ConfigDir: fakeLoginLoggedInConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	for line := range run.Lines() {
		out.WriteString(line)
		out.WriteByte('\n')
	}
	<-run.Done()
	env := out.String()
	for _, want := range []string{"HOME=" + fakeHome, "TERM=dumb", "CODEX_HOME=" + fakeLoginLoggedInConfigDir} {
		if !strings.Contains(env, want) {
			t.Errorf("login process environment lost %s: %q", want, env)
		}
	}
	for _, banned := range []string{"FUGUE_VAULT_KEY", "OPENAI_API_KEY", "FUGUE_TEST_OUTER_SECRET", "secret"} {
		if strings.Contains(env, banned) {
			t.Errorf("login process environment carries %s: %q", banned, env)
		}
	}
}

func TestClassifyLoginRecordedOutput(t *testing.T) {
	t.Parallel()
	in := readFile(t, filepath.Join("testdata", "fixtures", "codex-0.147.0-login-status-logged-in.stdout"))
	out := readFile(t, filepath.Join("testdata", "fixtures", "codex-0.147.0-login-status-logged-out.stderr"))
	cases := []struct {
		name string
		res  vendor.CommandResult
		want vendor.LoginState
	}{
		{"recorded logged in", vendor.CommandResult{Stdout: string(in), Ran: true}, vendor.LoginActive},
		{"recorded logged out", vendor.CommandResult{Stderr: string(out), ExitCode: 1, Ran: true}, vendor.LoginMissing},
		{"exit 1 with other text", vendor.CommandResult{Stderr: "boom", ExitCode: 1, Ran: true}, vendor.LoginUnknown},
		{"unknown subcommand", vendor.CommandResult{Stderr: "unrecognized subcommand", ExitCode: 2, Ran: true}, vendor.LoginUnknown},
	}
	for _, tc := range cases {
		if got := classifyLogin(&tc.res); got != tc.want {
			t.Errorf("%s: login = %s, want %s", tc.name, got, tc.want)
		}
	}
}

func codexAccounts() *vendor.Accounts {
	return vendor.NewAccounts(clock.NewFake(testEpoch()), []vendor.AccountVendor{{Name: DefaultBinary, DirectQuota: true}})
}

func lineWith(t *testing.T, fixture, marker string) []byte {
	t.Helper()
	for _, line := range readFixture(t, fixture) {
		if bytes.Contains(line, []byte(marker)) {
			return line
		}
	}
	t.Fatalf("fixture %s has no line with %s", fixture, marker)
	return nil
}

func TestRecordedRateLimitsBecomeAccountQuotaWindows(t *testing.T) {
	t.Parallel()
	accounts := codexAccounts()
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1")
	for _, marker := range []string{`"account/rateLimits/updated"`} {
		obs, err := n.Normalize(lineWith(t, quotaFixture, marker))
		if err != nil {
			t.Fatal(err)
		}
		accounts.ObserveQuota("codex", obs.Quota)
	}
	got := accounts.Snapshot()[0]
	if got.State != vendor.AccountOK || !got.QuotaKnown || len(got.Windows) != 1 {
		t.Fatalf("account = %+v, want ok with one known window", got)
	}
	w := got.Windows[0]
	if w.Name != "primary" || w.Utilization != 0.01 || !w.ResetsAt.Equal(time.Unix(1797260501, 0)) {
		t.Fatalf("window = %+v", w)
	}
}

func TestDerivedExhaustedWindowPausesTheAccountUntilResetsAt(t *testing.T) {
	t.Parallel()
	line := lineWith(t, quotaFixture, `"account/rateLimits/updated"`)
	derived := bytes.Replace(line, []byte(`"usedPercent":1,`), []byte(`"usedPercent":100,`), 1)
	if bytes.Equal(line, derived) {
		t.Fatal("the derived line did not change: the fixture shape moved")
	}
	n := NewNormalizer("session-1")
	n.BeginTurn("turn-1")
	obs, err := n.Normalize(derived)
	if err != nil {
		t.Fatal(err)
	}
	accounts := codexAccounts()
	accounts.ObserveQuota("codex", obs.Quota)
	got := accounts.Snapshot()[0]
	if got.State != vendor.AccountPaused || !got.PausedUntil.Equal(time.Unix(1797260501, 0)) {
		t.Fatalf("account = %+v, want paused until the window's resetsAt", got)
	}
}

func TestDerivedFailedTurnIsClassifiedByCodexErrorInfo(t *testing.T) {
	t.Parallel()
	completed := lineWith(t, "codex-0.167.0-session1.jsonl", `"turn/completed"`)
	cases := map[string]vendor.FailureClass{
		"unauthorized":       vendor.FailureAuth,
		"usageLimitExceeded": vendor.FailureQuota,
		"rateLimitExceeded":  vendor.FailureRateLimited,
		"serverOverloaded":   vendor.FailureNone,
	}
	for info, want := range cases {
		failed := strings.Replace(string(completed), `"status":"completed","error":null`,
			`"status":"failed","error":{"message":"x","codexErrorInfo":"`+info+`","additionalDetails":null}`, 1)
		if failed == string(completed) {
			t.Fatal("the derived line did not change: the fixture shape moved")
		}
		n := NewNormalizer("session-1")
		n.BeginTurn("turn-1")
		obs, err := n.Normalize([]byte(failed))
		if err != nil {
			t.Fatal(err)
		}
		if obs.TurnEnd == nil || obs.TurnEnd.Outcome != OutcomeFailed || obs.TurnEnd.Failure != want || obs.TurnEnd.FailureCode != info {
			t.Errorf("%s: turn end = %+v, want a failed turn classed %q", info, obs.TurnEnd, want)
		}
	}
}
