package claude

// The login probe (VR-03, VR-E09): `claude auth status --json` over the fake
// binary, answering with the recorded 2.1.325 status documents. The host keeps
// one boolean; identity fields in the document go nowhere.

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/vendor"
)

func TestLoginProbeReadsOnlyWhatTheCLIStates(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		configDir string
		want      vendor.LoginState
	}{
		"logged in":           {fakeAuthLoggedInConfigDir, vendor.LoginActive},
		"logged out":          {fakeAuthLoggedOutConfigDir, vendor.LoginMissing},
		"unparseable output":  {fakeAuthGarbageConfigDir, vendor.LoginUnknown},
		"nothing printed":     {"", vendor.LoginUnknown},
		"status command hung": {fakeAuthHangConfigDir, vendor.LoginUnknown},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a := &Adapter{Binary: fakeClaudeBinary(t), ProbeTimeout: 2 * time.Second}
			report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: tc.configDir})
			// A stalled status command must not fail the whole probe.
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
		Binary: fakeClaudeBinary(t),
		HostEnv: []string{
			"HOME=" + fakeHome,
			"FUGUE_VAULT_KEY=secret", "ANTHROPIC_API_KEY=secret", "FUGUE_TEST_OUTER_SECRET=secret",
		},
	}
	report, err := a.Probe(t.Context(), vendor.AccountHome{ConfigDir: fakeAuthLoggedInConfigDir})
	if err != nil {
		t.Fatal(err)
	}
	if report.Login != vendor.LoginActive {
		t.Fatalf("login = %s: the status command saw a Fugue secret in its environment", report.Login)
	}
}

func TestClassifyLoginRequiresTheDocumentToSayLoggedIn(t *testing.T) {
	t.Parallel()
	in := readFile(t, filepath.Join("testdata", "fixtures", "claude-2.1.325-auth-status-logged-in.json"))
	out := readFile(t, filepath.Join("testdata", "fixtures", "claude-2.1.325-auth-status-logged-out.json"))
	cases := []struct {
		name string
		res  vendor.CommandResult
		want vendor.LoginState
	}{
		{"recorded logged in", vendor.CommandResult{Stdout: string(in), Ran: true}, vendor.LoginActive},
		{"recorded logged out, exit 1", vendor.CommandResult{Stdout: string(out), ExitCode: 1, Ran: true}, vendor.LoginMissing},
		{"logged in but the command failed", vendor.CommandResult{Stdout: string(in), ExitCode: 1, Ran: true}, vendor.LoginUnknown},
		{"no loggedIn field", vendor.CommandResult{Stdout: `{"authMethod":"none"}`, Ran: true}, vendor.LoginUnknown},
		{"loggedIn of the wrong type", vendor.CommandResult{Stdout: `{"loggedIn":"yes"}`, Ran: true}, vendor.LoginUnknown},
		{"empty", vendor.CommandResult{Ran: true}, vendor.LoginUnknown},
	}
	for _, tc := range cases {
		if got := classifyLogin(&tc.res); got != tc.want {
			t.Errorf("%s: login = %s, want %s", tc.name, got, tc.want)
		}
	}
}
