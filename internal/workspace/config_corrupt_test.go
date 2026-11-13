package workspace

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const corruptTOML = "name = \n[[roots\n"

// corruptWorkspace writes unparsable TOML out of band and returns the hash a client sees via Get.
func corruptWorkspace(t *testing.T, ctx context.Context, f *testFixture, ws *Workspace) string {
	t.Helper()
	testWriteFile(t, f.paths.ConfigPath(ws.ID), []byte(corruptTOML))
	_, loaded, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatalf("GetEffectiveConfig on corrupt file: %v", err)
	}
	if loaded.ConfigError == nil || loaded.ConfigHash == ws.ConfigHash {
		t.Fatalf("expected config error and new disk hash, got %+v", loaded)
	}
	return loaded.ConfigHash
}

func TestRejectRestoresCorruptedConfigWithShownHash_WSE12(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, _ := createTestWorkspaceWithRepo(t, f, "corrupt-reject")
	accepted, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	effBefore, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	shown := corruptWorkspace(t, ctx, f, ws)

	restored, err := f.svc.RejectWorkspaceConfig(ctx, ws.ID, shown)
	if err != nil {
		t.Fatalf("RejectWorkspaceConfig with shown hash: %v", err)
	}
	if restored.ConfigError != nil || restored.ConfigHash != ws.AcceptedConfigHash {
		t.Fatalf("workspace after reject = %+v", restored)
	}
	got, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil || !bytes.Equal(got, accepted) {
		t.Fatalf("fugue.toml after reject = %q, err %v", got, err)
	}
	effAfter, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil || !reflect.DeepEqual(effBefore.Policy, effAfter.Policy) {
		t.Fatalf("effective policy changed: before %+v after %+v, err %v", effBefore.Policy, effAfter.Policy, err)
	}
}

func TestUpdateOverwritesCorruptedConfigWithShownHash_WSE12(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "corrupt-update")
	shown := corruptWorkspace(t, ctx, f, ws)

	valid := []byte("name = \"recovered\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n")
	updated, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, valid, shown)
	if err != nil {
		t.Fatalf("UpdateWorkspaceConfig with shown hash: %v", err)
	}
	if updated.ConfigError != nil || updated.Name != "recovered" {
		t.Fatalf("workspace after update = %+v", updated)
	}
}

func TestCorruptedConfigStaleHashIsConfigChanged_WSE12(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, repo := createTestWorkspaceWithRepo(t, f, "corrupt-stale")
	corruptWorkspace(t, ctx, f, ws)

	valid := []byte("name = \"x\"\n[[roots]]\nname = \"main\"\npath = \"" + filepath.ToSlash(repo) + "\"\naccess = \"rw\"\n")
	for _, stale := range []string{ws.ConfigHash, "", "deadbeef"} {
		if _, err := f.svc.RejectWorkspaceConfig(ctx, ws.ID, stale); CodeOf(err) != ErrConfigChanged {
			t.Fatalf("Reject with %q: got %v, want config_changed", stale, err)
		}
		if _, err := f.svc.UpdateWorkspaceConfig(ctx, ws.ID, valid, stale); CodeOf(err) != ErrConfigChanged {
			t.Fatalf("Update with %q: got %v, want config_changed", stale, err)
		}
	}
	got, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil || string(got) != corruptTOML {
		t.Fatalf("file modified by refused ops: %q, err %v", got, err)
	}
}

func TestConfirmRefusesCorruptedConfig_WSE12(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)
	ws, _ := createTestWorkspaceWithRepo(t, f, "corrupt-confirm")
	shown := corruptWorkspace(t, ctx, f, ws)

	if _, err := f.svc.ConfirmWorkspaceConfig(ctx, ws.ID, shown); err == nil {
		t.Fatal("ConfirmWorkspaceConfig accepted an unparsable file")
	}
	_, loaded, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil || loaded.AcceptedConfigHash != ws.AcceptedConfigHash {
		t.Fatalf("accepted hash changed after refused confirm: %+v, err %v", loaded, err)
	}
}
