package app

import (
	"fmt"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	testAppPatchKeyIdle      = "teams.idle_detach_seconds"
	testAppPatchKeyMaxAgents = "teams.team_max_agents"
)

func TestWorkspacePatchRuntimeAndDedup(t *testing.T) {
	t.Parallel()
	rt, stream, _ := workspaceRuntimeFixture(t)
	created, err := rt.CreateWorkspace(t.Context(), string(KindApp), "patch-rt")
	if err != nil {
		t.Fatal(err)
	}
	assertAppWorkspaceEvent(t, stream, 1, EventWorkspaceCreated)

	assertAppPatchRejections(t, rt, created.ID, created.ConfigHash)
	patched := assertAppPatchSetAndDedup(t, rt, stream, created.ID, created.ConfigHash)
	assertAppPatchDeleteAndEvict(t, rt, created.ID, created.ConfigHash, patched.ConfigHash)
}

func assertAppPatchRejections(t *testing.T, rt *HostRuntime, wsID, hash string) {
	t.Helper()
	ctx := t.Context()
	for _, principal := range []string{PrincipalSession, string(KindCLI)} {
		if _, err := rt.PatchWorkspaceConfig(ctx, principal, "cmd-deny", wsID, hash, []WorkspaceConfigPatch{
			{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 60},
		}); errcode.CodeOf(err) != errcode.PermissionDenied {
			t.Fatalf("principal %q err = %v, want PermissionDenied", principal, err)
		}
	}
	if _, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "", wsID, hash, []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 60},
	}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty command_id err = %v, want InvalidArgument", err)
	}
	if _, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-empty", wsID, hash, nil); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("empty patches err = %v, want InvalidArgument", err)
	}
	oversized := make([]WorkspaceConfigPatch, maxWorkspaceConfigPatches+1)
	for i := range oversized {
		oversized[i] = WorkspaceConfigPatch{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 60}
	}
	if _, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-over", wsID, hash, oversized); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("oversized patches err = %v, want InvalidArgument", err)
	}
	if _, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-badop", wsID, hash, []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyIdle, Op: 99},
	}); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("bad op err = %v, want InvalidArgument", err)
	}
}

func assertAppPatchSetAndDedup(t *testing.T, rt *HostRuntime, stream *Subscription, wsID, hash string) *WorkspaceView {
	t.Helper()
	ctx := t.Context()
	patches := []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 90},
		{DottedKey: testAppPatchKeyMaxAgents, Op: WorkspacePatchSet, Value: 8},
	}
	patched, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-set-1", wsID, hash, patches)
	if err != nil {
		t.Fatalf("PatchWorkspaceConfig set: %v", err)
	}
	assertAppWorkspaceEvent(t, stream, 2, EventWorkspaceConfigChanged)

	replayed, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-set-1", wsID, hash, patches)
	if err != nil || replayed.ConfigHash != patched.ConfigHash {
		t.Fatalf("replay = (%+v, %v)", replayed, err)
	}
	if _, mismatchErr := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-set-1", wsID, hash, []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 45},
	}); errcode.CodeOf(mismatchErr) != errcode.InvalidArgument {
		t.Fatalf("mismatch replay err = %v, want InvalidArgument", mismatchErr)
	}
	return patched
}

func assertAppPatchDeleteAndEvict(t *testing.T, rt *HostRuntime, wsID, staleHash, curHash string) {
	t.Helper()
	ctx := t.Context()
	deleted, err := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-del-1", wsID, curHash, []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyMaxAgents, Op: WorkspacePatchDelete},
	})
	if err != nil {
		t.Fatalf("PatchWorkspaceConfig delete: %v", err)
	}
	if _, casErr := rt.PatchWorkspaceConfig(ctx, string(KindApp), "cmd-stale", wsID, staleHash, []WorkspaceConfigPatch{
		{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 30},
	}); errcode.CodeOf(casErr) != errcode.WorkspaceConfigChanged {
		t.Fatalf("stale CAS err = %v, want WorkspaceConfigChanged", casErr)
	}
	hash := deleted.ConfigHash
	for i := range workspacePatchDedupCap + 2 {
		v, loopErr := rt.PatchWorkspaceConfig(ctx, string(KindApp), fmt.Sprintf("cmd-evict-%d", i), wsID, hash, []WorkspaceConfigPatch{
			{DottedKey: testAppPatchKeyIdle, Op: WorkspacePatchSet, Value: 100 + i},
		})
		if loopErr != nil {
			t.Fatalf("evict loop %d: %v", i, loopErr)
		}
		hash = v.ConfigHash
	}
}
