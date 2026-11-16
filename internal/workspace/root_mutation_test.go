package workspace

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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

func TestRootMutationPreservesCommentsAndFormatting(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	first, second := filepath.Join(f.tempDir, "first"), filepath.Join(f.tempDir, "second")
	createMutationRoots(t, first, second)
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "preserve"})
	if err != nil {
		t.Fatal(err)
	}
	initialTOML := "" +
		"# Top of file comment\n" +
		"# Second header line\n" +
		"name = \"preserve\"\n\n" +
		"[env]\n" +
		"override_allow = [\"LANG\"]\n" +
		"keep = [\"HOME\", \"PATH\"]\n\n" +
		"[policy] # comment next to [policy]\n" +
		"shell_allow = [\"git status\"]\n" +
		"approval_mode = \"ask\"\n"
	ws, err = f.svc.UpdateWorkspaceConfig(ctx, ws.ID, []byte(initialTOML), ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}

	ws, err = f.svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: "main", Path: first, Access: AccessReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	afterAdd, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(afterAdd, []byte(initialTOML)) {
		t.Fatalf("AddRoot altered bytes outside roots block:\ngot:\n%s\nwant prefix:\n%s", afterAdd, initialTOML)
	}

	ws, err = f.svc.UpdateRoot(ctx, ws.ID, ws.ConfigHash, "main", RootSpec{Name: "renamed", Path: second, Access: AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	afterUpdate, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(afterUpdate, []byte(initialTOML)) {
		t.Fatalf("UpdateRoot altered bytes outside roots block:\ngot:\n%s\nwant prefix:\n%s", afterUpdate, initialTOML)
	}

	ws, err = f.svc.RemoveRoot(ctx, ws.ID, ws.ConfigHash, "renamed")
	if err != nil {
		t.Fatal(err)
	}
	afterRemove, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	if string(afterRemove) != initialTOML {
		t.Fatalf("RemoveRoot did not restore original bytes:\ngot:\n%q\nwant:\n%q", afterRemove, initialTOML)
	}
}

func TestRootMutationGathersScatteredRootTables(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := context.Background()
	r1, r2, r3 := filepath.Join(f.tempDir, "r1"), filepath.Join(f.tempDir, "r2"), filepath.Join(f.tempDir, "r3")
	createMutationRoots(t, r1, r2, r3)
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "scattered"})
	if err != nil {
		t.Fatal(err)
	}
	scatteredTOML := "" +
		"# Header comment\n" +
		"name = \"scattered\"\n\n" +
		"[[roots]]\n" +
		"# comment inside first root\n" +
		"name = \"one\"\n" +
		"path = " + quoteTOMLString(r1) + "\n" +
		"access = \"rw\"\n\n" +
		"[policy] # keep policy comment\n" +
		"approval_mode = \"ask\"\n\n" +
		"[[roots]]\n" +
		"name = \"two\"\n" +
		"path = " + quoteTOMLString(r2) + "\n" +
		"access = \"ro\"\n\n" +
		"# Env comment\n" +
		"[env]\n" +
		"keep = [\"HOME\"]\n"
	ws, err = f.svc.UpdateWorkspaceConfig(ctx, ws.ID, []byte(scatteredTOML), ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}

	ws, err = f.svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: "three", Path: r3, Access: AccessReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Roots) != 3 {
		t.Fatalf("len(ws.Roots) = %d, want 3", len(ws.Roots))
	}
	gotBytes, err := os.ReadFile(f.paths.ConfigPath(ws.ID))
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)
	policySection := "[policy] # keep policy comment\napproval_mode = \"ask\"\n"
	envSection := "# Env comment\n[env]\nkeep = [\"HOME\"]\n"
	if !strings.HasPrefix(got, "# Header comment\nname = \"scattered\"\n\n[[roots]]\n") {
		t.Fatalf("expected roots gathered at first [[roots]] position, got:\n%s", got)
	}
	if !strings.Contains(got, policySection) || !strings.Contains(got, envSection) {
		t.Fatalf("expected policy and env sections preserved verbatim, got:\n%s", got)
	}
	policyIdx := strings.Index(got, policySection)
	lastRootIdx := strings.LastIndex(got, "[[roots]]")
	if lastRootIdx > policyIdx {
		t.Fatalf("expected all [[roots]] tables gathered before [policy], got:\n%s", got)
	}
}

func TestRootMutationFallsBackWhenRootsCannotBeLocated(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	f := newTestFixture(t, WithLogger(logger))
	ctx := context.Background()
	first, second := filepath.Join(f.tempDir, "first"), filepath.Join(f.tempDir, "second")
	createMutationRoots(t, first, second)
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "inline-roots"})
	if err != nil {
		t.Fatal(err)
	}
	inlineTOML := "name = \"inline-roots\"\nroots = [{ name = \"main\", path = " + quoteTOMLString(first) + ", access = \"rw\" }]\n"
	ws, err = f.svc.UpdateWorkspaceConfig(ctx, ws.ID, []byte(inlineTOML), ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}

	ws, err = f.svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: "extra", Path: second, Access: AccessReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Roots) != 2 {
		t.Fatalf("len(ws.Roots) = %d, want 2", len(ws.Roots))
	}
	if !strings.Contains(logBuf.String(), "falling back to full-file TOML encode") {
		t.Fatalf("expected fallback warning in log, got:\n%s", logBuf.String())
	}
}

