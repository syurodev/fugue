package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
)

func createTestWorkspaceWithRepo(t *testing.T, f *testFixture, name string) (*Workspace, string) {
	t.Helper()
	repo := filepath.Join(f.tempDir, name+"-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}
	ws, err := f.svc.CreateWorkspace(t.Context(), CreateWorkspaceRequest{
		Name:    name,
		HomeDir: filepath.Join(f.tempDir, "userhome"),
		Roots:   []RootSpec{{Name: testMainRoot, Path: repo, Access: AccessReadWrite}},
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	return ws, repo
}

func TestDirectEditAndUpdateConfigMatchAndCASConflict_WSE09(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f1 := newTestFixture(t)
	ws1, repo1 := createTestWorkspaceWithRepo(t, f1, "ws-direct")

	f2 := newTestFixture(t)
	ws2, repo2 := createTestWorkspaceWithRepo(t, f2, "ws-api")

	makeTOML := func(repo string) []byte {
		return []byte("name = \"updated-name\"\n[[roots]]\nname = \"main\"\npath = " +
			"\"" + filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[profiles.dev]\nvendor = \"claude\"\neffort = \"high\"\n")
	}

	testWriteFile(t, f1.paths.ConfigPath(ws1.ID), makeTOML(repo1))
	eff1, loaded1, err := f1.svc.GetEffectiveConfig(ctx, ws1.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig ws1: %v", err)
	}
	updated2, err := f2.svc.UpdateWorkspaceConfig(ctx, ws2.ID, makeTOML(repo2), ws2.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig ws2: %v", err)
	}
	eff2, _, err := f2.svc.GetEffectiveConfig(ctx, ws2.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig ws2: %v", err)
	}
	if eff1.Name != eff2.Name || eff1.Profiles["dev"] != eff2.Profiles["dev"] {
		t.Fatalf("direct edit and UpdateWorkspaceConfig diverged: %+v vs %+v", eff1, eff2)
	}
	if loaded1.ConfigRevision != updated2.ConfigRevision || loaded1.PolicyRevision != updated2.PolicyRevision {
		t.Fatalf("revisions diverged: loaded1=%+v updated2=%+v", loaded1, updated2)
	}

	_, err = f2.svc.UpdateWorkspaceConfig(ctx, ws2.ID, makeTOML(repo2), ws2.ConfigHash)
	if err == nil || CodeOf(err) != ErrConfigChanged {
		t.Fatalf("expected ErrConfigChanged on stale expected_hash, got %v", err)
	}
}

func TestOutOfBandNonPolicyChangeApplies(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "oob-non-policy")

	nonPolicyTOML := []byte("name = \"oob-renamed\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[[roles]]\nname = \"reviewer\"\nvendor = \"claude\"\neffort = \"medium\"\npermission = \"read\"\n\n[profiles.fast]\nvendor = \"claude\"\neffort = \"low\"\n")
	testWriteFile(t, f.paths.ConfigPath(ws.ID), nonPolicyTOML)

	eff, loaded, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig: %v", err)
	}
	if loaded.PendingPolicyConfirmation {
		t.Fatalf("expected PendingPolicyConfirmation=false for non-policy edit, got diffs=%+v", loaded.PolicyDiff)
	}
	if loaded.ConfigRevision != 2 || loaded.PolicyRevision != 1 || loaded.ConfigHash != loaded.AcceptedConfigHash {
		t.Fatalf("unexpected revisions/hashes: %+v", loaded)
	}
	if eff.Name != "oob-renamed" || len(eff.Roles) != 1 || eff.Profiles["fast"].Effort != "low" {
		t.Fatalf("expected non-policy changes to apply immediately, got %+v", eff)
	}
}

func TestOutOfBandPolicyChangeIsNotAppliedUntilConfirmed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "oob-policy")

	tamperedTOML := []byte("name = \"oob-policy\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[policy]\napproval_mode = \"full\"\nshell_allow = [\"rm -rf\"]\n")
	testWriteFile(t, f.paths.ConfigPath(ws.ID), tamperedTOML)

	assertPendingPolicyChangeHeld(t, ctx, f, ws.ID)
	assertRejectRestoresAcceptedCopy(t, ctx, f, ws)

	testWriteFile(t, f.paths.ConfigPath(ws.ID), tamperedTOML)
	assertConfirmAppliesPolicyChange(t, ctx, f, ws.ID)
	testConfirmCrashBetweenFileAndDBFailsClosed(t)
}

