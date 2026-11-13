package workspace

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

const rootMutationFileContent = "keep"

type workspaceGrantInvalidator struct {
	ids []string
}

func (g *workspaceGrantInvalidator) InvalidateWorkspaceGrants(id string) {
	g.ids = append(g.ids, id)
}

func TestRootMutationsUseAcceptedConfigAndInvalidateWorkspaceGrants(t *testing.T) {
	t.Parallel()
	grants := &workspaceGrantInvalidator{}
	f := newTestFixture(t, WithGrantInvalidator(grants))
	ctx := context.Background()
	first, second := filepath.Join(f.tempDir, "first"), filepath.Join(f.tempDir, "second")
	createMutationRoots(t, first, second)
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "mutations"})
	if err != nil {
		t.Fatal(err)
	}
	ws = addAndCheckRoot(t, ctx, f.svc, ws, first, 2, "main", AccessReadWrite)
	ws = updateAndCheckRoot(t, ctx, f.svc, ws, RootSpec{Path: second}, 3, "main", second, AccessReadWrite)
	ws = updateAndCheckRoot(t, ctx, f.svc, ws, RootSpec{Name: "renamed", Access: AccessReadOnly}, 4, "renamed", second, AccessReadOnly)
	ws, err = f.svc.RemoveRoot(ctx, ws.ID, ws.ConfigHash, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	if ws.ConfigRevision != 5 || ws.PolicyRevision != 5 || len(ws.Roots) != 0 {
		t.Fatalf("removed workspace revisions/roots = %d/%d/%+v", ws.ConfigRevision, ws.PolicyRevision, ws.Roots)
	}
	assertWorkspaceInvalidations(t, grants.ids, ws.ID)
	assertAcceptedWorkspaceFiles(t, f, ws)
}

func TestDeleteWorkspaceConfinesRemovalAndRequiresConfirmation(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	ws, content := createDeleteWorkspaceWithRoot(t, f, ctx)
	assertDeleteWorkspaceConfirmation(t, f, ctx, ws)
	assertDeleteWorkspaceRemovesOnlyFugueState(t, f, ctx, ws, content)
}

func createDeleteWorkspaceWithRoot(t *testing.T, f *testFixture, ctx context.Context) (*Workspace, string) {
	t.Helper()
	root := filepath.Join(f.tempDir, "user-repository")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(content, []byte(rootMutationFileContent), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "delete me"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: "delete-root", Path: root, Access: AccessReadWrite}); err != nil {
		t.Fatal(err)
	}
	ws, err = f.svc.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatal(err)
	}
	return ws, content
}

func assertDeleteWorkspaceConfirmation(t *testing.T, f *testFixture, ctx context.Context, ws *Workspace) {
	t.Helper()
	if _, err := f.svc.DeleteWorkspace(ctx, "../x", ws.ConfigHash, ws.Name); CodeOf(err) != ErrIDInvalid {
		t.Fatalf("malicious ID error = %v", err)
	}
	if _, err := f.svc.DeleteWorkspace(ctx, ws.ID, "", ws.Name); CodeOf(err) != ErrConfirmationRequired {
		t.Fatalf("missing expected hash error = %v", err)
	}
	if _, err := f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, "wrong"); CodeOf(err) != ErrConfirmationRequired {
		t.Fatalf("wrong confirmation error = %v", err)
	}
	if _, err := f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, ""); CodeOf(err) != ErrConfirmationRequired {
		t.Fatalf("missing confirmation error = %v", err)
	}
}

func assertDeleteWorkspaceRemovesOnlyFugueState(t *testing.T, f *testFixture, ctx context.Context, ws *Workspace, content string) {
	t.Helper()
	workspaceDir := f.paths.WorkspaceDir(ws.ID)
	if err := os.Symlink(filepath.Dir(content), filepath.Join(workspaceDir, "linked-root")); err != nil {
		t.Fatal(err)
	}
	deleted, err := f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, ws.Name)
	if err != nil || deleted.ID != ws.ID {
		t.Fatalf("DeleteWorkspace = %+v, err %v", deleted, err)
	}
	if _, err = os.Stat(workspaceDir); !os.IsNotExist(err) {
		t.Fatalf("workspace directory remains: %v", err)
	}
	if _, err = os.Lstat(f.paths.AcceptedWorkspaceDir(ws.ID)); !os.IsNotExist(err) {
		t.Fatalf("accepted configuration directory remains: %v", err)
	}
	if items, listErr := f.svc.ListWorkspaces(ctx); listErr != nil || len(items) != 0 {
		t.Fatalf("deleted workspace records = %+v, err %v", items, listErr)
	}
	if data, readErr := os.ReadFile(filepath.Clean(content)); readErr != nil || string(data) != rootMutationFileContent {
		t.Fatalf("root content = %q, err %v", data, readErr)
	}
}

