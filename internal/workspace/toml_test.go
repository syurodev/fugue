package workspace

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/config"
)

func TestConfigRejectsUnknownKeys(t *testing.T) {
	t.Parallel()
	invalidTOML := []byte("name = \"my-project\"\nunknown_option = true\n")
	_, err := decodeConfigTOML(invalidTOML)
	if err == nil {
		t.Fatal("expected error decoding TOML with unknown keys, got nil")
	}
	if CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	var target *Error
	if !errors.As(err, &target) || target.Line != 2 || target.Key != "unknown_option" {
		t.Fatalf("expected error at line 2 with key unknown_option, got %+v", target)
	}
	if strings.Contains(err.Error(), "unknown_option = true") {
		t.Errorf("error message leaked file contents: %s", err.Error())
	}
}

func TestConfigDecodeErrorHasLineAndColumn(t *testing.T) {
	t.Parallel()
	syntaxErrTOML := []byte("name = \"good\"\n[[roots]]\nname = [unclosed\n")
	_, err := decodeConfigTOML(syntaxErrTOML)
	if err == nil {
		t.Fatal("expected syntax error, got nil")
	}
	if CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	var wsErr *Error
	if !errors.As(err, &wsErr) {
		t.Fatalf("expected *Error, got %T", err)
	}
	line, col, ok := wsErr.Position()
	if !ok || line == 0 || col == 0 {
		t.Fatalf("expected valid position (line > 0, col > 0), got line=%d, col=%d, ok=%v", line, col, ok)
	}
	if strings.Contains(err.Error(), "[unclosed") {
		t.Fatalf("error message leaked raw file content: %s", err.Error())
	}
}

func TestConfigRoundTripsPathsWithQuotesAndUnicode(t *testing.T) {
	t.Parallel()
	roots := []Root{
		{
			Name:   "tieng-viet",
			Path:   "/tmp/dự án/thư mục \"đặc biệt\"\\test",
			Access: AccessReadWrite,
		},
	}
	encoded, err := encodeConfigTOML("dự-án-chính", roots)
	if err != nil {
		t.Fatalf("encodeConfigTOML: %v", err)
	}

	decoded, err := decodeConfigTOML(encoded)
	if err != nil {
		t.Fatalf("decodeConfigTOML: %v", err)
	}
	if decoded.Name != "dự-án-chính" {
		t.Errorf("decoded Name = %q, want 'dự-án-chính'", decoded.Name)
	}
	if len(decoded.Roots) != 1 || decoded.Roots[0].Path != roots[0].Path {
		t.Errorf("decoded roots = %+v, want %+v", decoded.Roots, roots)
	}
}

func TestIsSubPath(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
		parent string
		want   bool
	}{
		{"prefix dots ..x", "/a/..x", "/a", true},
		{"triple dots ...", "/a/...", "/a", true},
		{"identical path", "/a", "/a", false},
		{"sibling path", "/b", "/a", false},
		{"nested child", "/a/child", "/a", true},
		{"deep nested", "/a/b/c", "/a", true},
		{"parent of target", "/a", "/a/child", false},
		{"cousin paths", "/a/b", "/a/c", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := isSubPath(tt.target, tt.parent)
			if got != tt.want {
				t.Errorf("isSubPath(%q, %q) = %v, want %v", tt.target, tt.parent, got, tt.want)
			}
		})
	}
}

func TestStrictDecodeAndHardRuleProtection_WSE10(t *testing.T) {
	t.Parallel()
	testStrictDecodeRejections(t)
	testSecretRejectionWithoutLeakingValue(t)
	testHardRuleI03Protection(t)
}