func TestConfirmRejectsWhenFileChangedAfterReview(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "confirm-race")
	reviewed := []byte("name = \"reviewed\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[policy]\napproval_mode = \"full\"\n")
	changed := []byte("name = \"not-reviewed\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[policy]\napproval_mode = \"full\"\nshell_allow = [\"rm -rf\"]\n")
	testWriteFile(t, f.paths.ConfigPath(ws.ID), reviewed)
	_, pending, err := f.svc.GetEffectiveConfig(t.Context(), ws.ID, nil)
	if err != nil || !pending.PendingPolicyConfirmation {
		t.Fatalf("GetEffectiveConfig pending = %+v, err %v", pending, err)
	}
	reviewedHash := pending.ConfigHash
	testWriteFile(t, f.paths.ConfigPath(ws.ID), changed)
	_, confirmErr := f.svc.ConfirmWorkspaceConfig(t.Context(), ws.ID, reviewedHash)
	if CodeOf(confirmErr) != ErrConfigChanged {
		t.Fatalf("ConfirmWorkspaceConfig error = %v, want config_changed", confirmErr)
	}
	_, rejectErr := f.svc.RejectWorkspaceConfig(t.Context(), ws.ID, reviewedHash)
	if CodeOf(rejectErr) != ErrConfigChanged {
		t.Fatalf("RejectWorkspaceConfig error = %v, want config_changed", rejectErr)
	}
	got, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil || !bytes.Equal(got, changed) {
		t.Fatalf("config file after rejected confirm = %q, err %v", got, err)
	}
}

func assertPendingPolicyChangeHeld(t *testing.T, ctx context.Context, f *testFixture, wsID string) {
	t.Helper()
	eff, loaded, err := f.svc.GetEffectiveConfig(ctx, wsID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig: %v", err)
	}
	if !loaded.PendingPolicyConfirmation || len(loaded.PolicyDiff) == 0 || loaded.PolicyRevision != 1 {
		t.Fatalf("expected pending policy confirmation at rev 1, got %+v", loaded)
	}
	if eff.Policy.ApprovalMode != defaultApprovalMode || len(eff.Policy.ShellAllow) != 0 {
		t.Fatalf("unconfirmed policy took effect! got %+v", eff.Policy)
	}
	if !slices.ContainsFunc(loaded.PolicyDiff, func(d PolicyDiffEntry) bool {
		return d.Key == keyPolicyApprovalMode && d.OldValue == defaultApprovalMode && d.NewValue == "full"
	}) {
		t.Fatalf("expected policy.approval_mode diff in %+v", loaded.PolicyDiff)
	}
}

func assertRejectRestoresAcceptedCopy(t *testing.T, ctx context.Context, f *testFixture, ws *Workspace) {
	t.Helper()
	pending, err := f.svc.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace before Reject: %v", err)
	}
	rejected, err := f.svc.RejectWorkspaceConfig(ctx, ws.ID, pending.ConfigHash)
	if err != nil {
		t.Fatalf("RejectWorkspaceConfig: %v", err)
	}
	if rejected.PendingPolicyConfirmation || rejected.PolicyRevision != 1 || rejected.ConfigHash != ws.AcceptedConfigHash {
		t.Fatalf("unexpected state after Reject: %+v", rejected)
	}
}

