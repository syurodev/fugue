package workspace

import (
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestPatchTOMLPreservesCommentsBlankLinesAndKeyOrder(t *testing.T) {
	t.Parallel()
	orig := strings.Join([]string{
		"# Workspace header comment",
		"name = \"alpha\" # inline name comment",
		"",
		"[[roots]]",
		"name = \"main\"",
		"path = \"/repo\"",
		"access = \"rw\"",
		"",
		"[policy]",
		"# Keep this policy comment",
		"sandbox_profile = \"workspace-write\"",
		"\"approval_mode\" = \"auto\" # inline approval comment",
		"shell_allow = [",
		"  \"git status\",",
		"  \"go version\",",
		"]",
		"shell_allow_add = [\"ls\"]",
		"env_tags = [",
		"  {",
		"    match = \"*.prod\",",
		"    tag = \"prod\",",
		"  },",
		"]",
		"",
		"[profiles.\"my-root\"]",
		"vendor = \"codex\"",
		"",
		"[teams]",
		"# Team comment",
		"team_max_agents = 10",
		"idle_detach_seconds = 300",
		"",
	}, "\n")

	patched, err := PatchTOML([]byte(orig), []ConfigPatch{
		{DottedKey: "policy.approval_mode", Op: PatchOpSet, Value: "ask"},
		{DottedKey: "policy.shell_allow", Op: PatchOpSet, Value: []string{"git status", `echo "ok"`}},
		{DottedKey: "policy.shell_allow_add", Op: PatchOpDelete},
		{DottedKey: "policy.env_tags", Op: PatchOpSet, Value: []EnvTagRule{{Match: "prod/*", Tag: "prod"}}},
		{DottedKey: "profiles.my-root.vendor", Op: PatchOpSet, Value: "claude"},
		{DottedKey: "teams.idle_detach_seconds", Op: PatchOpSet, Value: 120},
		{DottedKey: "session.teams", Op: PatchOpSet, Value: true},
		{DottedKey: "profiles.fast.vendor", Op: PatchOpSet, Value: "claude"},
	})
	if err != nil {
		t.Fatalf("PatchTOML: %v", err)
	}
	want := strings.Join([]string{
		"# Workspace header comment",
		"name = \"alpha\" # inline name comment",
		"",
		"[[roots]]",
		"name = \"main\"",
		"path = \"/repo\"",
		"access = \"rw\"",
		"",
		"[policy]",
		"# Keep this policy comment",
		"sandbox_profile = \"workspace-write\"",
		"\"approval_mode\" = \"ask\" # inline approval comment",
		"shell_allow = [\"git status\", \"echo \\\"ok\\\"\"]",
		"env_tags = [{ match = \"prod/*\", tag = \"prod\" }]",
		"",
		"[profiles.\"my-root\"]",
		"vendor = \"claude\"",
		"",
		"[teams]",
		"# Team comment",
		"team_max_agents = 10",
		"idle_detach_seconds = 120",
		"",
		"[session]",
		"teams = true",
		"",
		"[profiles.fast]",
		"vendor = \"claude\"",
		"",
	}, "\n")
	if diff := cmp.Diff(want, string(patched)); diff != "" {
		t.Fatalf("patched TOML mismatch (-want +got):\n%s", diff)
	}
}

func TestPatchTOMLRejectsInvalidInputsAndInjection(t *testing.T) {
	t.Parallel()
	base := []byte("name = \"alpha\"\n")
	badCases := []struct {
		name  string
		toml  []byte
		patch ConfigPatch
	}{
		{"unknown_key", base, ConfigPatch{DottedKey: "unknown.key", Op: PatchOpSet, Value: "x"}},
		{"host_key_rejected", base, ConfigPatch{DottedKey: "host.max_sessions", Op: PatchOpSet, Value: 5}},
		{"roots_rejected", base, ConfigPatch{DottedKey: "roots", Op: PatchOpSet, Value: "x"}},
		{"newline_injection", base, ConfigPatch{DottedKey: "policy.approval_mode", Op: PatchOpSet, Value: "ask\n[policy]\napproval_mode = \"full\""}},
		{"cr_injection", base, ConfigPatch{DottedKey: "name", Op: PatchOpSet, Value: "a\rb"}},
		{"nul_injection", base, ConfigPatch{DottedKey: "policy.shell_allow", Op: PatchOpSet, Value: []string{"ok\x00bad"}}},
		{"wrong_type_bool", base, ConfigPatch{DottedKey: "session.teams", Op: PatchOpSet, Value: "true"}},
		{"wrong_type_float", base, ConfigPatch{DottedKey: "teams.idle_detach_seconds", Op: PatchOpSet, Value: 1.5}},
		{"bad_existing_toml", []byte("not = [valid toml\n"), ConfigPatch{DottedKey: "name", Op: PatchOpSet, Value: "beta"}},
	}
	for _, tc := range badCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := PatchTOML(tc.toml, []ConfigPatch{tc.patch}); CodeOf(err) != ErrConfigInvalid {
				t.Fatalf("PatchTOML(%s) err = %v (code=%q), want ErrConfigInvalid", tc.name, err, CodeOf(err))
			}
		})
	}
	if _, err := PatchTOML(base, nil); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("empty patches err = %v, want ErrConfigInvalid", err)
	}
	tooMany := make([]ConfigPatch, maxConfigPatches+1)
	for i := range tooMany {
		tooMany[i] = ConfigPatch{DottedKey: "name", Op: PatchOpSet, Value: "alpha"}
	}
	if _, err := PatchTOML(base, tooMany); CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("oversized patches err = %v, want ErrConfigInvalid", err)
	}
}