func TestDeleteWorkspaceRejectsSymlinkAndMissingAcceptedCopy(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "protected"})
	if err != nil {
		t.Fatal(err)
	}
	workspaceDir := f.paths.WorkspaceDir(ws.ID)
	if err = os.RemoveAll(workspaceDir); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(f.tempDir, "target")
	if err = os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(target, workspaceDir); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, ws.Name); CodeOf(err) != ErrPathDenied {
		t.Fatalf("workspace symlink error = %v", err)
	}
	if _, err = os.Stat(target); err != nil {
		t.Fatalf("symlink target was touched: %v", err)
	}
	missingCopy, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "missing accepted copy"})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(f.paths.AcceptedConfigPath(missingCopy.ID)); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DeleteWorkspace(ctx, missingCopy.ID, missingCopy.ConfigHash, missingCopy.Name); CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("missing accepted copy error = %v", err)
	}
	if _, err = f.svc.store.Get(ctx, missingCopy.ID); err != nil {
		t.Fatalf("failed deletion removed DB row: %v", err)
	}
}

func TestOpenRemovalRootsRejectsSwappedWorkspacesSymlink(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "swap parent"})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.svc.validateWorkspaceDirectory(ws.ID); err != nil {
		t.Fatal(err)
	}
	fakeRoot := filepath.Join(f.tempDir, "fake-root")
	if err = os.MkdirAll(fakeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(fakeRoot, "keep.txt")
	if err = os.WriteFile(rootFile, []byte(rootMutationFileContent), 0o600); err != nil {
		t.Fatal(err)
	}
	movedWorkspaceDir := filepath.Join(f.tempDir, "workspaces-before-swap")
	if err = os.Rename(f.paths.WorkspacesDir, movedWorkspaceDir); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(fakeRoot, f.paths.WorkspacesDir); err != nil {
		t.Fatal(err)
	}
	if err = openRemovalRootsErr(f.svc, ws.ID); CodeOf(err) != ErrPathDenied {
		t.Fatalf("swapped workspace parent error = %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Clean(rootFile)); readErr != nil || string(data) != rootMutationFileContent {
		t.Fatalf("fake root file = %q, err %v", data, readErr)
	}
	if _, statErr := os.Lstat(f.paths.AcceptedConfigPath(ws.ID)); statErr != nil {
		t.Fatalf("rejected removal touched accepted copy: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedWorkspaceDir, ws.ID)); statErr != nil {
		t.Fatalf("rejected removal touched original workspace: %v", statErr)
	}
}

func TestOpenRemovalRootsRejectsWorkspaceSymlink(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "swap workspace"})
	if err != nil {
		t.Fatal(err)
	}
	workspaceDir := f.paths.WorkspaceDir(ws.ID)
	if err = f.svc.validateWorkspaceDirectory(ws.ID); err != nil {
		t.Fatal(err)
	}
	movedWorkspaceDir := filepath.Join(f.tempDir, "workspace-before-swap")
	if err = os.Rename(workspaceDir, movedWorkspaceDir); err != nil {
		t.Fatal(err)
	}
	fakeRoot := filepath.Join(f.tempDir, "fake-root")
	if err = os.MkdirAll(fakeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	rootFile := filepath.Join(fakeRoot, "keep.txt")
	if err = os.WriteFile(rootFile, []byte(rootMutationFileContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(fakeRoot, workspaceDir); err != nil {
		t.Fatal(err)
	}
	if err = openRemovalRootsErr(f.svc, ws.ID); CodeOf(err) != ErrPathDenied {
		t.Fatalf("swapped workspace error = %v", err)
	}
	if data, readErr := os.ReadFile(filepath.Clean(rootFile)); readErr != nil || string(data) != rootMutationFileContent {
		t.Fatalf("fake root file = %q, err %v", data, readErr)
	}
	if _, statErr := os.Lstat(f.paths.AcceptedConfigPath(ws.ID)); statErr != nil {
		t.Fatalf("rejected removal touched accepted copy: %v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(movedWorkspaceDir, "fugue.toml")); statErr != nil {
		t.Fatalf("rejected removal touched original workspace: %v", statErr)
	}
}

func TestDeleteWorkspaceRefusedAfterValidationKeepsRecord(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "swap accepted parent"})
	if err != nil {
		t.Fatal(err)
	}
	acceptedParent := filepath.Dir(f.paths.AcceptedWorkspaceDir(ws.ID))
	movedParent := filepath.Join(f.tempDir, "accepted-parent-before-swap")
	if err = os.Rename(acceptedParent, movedParent); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(movedParent, acceptedParent); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, ws.Name); CodeOf(err) != ErrPathDenied {
		t.Fatalf("accepted parent symlink error = %v", err)
	}
	if _, err = f.svc.store.Get(ctx, ws.ID); err != nil {
		t.Fatalf("refused deletion removed DB row: %v", err)
	}
	if _, err = os.Stat(filepath.Join(movedParent, ws.ID, acceptedConfigName)); err != nil {
		t.Fatalf("refused deletion touched accepted copy: %v", err)
	}
	if _, err = os.Stat(f.paths.ConfigPath(ws.ID)); err != nil {
		t.Fatalf("refused deletion touched workspace directory: %v", err)
	}
	if err = os.Remove(acceptedParent); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(movedParent, acceptedParent); err != nil {
		t.Fatal(err)
	}
	if _, err = f.svc.DeleteWorkspace(ctx, ws.ID, ws.ConfigHash, ws.Name); err != nil {
		t.Fatalf("retried deletion: %v", err)
	}
}

func openRemovalRootsErr(svc *Service, id string) error {
	roots, err := svc.openRemovalRoots(id)
	if err != nil {
		return err
	}
	return roots.close()
}

func createMutationRoots(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
}

func addAndCheckRoot(t *testing.T, ctx context.Context, svc *Service, ws *Workspace, path string, revision int64, name string, access RootAccess) *Workspace {
	t.Helper()
	updated, err := svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: name, Path: path, Access: access})
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRootRevision(t, updated, revision, name, path, access)
	return updated
}