func assertConfirmAppliesPolicyChange(t *testing.T, ctx context.Context, f *testFixture, wsID string) {
	t.Helper()
	pending, err := f.svc.GetWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetWorkspace before Confirm: %v", err)
	}
	confirmed, err := f.svc.ConfirmWorkspaceConfig(ctx, wsID, pending.ConfigHash)
	if err != nil {
		t.Fatalf("ConfirmWorkspaceConfig: %v", err)
	}
	if confirmed.PendingPolicyConfirmation || confirmed.PolicyRevision != 2 || confirmed.ConfigHash != confirmed.AcceptedConfigHash {
		t.Fatalf("unexpected state after Confirm: %+v", confirmed)
	}
	effAfter, _, err := f.svc.GetEffectiveConfig(ctx, wsID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig after Confirm: %v", err)
	}
	if effAfter.Policy.ApprovalMode != "full" || len(effAfter.Policy.ShellAllow) != 1 {
		t.Fatalf("expected confirmed policy to take effect, got %+v", effAfter.Policy)
	}
}

func testConfirmCrashBetweenFileAndDBFailsClosed(t *testing.T) {
	t.Helper()
	crashErr := NewError(ErrOwnershipInvalid, "simulated crash after accepted.toml write before DB update")
	f := newTestFixture(t, WithBeforeStoreUpdate(func(context.Context, string) error { return crashErr }))
	ws, repo := createTestWorkspaceWithRepo(t, f, "crash-confirm")
	tampered := []byte("name = \"crash-confirm\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[policy]\napproval_mode = \"plan\"\n")
	testWriteFile(t, f.paths.ConfigPath(ws.ID), tampered)

	pending, err := f.svc.GetWorkspace(t.Context(), ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ConfirmWorkspaceConfig(t.Context(), ws.ID, pending.ConfigHash); !errors.Is(err, crashErr) {
		t.Fatalf("expected simulated crash error, got %v", err)
	}
	if _, err := f.svc.GetWorkspace(t.Context(), ws.ID); err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected fail-closed ErrConfigUnconfirmed after mid-Confirm crash, got %v", err)
	}
}

func TestUnparsableConfigKeepsAcceptedPolicy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, _ := createTestWorkspaceWithRepo(t, f, "unparsable-ws")

	testWriteFile(t, f.paths.ConfigPath(ws.ID), []byte("name = [broken toml syntax\n"))

	eff, loaded, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig must keep accepted policy instead of failing open: %v", err)
	}
	if loaded.ConfigError == nil || CodeOf(loaded.ConfigError) != ErrConfigInvalid {
		t.Fatalf("expected ws.ConfigError with ErrConfigInvalid, got %v", loaded.ConfigError)
	}
	if eff.Name != "unparsable-ws" || eff.Policy.ApprovalMode != defaultApprovalMode || len(eff.Roots) != 1 {
		t.Fatalf("accepted policy was not preserved: %+v", eff)
	}

	if _, cwdErr := f.svc.SelectSessionCwd(ctx, ws.ID, testMainRoot); cwdErr == nil || CodeOf(cwdErr) != ErrConfigInvalid {
		t.Fatalf("expected SelectSessionCwd to reject new session with ErrConfigInvalid, got %v", cwdErr)
	}
}

func TestAcceptedCopyHashMismatchFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "mismatch-copy")

	tamperedCopy := []byte("name = \"mismatch-copy\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[policy]\napproval_mode = \"full\"\n")
	testWriteFile(t, f.paths.AcceptedConfigPath(ws.ID), tamperedCopy)

	if _, err := f.svc.GetWorkspace(ctx, ws.ID); err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected GetWorkspace to fail closed with ErrConfigUnconfirmed on hash mismatch, got %v", err)
	}
	if _, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil); err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected GetEffectiveConfig to fail closed with ErrConfigUnconfirmed on hash mismatch, got %v", err)
	}
}

func TestAcceptedCopyMissingFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, _ := createTestWorkspaceWithRepo(t, f, "missing-copy")

	accPath := f.paths.AcceptedConfigPath(ws.ID)
	if err := os.Remove(accPath); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.GetWorkspace(ctx, ws.ID); err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected GetWorkspace to fail closed with ErrConfigUnconfirmed when accepted.toml is missing, got %v", err)
	}
	if _, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil); err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected GetEffectiveConfig to fail closed with ErrConfigUnconfirmed when accepted.toml is missing, got %v", err)
	}

	if err := os.Symlink(f.paths.ConfigPath(ws.ID), accPath); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.GetWorkspace(ctx, ws.ID); err == nil || CodeOf(err) != ErrOwnershipInvalid {
		t.Fatalf("expected symlink at accepted.toml to fail closed with ErrOwnershipInvalid, got %v", err)
	}
}