func testStrictDecodeRejections(t *testing.T) {
	t.Helper()
	cases := []struct {
		name    string
		tier    Tier
		raw     string
		wantKey string
	}{
		{"git_guard rejected as unknown key", TierWorkspace, "name = \"proj\"\n[roots.main.git_guard]\nmode = \"ask\"\n", "roots.main.git_guard"},
		{"host section rejected in tier 3", TierWorkspace, "name = \"proj\"\n[host]\nsnapshot_disk = \"2 GiB\"\n", "host"},
		{"name rejected in tier 2", TierGlobal, "name = \"global-not-allowed\"\n", "name"},
		{"policy rejected in tier 4", TierSession, "model = \"claude-sonnet-4-6\"\n[policy]\napproval_mode = \"full\"\n", "policy"},
		{"invalid profile effort", TierWorkspace, "[profiles.dev]\neffort = \"turbo\"\n", "profiles.dev.effort"},
		{"invalid approval_mode", TierWorkspace, "[policy]\napproval_mode = \"yolo\"\n", keyPolicyApprovalMode},
		{"invalid host byte size", TierGlobal, "[host]\nsnapshot_disk = \"-5 MiB\"\n", "host.snapshot_disk"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertConfigErrorKey(t, tc.tier, tc.raw, tc.wantKey)
		})
	}
}

func assertConfigErrorKey(t *testing.T, tier Tier, raw, wantKey string) {
	t.Helper()
	err := ValidateConfig(tier, []byte(raw))
	if err == nil || CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid, got %v", err)
	}
	var wsErr *Error
	if !errors.As(err, &wsErr) || wsErr.File == "" || wsErr.Key != wantKey {
		t.Fatalf("expected file set and key=%q, got %+v", wantKey, wsErr)
	}
}

func testSecretRejectionWithoutLeakingValue(t *testing.T) {
	t.Helper()
	sampleVal := "sk-ant-" + "api03-testValueForValidatorCheck1234567890"
	raw := "[profiles.work]\nmodel = \"" + sampleVal + "\"\n"
	assertConfigErrorKey(t, TierWorkspace, raw, "profiles.work.model")
	err := ValidateConfig(TierWorkspace, []byte(raw))
	if strings.Contains(err.Error(), sampleVal) {
		t.Fatalf("error message leaked sensitive value: %s", err.Error())
	}
}

func testHardRuleI03Protection(t *testing.T) {
	t.Helper()
	raw := "[[policy.env_tags]]\nmatch = \"deploy/*\"\ntag = \"prod\"\nrequires_confirmation = false\n"
	assertConfigErrorKey(t, TierWorkspace, raw, "policy.env_tags.requires_confirmation")

	for _, tag := range []string{"prod", "", "staging"} {
		if !RequiresConfirmationForTag(tag) {
			t.Fatalf("tag %q must always require confirmation (I-03)", tag)
		}
	}
	for _, tag := range []string{"dev", "local", "test"} {
		if RequiresConfirmationForTag(tag) {
			t.Fatalf("tag %q should not require confirmation", tag)
		}
	}
	assertShellAllowHardRule(t)
}

func assertShellAllowHardRule(t *testing.T) {
	t.Helper()
	cfg := DefaultConfig("/tmp/fugue", "ws-1")
	cfg.Policy.ShellAllow = []string{"kubectl apply", "git status"}
	cfg.Policy.ShellAsk = []string{"kubectl apply --force"}
	cfg.Policy.EnvTags = []EnvTagRule{
		{Match: "prod-*", Tag: "prod"},
		{Match: "dev-*", Tag: "dev"},
	}
	if IsShellAllowed(&cfg, "kubectl apply -f app.yaml", "prod-cluster") {
		t.Fatal("shell_allow must never beat a target with a tag requiring confirmation (prod)")
	}
	if IsShellAllowed(&cfg, "kubectl apply -f app.yaml", "unmapped-target") {
		t.Fatal("shell_allow must never beat an unmapped target requiring confirmation")
	}
	if !IsShellAllowed(&cfg, "kubectl apply -f app.yaml", "dev-cluster") {
		t.Fatal("expected shell_allow to permit command on dev-cluster")
	}
	if IsShellAllowed(&cfg, "kubectl apply --force -f app.yaml", "dev-cluster") {
		t.Fatal("shell_ask must take precedence over shell_allow")
	}
}

const sampleTier2TOML = `
[profiles.default]
vendor = "claude"
model = "claude-sonnet-4-6"
effort = "medium"

[host]
workspaces_dir = "~/Fugue/custom-ws"
snapshot_disk = "2 GiB"
`