func TestServicePatchWorkspaceConfigCASAndPolicyRevision(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := t.Context()
	ws, _ := createTestWorkspaceWithRepo(t, f, "patch-ws")
	initial := "# header\nname = \"patch-ws\"\n\n[teams]\n# keep me\nteam_max_agents = 8\n"
	updated, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, []byte(initial), ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}
	// Non-policy patch keeps PolicyRevision unchanged and preserves comments.
	nonPolicy, err := f.svc.PatchWorkspaceConfig(ctx, ws.ID, updated.ConfigHash, []ConfigPatch{
		{DottedKey: "teams.idle_detach_seconds", Op: PatchOpSet, Value: 90},
	})
	if err != nil {
		t.Fatalf("non-policy PatchWorkspaceConfig: %v", err)
	}
	if nonPolicy.PolicyRevision != updated.PolicyRevision {
		t.Fatalf("PolicyRevision bumped on non-policy patch: got %d want %d", nonPolicy.PolicyRevision, updated.PolicyRevision)
	}
	diskBytes, err := os.ReadFile(f.svc.Paths().ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(diskBytes), "# header") || !strings.Contains(string(diskBytes), "# keep me") {
		t.Fatalf("comments lost in fugue.toml:\n%s", diskBytes)
	}
	// Stale CAS fails.
	if _, err := f.svc.PatchWorkspaceConfig(ctx, ws.ID, updated.ConfigHash, []ConfigPatch{
		{DottedKey: "teams.idle_detach_seconds", Op: PatchOpSet, Value: 60},
	}); CodeOf(err) != ErrConfigChanged {
		t.Fatalf("stale CAS err = %v, want ErrConfigChanged", err)
	}
	// Policy patch bumps PolicyRevision.
	policyUpdated, err := f.svc.PatchWorkspaceConfig(ctx, ws.ID, nonPolicy.ConfigHash, []ConfigPatch{
		{DottedKey: "policy.approval_mode", Op: PatchOpSet, Value: "ask"},
	})
	if err != nil {
		t.Fatalf("policy PatchWorkspaceConfig: %v", err)
	}
	if policyUpdated.PolicyRevision != nonPolicy.PolicyRevision+1 {
		t.Fatalf("PolicyRevision = %d, want %d", policyUpdated.PolicyRevision, nonPolicy.PolicyRevision+1)
	}
}