func TestPolicyRevisionBumpInvalidatesWorkspaceGrants_WSE13(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	grants := make(map[string]int)
	f := newTestFixture(t, WithGrantInvalidator(grantInvalidatorFunc(func(wsID string) {
		delete(grants, wsID)
	})))
	ws, repo := createTestWorkspaceWithRepo(t, f, "grant-ws")
	grants[ws.ID] = 2

	nonPolicyTOML := []byte("name = \"grant-ws-renamed\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[profiles.default]\nvendor = \"claude\"\neffort = \"high\"\n")
	wsAfterNonPolicy, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, nonPolicyTOML, ws.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig non-policy: %v", err)
	}
	if wsAfterNonPolicy.PolicyRevision != 1 || grants[ws.ID] != 2 {
		t.Fatalf("expected PolicyRevision=1 and grants preserved, got rev=%d grants=%v", wsAfterNonPolicy.PolicyRevision, grants)
	}

	policyTOML := []byte("name = \"grant-ws-renamed\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[redact]\nallow_keys = [\"SAFE_PUBLIC_TOKEN_ID\"]\n")
	wsAfterPolicy, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, policyTOML, wsAfterNonPolicy.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig policy: %v", err)
	}
	if wsAfterPolicy.PolicyRevision != 2 {
		t.Fatalf("expected PolicyRevision=2 after policy update, got %d", wsAfterPolicy.PolicyRevision)
	}
	if _, exists := grants[ws.ID]; exists {
		t.Fatal("expected workspace grants to be invalidated after policy_revision bump")
	}
}

func TestEnvChangeBumpsPolicyRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var invalidated []string
	f := newTestFixture(t, WithGrantInvalidator(grantInvalidatorFunc(func(wsID string) {
		invalidated = append(invalidated, wsID)
	})))
	ws, repo := createTestWorkspaceWithRepo(t, f, "env-policy-ws")

	envTOML := []byte("name = \"env-policy-ws\"\n[[roots]]\nname = \"main\"\npath = \"" +
		filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n\n[env]\nkeep = [\"RUST_LOG\"]\noverride_allow = [\"GOFLAGS\"]\n")
	updated, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, envTOML, ws.ConfigHash)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig [env]: %v", err)
	}
	if updated.PolicyRevision != 2 || !slices.Equal(invalidated, []string{ws.ID}) {
		t.Fatalf("expected PolicyRevision=2 and grant invalidation for %s, got rev=%d inv=%v", ws.ID, updated.PolicyRevision, invalidated)
	}
}

type grantInvalidatorFunc func(string)

func (fn grantInvalidatorFunc) InvalidateWorkspaceGrants(workspaceID string) { fn(workspaceID) }

func TestTier2EditCannotChangeEffectivePolicy(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, _ := createTestWorkspaceWithRepo(t, f, "tier2-edit")
	before, loadedBefore, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig: %v", err)
	}

	testWriteFile(t, f.paths.GlobalConfigPath(), []byte("[host]\nsnapshot_disk = \"2 GiB\"\n"))
	after, loadedAfter, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig after tier 2 edit: %v", err)
	}
	if !reflect.DeepEqual(before.Policy, after.Policy) || !reflect.DeepEqual(before.Env, after.Env) || !reflect.DeepEqual(before.Redact, after.Redact) {
		t.Fatalf("tier 2 edit changed effective policy: before=%+v after=%+v", before, after)
	}
	if loadedAfter.PolicyRevision != loadedBefore.PolicyRevision || loadedAfter.PendingPolicyConfirmation {
		t.Fatalf("policy_revision or pending state changed: before=%+v after=%+v", loadedBefore, loadedAfter)
	}

	testWriteFile(t, f.paths.GlobalConfigPath(), []byte("[policy]\napproval_mode = \"full\"\n"))
	if _, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil); err == nil || CodeOf(err) != ErrConfigInvalid {
		t.Fatalf("expected ErrConfigInvalid for tier 2 policy, got %v", err)
	}
}