const sampleTier3TOML = `
name = "ws-merged"

[policy]
shell_allow = ["git status", "git diff", "go test"]
approval_mode = "ask"

[env]
keep = ["LANG", "PATH", "GOFLAGS"]

[redact]
allow_keys = ["OVERRIDE_KEY"]

[[roles]]
name = "coder"
vendor = "claude"
effort = "xhigh"
permission = "write"
checks = ["task test"]

[profiles.default]
effort = "max"

[session]
scratch_dir = "~/Fugue/custom-scratch"
`

func TestFourTierMergeAddConflictAndProvenance_WSE11(t *testing.T) {
	t.Parallel()
	tempDir := t.TempDir()
	homeDir := filepath.Join(tempDir, "home")
	fugueHome := filepath.Join(homeDir, "Fugue")
	customWsDir := filepath.Join(fugueHome, "custom-ws")
	if err := os.MkdirAll(fugueHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fugueHome, "config.toml"), []byte(sampleTier2TOML), 0o600); err != nil {
		t.Fatal(err)
	}

	paths, err := ResolvePaths(config.MapEnv(nil), func() (string, error) { return homeDir, nil }, filepath.Join(tempDir, "data"))
	if err != nil {
		t.Fatalf("ResolvePaths: %v", err)
	}
	if paths.WorkspacesDir != customWsDir {
		t.Fatalf("expected WorkspacesDir %q, got %q", customWsDir, paths.WorkspacesDir)
	}

	t2Cfg, err := decodeTier2TOML(paths.GlobalConfigPath(), []byte(sampleTier2TOML), homeDir)
	if err != nil {
		t.Fatalf("decodeTier2TOML: %v", err)
	}
	t3Cfg, err := decodeTier3TOML("fugue.toml", []byte(sampleTier3TOML), homeDir)
	if err != nil {
		t.Fatalf("decodeTier3TOML: %v", err)
	}

	base := DefaultConfig(fugueHome, "ws-1")
	t4 := &SessionOverride{Model: "claude-opus-4-6", Effort: "high"}
	eff := mergeConfigs(&base, &t2Cfg, &t3Cfg, t4, homeDir)

	assertMergedValuesAndProvenance(t, &eff, customWsDir, filepath.Join(fugueHome, "custom-scratch"))
	testSameTierAddConflict(t)
}

func assertMergedValuesAndProvenance(t *testing.T, eff *EffectiveConfig, wantWsDir, wantScratch string) {
	t.Helper()
	checks := []struct {
		key      string
		gotVal   string
		wantVal  string
		wantTier Tier
	}{
		{"policy.approval_mode", eff.Policy.ApprovalMode, "ask", TierWorkspace},
		{"policy.sandbox_profile", eff.Policy.SandboxProfile, "workspace-write", TierDefault},
		{"policy.shell_allow", strings.Join(eff.Policy.ShellAllow, ","), "git status,git diff,go test", TierWorkspace},
		{"env.keep", strings.Join(eff.Env.Keep, ","), "LANG,PATH,GOFLAGS", TierWorkspace},
		{"redact.allow_keys", strings.Join(eff.Redact.AllowKeys, ","), "OVERRIDE_KEY", TierWorkspace},
		{"profiles.default.vendor", eff.Profiles["default"].Vendor, "claude", TierGlobal},
		{"profiles.default.effort", eff.Profiles["default"].Effort, "max", TierWorkspace},
		{"session.scratch_dir", eff.Session.ScratchDir, wantScratch, TierWorkspace},
		{"session.model", eff.Session.Model, "claude-opus-4-6", TierSession},
		{"host.workspaces_dir", eff.Host.WorkspacesDir, wantWsDir, TierGlobal},
	}
	for _, tc := range checks {
		if tc.gotVal != tc.wantVal || eff.Provenance[tc.key] != tc.wantTier {
			t.Fatalf("%s = %q (tier %v), want %q (tier %v)", tc.key, tc.gotVal, eff.Provenance[tc.key], tc.wantVal, tc.wantTier)
		}
	}
}