func updateAndCheckRoot(t *testing.T, ctx context.Context, svc *Service, ws *Workspace, update RootSpec, revision int64, name, path string, access RootAccess) *Workspace {
	t.Helper()
	updated, err := svc.UpdateRoot(ctx, ws.ID, ws.ConfigHash, "main", update)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceRootRevision(t, updated, revision, name, path, access)
	return updated
}

func assertWorkspaceInvalidations(t *testing.T, invalidated []string, id string) {
	t.Helper()
	if len(invalidated) != 4 {
		t.Fatalf("invalidated workspaces = %v, want four root changes", invalidated)
	}
	for _, got := range invalidated {
		if got != id {
			t.Fatalf("invalidated workspace id = %q, want %q", got, id)
		}
	}
}

func assertAcceptedWorkspaceFiles(t *testing.T, f *testFixture, ws *Workspace) {
	t.Helper()
	data, err := os.ReadFile(f.paths.WorkspaceMDPath(ws.ID))
	if err != nil || len(data) == 0 {
		t.Fatalf("WORKSPACE.md after root removal = %q, err %v", data, err)
	}
	accepted, err := os.ReadFile(f.paths.AcceptedConfigPath(ws.ID))
	if err != nil || computeConfigHash(accepted) != ws.AcceptedConfigHash {
		t.Fatalf("accepted copy hash does not match workspace: err %v", err)
	}
}

func assertWorkspaceRootRevision(t *testing.T, ws *Workspace, revision int64, name, path string, access RootAccess) {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	if ws.ConfigRevision != revision || ws.PolicyRevision != revision || len(ws.Roots) != 1 ||
		ws.Roots[0].Name != name || ws.Roots[0].Path != resolved || ws.Roots[0].Access != access {
		t.Fatalf("workspace root revision = %+v", ws)
	}
}
