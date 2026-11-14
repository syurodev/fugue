package claude

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
	testSessionValue = "c2Vzc2lvbi10b2tlbi1mb3ItdGhlLWFyZ3YtdGVzdA"
	testFugueBinary  = "/opt/fugue/bin/fugue"
	testFugueSocket  = "/run/user/1000/fugue/fugue.sock"
)

const (
	envHome = "/home/testuser"
	envUser = "testuser"
	envPath = "/usr/bin:/bin"
	envLang = "en_US.UTF-8"
)

// entries joins name and value pairs into environment entries.
func entries(pairs ...string) []string {
	out := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, pairs[i]+"="+pairs[i+1])
	}
	return out
}

func dirtyHostEnv() []string {
	return entries(
		"PATH", envPath, "HOME", envHome, "USER", envUser, "LOGNAME", envUser, "LANG", envLang, "TERM", "xterm-256color",
		"FUGUE_SECRET_KEY", "super-secret-vault-token-12345",
		vendor.SessionTokenEnv, "a-token-inherited-from-the-host-environment",
		"SYNX_FUGUE_DATA_DIR", envHome+"/.config/Fugue",
		"ANTHROPIC_API_KEY", "sk-ant-live-secret-never-leak",
		"GITHUB_TOKEN", "ghp_secretTokenDoNotPassToProcess",
	)
}

func TestVendorEnvCarriesOnlyTheSessionToken(t *testing.T) {
	t.Parallel()
	base := entries("HOME", envHome, "LANG", envLang, "LOGNAME", envUser, "PATH", envPath, "TERM", "dumb", "USER", envUser)

	without := SessionEnv(&vendor.StartSpec{HostEnv: dirtyHostEnv()})
	if diff := cmp.Diff(base, without); diff != "" {
		t.Errorf("env without a session token (-want +got):\n%s", diff)
	}

	// The only variable the session adds is its own token; a variable of the
	// same name in the host environment never passes the allowlist.
	want := append([]string{vendor.SessionTokenEnv + "=" + testSessionValue}, base...)
	with := SessionEnv(&vendor.StartSpec{HostEnv: dirtyHostEnv(), SessionToken: redact.NewSecret(testSessionValue)})
	if diff := cmp.Diff(want, with); diff != "" {
		t.Errorf("env with a session token (-want +got):\n%s", diff)
	}
	if got := ProbeEnv(dirtyHostEnv(), vendor.AccountHome{}); cmp.Diff(base, got) != "" {
		t.Errorf("the probe env carries more than the allowlist: %v", got)
	}
}

func TestSessionArgvCarriesTheFugueBridgeAndNoToken(t *testing.T) {
	t.Parallel()
	spec := &vendor.StartSpec{
		Model: testModelSonnet, SessionToken: redact.NewSecret(testSessionValue),
		Bridge: vendor.Bridge{Binary: testFugueBinary, Socket: testFugueSocket},
	}
	argv, err := BuildArgv(defaultTestClaudeBinary, spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(argv, "\x00"), testSessionValue) {
		t.Fatal("the session token is in argv")
	}
	tail := argv[len(argv)-3:]
	if tail[0] != "--mcp-config" || tail[2] != "--strict-mcp-config" {
		t.Fatalf("argv tail = %q, want --mcp-config <json> --strict-mcp-config", tail)
	}
	var got map[string]map[string]mcpServer
	if err := json.Unmarshal([]byte(tail[1]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]map[string]mcpServer{"mcpServers": {"fugue": {
		Type: "stdio", Command: testFugueBinary, Args: []string{"mcp-bridge", "--socket", testFugueSocket},
		Env: map[string]string{vendor.SessionTokenEnv: "${" + vendor.SessionTokenEnv + "}"},
	}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mcp config (-want +got):\n%s", diff)
	}
	// Fugue's own flags are the only MCP flags: everything before them is
	// free of the denylist.
	for _, arg := range argv[1 : len(argv)-3] {
		if IsDenylistedFlag(arg) {
			t.Errorf("denylisted flag %q among the profile arguments", arg)
		}
	}
}

func TestProfileStillCannotSetTheMCPFlags(t *testing.T) {
	t.Parallel()
	bridge := vendor.Bridge{Binary: testFugueBinary, Socket: testFugueSocket}
	for _, denied := range []string{"--mcp-config", "--mcp-config={}", "--permission-prompt-tool", "--permission-prompt-tool=mcp__x__y"} {
		for _, spec := range []vendor.StartSpec{{Model: denied, Bridge: bridge}, {Effort: denied, Bridge: bridge}, {ResumeSessionID: denied, Bridge: bridge}} {
			if _, err := BuildArgv(defaultTestClaudeBinary, &spec); errcode.CodeOf(err) != errcode.InvalidArgument {
				t.Errorf("profile value %q = %v, want invalid_argument", denied, err)
			}
		}
	}
	for _, relative := range []vendor.Bridge{{Binary: "fugue", Socket: testFugueSocket}, {Binary: testFugueBinary, Socket: "fugue.sock"}} {
		if _, err := BuildArgv(defaultTestClaudeBinary, &vendor.StartSpec{Bridge: relative}); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Errorf("relative bridge path %+v = %v, want invalid_argument", relative, err)
		}
	}
}