func testSameTierAddConflict(t *testing.T) {
	t.Helper()
	conflicts := []struct {
		raw     string
		wantKey string
	}{
		{"[policy]\nshell_allow = [\"a\"]\nshell_allow_add = [\"b\"]\n", "policy.shell_allow_add"},
		{"[policy]\nshell_ask = [\"a\"]\nshell_ask_add = [\"b\"]\n", "policy.shell_ask_add"},
		{"[env]\nkeep = [\"FOO\"]\nkeep_add = [\"BAR\"]\n", "env.keep_add"},
		{"[env]\noverride_allow = [\"FOO\"]\noverride_allow_add = [\"BAR\"]\n", "env.override_allow_add"},
		{"[redact]\nallow_keys = [\"k1\"]\nallow_keys_add = [\"k2\"]\n", "redact.allow_keys_add"},
		{"[redact]\nallow_paths = [\"*.md\"]\nallow_paths_add = [\"*.txt\"]\n", "redact.allow_paths_add"},
	}
	for _, tc := range conflicts {
		assertConfigErrorKey(t, TierWorkspace, tc.raw, tc.wantKey)
	}
}

func TestEnvKeysValidateNamesAndRejectDenylisted(t *testing.T) {
	t.Parallel()
	valid := "[env]\nkeep = [\"RUST_LOG\", \"_CUSTOM_1\"]\noverride_allow_add = [\"GOFLAGS\"]\n"
	if err := ValidateConfig(TierWorkspace, []byte(valid)); err != nil {
		t.Fatalf("expected valid [env] to pass, got %v", err)
	}
	assertInvalidEnvNamesRejected(t)
	assertDenylistedEnvNamesRejected(t)
}

func assertInvalidEnvNamesRejected(t *testing.T) {
	t.Helper()
	for _, bad := range []string{"lowercase", "1DIGIT_FIRST", "FOO=BAR", "FOO-BAR", ""} {
		raw := "[env]\nkeep = [\"" + bad + "\"]\n"
		assertConfigErrorKey(t, TierWorkspace, raw, "env.keep")
		err := ValidateConfig(TierWorkspace, []byte(raw))
		if bad != "" && strings.Contains(err.Error(), bad) {
			t.Fatalf("error message must not contain variable value %q: %s", bad, err.Error())
		}
	}
}

func assertDenylistedEnvNamesRejected(t *testing.T) {
	t.Helper()
	denylisted := []string{
		"GITHUB_TOKEN", "MY_SECRET_VAR", "DB_PASSWORD", "LDAP_PASSWD",
		"GOOGLE_CREDENTIAL", "SSH_PRIVATE", "OPENAI_API_KEY", "AWS_DEFAULT_REGION",
	}
	for _, denied := range denylisted {
		for _, field := range []string{"keep", "keep_add", "override_allow", "override_allow_add"} {
			raw := "[env]\n" + field + " = [\"" + denied + "\"]\n"
			assertConfigErrorKey(t, TierWorkspace, raw, "env."+field)
			err := ValidateConfig(TierWorkspace, []byte(raw))
			if strings.Contains(err.Error(), denied) {
				t.Fatalf("error message leaked denylisted value %q: %s", denied, err.Error())
			}
		}
	}
}

func TestTier2RejectsPolicyEnvAndRedactKeys(t *testing.T) {
	t.Parallel()
	const secretish = "never-leak-this-value"
	cases := map[string]string{
		"policy": "[host]\nsnapshot_disk = \"2 GiB\"\n[policy]\napproval_mode = \"" + secretish + "\"\n",
		"env":    "[env]\nkeep = [\"" + secretish + "\"]\n",
		"redact": "[redact]\nallow_keys = [\"" + secretish + "\"]\n",
	}
	for group, raw := range cases {
		t.Run(group, func(t *testing.T) {
			t.Parallel()
			assertConfigErrorKey(t, TierGlobal, raw, group)
			err := ValidateConfig(TierGlobal, []byte(raw))
			var wsErr *Error
			if !errors.As(err, &wsErr) || wsErr.Line < 1 || wsErr.Column < 1 {
				t.Fatalf("expected line and column, got %+v", wsErr)
			}
			if strings.Contains(err.Error(), secretish) {
				t.Fatalf("error leaked the value: %s", err.Error())
			}
		})
	}
}