func quoteTOMLString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

func TestRootRewriteNeverChangesKeysOutsideRoots(t *testing.T) {
	t.Parallel()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	f := newTestFixture(t, WithLogger(logger))
	ctx := context.Background()
	first := filepath.Join(f.tempDir, "first")
	createMutationRoots(t, first)
	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{Name: "guard"})
	if err != nil {
		t.Fatal(err)
	}
	initial := "name = \"guard\"\n\n[policy]\napproval_mode = \"ask\"\n"
	ws, err = f.svc.UpdateWorkspaceConfig(ctx, ws.ID, []byte(initial), ws.ConfigHash)
	if err != nil {
		t.Fatal(err)
	}

	f.svc.rewriteRootsFn = func(_ []byte, newRoots []Root) ([]byte, bool) {
		var sb strings.Builder
		sb.WriteString("name = \"guard\"\n\n[policy]\napproval_mode = \"full\"\n")
		for _, r := range newRoots {
			sb.WriteString("\n[[roots]]\nname = ")
			sb.WriteString(quoteTOMLString(r.Name))
			sb.WriteString("\npath = ")
			sb.WriteString(quoteTOMLString(r.Path))
			sb.WriteString("\naccess = ")
			sb.WriteString(quoteTOMLString(string(r.Access)))
			sb.WriteByte('\n')
		}
		return []byte(sb.String()), true
	}

	ws, err = f.svc.AddRoot(ctx, ws.ID, ws.ConfigHash, RootSpec{Name: "main", Path: first, Access: AccessReadWrite})
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Roots) != 1 {
		t.Fatalf("len(ws.Roots) = %d, want 1", len(ws.Roots))
	}
	if !strings.Contains(logBuf.String(), "falling back to full-file TOML encode") {
		t.Fatalf("expected fallback warning in log, got:\n%s", logBuf.String())
	}
	eff, _, err := f.svc.GetEffectiveConfig(ctx, ws.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if eff.Policy.ApprovalMode != "ask" {
		t.Fatalf("policy.approval_mode = %q, want %q", eff.Policy.ApprovalMode, "ask")
	}
}

func FuzzRewriteRootsKeepsOtherKeys(f *testing.F) {
	seeds := []string{
		"name = \"\"\"\n[[roots]]\nname = \"fake\"\n\"\"\"\n",
		"[policy]\nshell_allow = [\n  \"[[roots]]\",\n  \"git status\",\n]\n",
		"[[\"roots\"]] # quoted header\nname = \"a\"\npath = \"/tmp/a\"\naccess = \"rw\"\n",
		"name = \"crlf\"\r\n\r\n[[roots]]\r\nname = \"a\"\r\npath = \"/tmp/a\"\r\naccess = \"rw\"\r\n\r\n[policy]\r\napproval_mode = \"ask\"\r\n",
		"name = \"scattered\"\n[[roots]]\nname = \"a\"\npath = \"/tmp/a\"\naccess = \"rw\"\n[policy]\napproval_mode = \"ask\"\n[[roots]]\nname = \"b\"\npath = \"/tmp/b\"\naccess = \"ro\"\n",
		"name = \"no-nl\"\n[env]\nkeep = [\"HOME\"]",
		"name = \"inline\"\nroots = [{ name = \"a\", path = \"/tmp/a\", access = \"rw\" }]\n",
	}
	for i, s := range seeds {
		f.Add([]byte(s), uint8(i%3))
	}
	f.Fuzz(func(t *testing.T, raw []byte, mode uint8) {
		var origCfg tier3RawConfig
		if decodeStrictTOML("fugue.toml", raw, &origCfg) != nil {
			return
		}
		var newRoots []Root
		switch mode % 3 {
		case 1:
			newRoots = []Root{{Name: "one", Path: "/tmp/one", Access: AccessReadWrite}}
		case 2:
			newRoots = []Root{
				{Name: "one", Path: "/tmp/one", Access: AccessReadWrite},
				{Name: "two", Path: "/tmp/two", Access: AccessReadOnly},
			}
		}
		updated, ok := rewriteRootsTOML(raw, newRoots)
		if !ok {
			return
		}
		var gotCfg tier3RawConfig
		if err := decodeStrictTOML("fugue.toml", updated, &gotCfg); err != nil {
			t.Fatalf("rewritten TOML failed to parse: %v\n%s", err, updated)
		}
		if !slices.Equal(derefSlice(gotCfg.Roots), newRoots) {
			t.Fatalf("roots = %+v, want %+v", derefSlice(gotCfg.Roots), newRoots)
		}
		origCfg.Roots = nil
		gotCfg.Roots = nil
		if !reflect.DeepEqual(origCfg, gotCfg) {
			t.Fatalf("keys outside roots changed: orig=%+v got=%+v", origCfg, gotCfg)
		}
	})
}
