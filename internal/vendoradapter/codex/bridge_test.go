package codex

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/vendor"
)

const (
	// codexAppServerArg is the app-server subcommand every session spawns.
	codexAppServerArg = "app-server"

	testSessionValue = "c2Vzc2lvbi10b2tlbi1mb3ItdGhlLWFyZ3YtdGVzdA"
	testFugueBinary  = "/opt/fugue/bin/fugue"
	testFugueSocket  = "/run/user/1000/fugue/fugue.sock"
)

// TestCodexSessionGetsTheFugueBridgeWithoutTheTokenInArgv (F9.11 evidence):
// the bridge reaches Codex only as `-c mcp_servers.fugue.*` overrides on
// argv — no file is written — and the token travels by name through the
// `env_vars` pass-through, never as a value in argv (vendor-runtime §12).
func TestCodexSessionGetsTheFugueBridgeWithoutTheTokenInArgv(t *testing.T) {
	t.Parallel()
	token := redact.NewSecret(testSessionValue)
	bridge := vendor.Bridge{Binary: testFugueBinary, Socket: testFugueSocket}
	spec := &vendor.StartSpec{Model: testModelGPT54, SessionToken: token, Bridge: bridge}
	argv, err := BuildArgv(defaultTestCodexBin, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(argv, "\x00"), testSessionValue) {
		t.Fatal("the session token is in argv")
	}
	assertBridgeOverrides(t, argv, bridge)

	// Without a bridge the argv stays exactly the vetted two arguments.
	plain, err := BuildArgv(defaultTestCodexBin, &vendor.StartSpec{Model: testModelGPT54, SessionToken: token})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{defaultTestCodexBin, codexAppServerArg}, plain); diff != "" {
		t.Errorf("argv without a bridge (-want +got):\n%s", diff)
	}

	// Host paths only: a relative binary or socket is refused, not repaired.
	assertRelativeBridgePathsRefused(t)
}

// assertBridgeOverrides checks the `-c mcp_servers.fugue.*` overrides of the
// argv key for key: the command and args name the bridge, the env_vars
// pass-through carries the token by name, and the server cannot be disabled
// by the user's config.
func assertBridgeOverrides(t *testing.T, argv []string, bridge vendor.Bridge) {
	t.Helper()
	overrides, err := bridgeConfigOverrides(bridge)
	if err != nil {
		t.Fatal(err)
	}
	want := append([]string{defaultTestCodexBin, codexAppServerArg}, overrides...)
	if diff := cmp.Diff(want, argv); diff != "" {
		t.Fatalf("argv (-want +got):\n%s", diff)
	}
	parsed := parseBridgeOverrides(t, argv)
	if parsed.command != `"`+testFugueBinary+`"` {
		t.Errorf("mcp command = %s, want the quoted fugue binary", parsed.command)
	}
	var gotArgs []string
	if err := json.Unmarshal([]byte(parsed.args), &gotArgs); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"mcp-bridge", "--socket", testFugueSocket}, gotArgs); diff != "" {
		t.Errorf("mcp args (-want +got):\n%s", diff)
	}
	var gotEnvVars []string
	if err := json.Unmarshal([]byte(parsed.envVars), &gotEnvVars); err != nil {
		t.Fatal(err)
	}
	// The pass-through carries the variable NAME: Codex copies the value from
	// its own environment, which SessionEnv fills with the token (VR-E09).
	if diff := cmp.Diff([]string{vendor.SessionTokenEnv}, gotEnvVars); diff != "" {
		t.Errorf("mcp env_vars (-want +got):\n%s", diff)
	}
	if parsed.enabled != "true" {
		t.Errorf("mcp enabled = %q, want true so a user config cannot disable the bridge", parsed.enabled)
	}
	// The env carries the value that the pass-through name resolves to; the
	// argv above must not repeat it.
	if strings.Contains(strings.Join(SessionEnv(&vendor.StartSpec{SessionToken: redact.NewSecret(testSessionValue)}), "\x00")+"\x00"+strings.Join(argv, "\x00"), "env_vars=[\""+testSessionValue+"\"]") {
		t.Error("the token value reached the env_vars override")
	}
}

// bridgeOverrideValues collects the `-c mcp_servers.fugue.*` values of argv.
type bridgeOverrideValues struct {
	command, args, envVars, enabled string
}

func parseBridgeOverrides(t *testing.T, argv []string) bridgeOverrideValues {
	t.Helper()
	var parsed bridgeOverrideValues
	for i := 2; i+1 < len(argv); i += 2 {
		if argv[i] != "-c" {
			t.Fatalf("argv[%d] = %q, want -c", i, argv[i])
		}
		key, value, _ := strings.Cut(argv[i+1], "=")
		switch key {
		case "mcp_servers.fugue.command":
			parsed.command = value
		case "mcp_servers.fugue.args":
			parsed.args = value
		case "mcp_servers.fugue.env_vars":
			parsed.envVars = value
		case "mcp_servers.fugue.enabled":
			parsed.enabled = value
		default:
			t.Fatalf("unexpected -c override %q", argv[i+1])
		}
	}
	return parsed
}

func assertRelativeBridgePathsRefused(t *testing.T) {
	t.Helper()
	for _, relative := range []vendor.Bridge{{Binary: "fugue", Socket: testFugueSocket}, {Binary: testFugueBinary, Socket: "fugue.sock"}} {
		if _, err := BuildArgv(defaultTestCodexBin, &vendor.StartSpec{Bridge: relative}); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Errorf("relative bridge path %+v = %v, want invalid_argument", relative, err)
		}
	}
}
