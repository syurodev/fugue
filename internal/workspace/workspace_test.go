package workspace

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/pelletier/go-toml/v2"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	testCoreRoot        = "core"
	testMainRoot        = "main"
	testWorkspaceName   = "test"
	defaultApprovalMode = "auto"
)

func TestWorkspaceLayoutAndPermissions_WSE01(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := t.Context()

	root1Dir := filepath.Join(f.tempDir, "repo-one")
	if err := os.MkdirAll(root1Dir, 0o700); err != nil {
		t.Fatalf("mkdir root1: %v", err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name: "project-alpha",
		Roots: []RootSpec{
			{Name: testCoreRoot, Path: root1Dir, Access: AccessReadWrite},
		},
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	verifyWorkspaceMetadata(t, ws, f)
	verifyWorkspacePermissions(t, ws.ID, f.paths)
	verifyWorkspaceNaming(t, ws.ID, f.paths)
}

func verifyWorkspaceMetadata(t *testing.T, ws *Workspace, f *testFixture) {
	t.Helper()
	if !ids.IsUUID(ws.ID) {
		t.Errorf("expected UUIDv7 ID, got %q", ws.ID)
	}
	if ws.Name != "project-alpha" {
		t.Errorf("expected name 'project-alpha', got %q", ws.Name)
	}
	if ws.ConfigRevision != 1 || ws.PolicyRevision != 1 {
		t.Errorf("expected revisions (1, 1), got (%d, %d)", ws.ConfigRevision, ws.PolicyRevision)
	}
	if f.svc.Paths().FugueHome != f.paths.FugueHome {
		t.Errorf("Paths() = %+v, want %+v", f.svc.Paths(), f.paths)
	}
}

func verifyWorkspacePermissions(t *testing.T, id string, paths Paths) {
	t.Helper()
	wsDir := paths.WorkspaceDir(id)
	dirInfo, err := os.Stat(wsDir)
	if err != nil {
		t.Fatalf("stat wsDir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("wsDir perm = %04o, want 0700", dirInfo.Mode().Perm())
	}

	cfgInfo, err := os.Stat(paths.ConfigPath(id))
	if err != nil {
		t.Fatalf("stat fugue.toml: %v", err)
	}
	if cfgInfo.Mode().Perm() != 0o600 {
		t.Errorf("fugue.toml perm = %04o, want 0600", cfgInfo.Mode().Perm())
	}

	mdInfo, err := os.Stat(paths.WorkspaceMDPath(id))
	if err != nil {
		t.Fatalf("stat WORKSPACE.md: %v", err)
	}
	if mdInfo.Mode().Perm() != 0o600 {
		t.Errorf("WORKSPACE.md perm = %04o, want 0600", mdInfo.Mode().Perm())
	}
}

func verifyWorkspaceNaming(t *testing.T, id string, paths Paths) {
	t.Helper()
	// Directory path is based on UUID id, not name (WS-03)
	expectedDir := paths.WorkspaceDir(id)
	if !strings.HasSuffix(expectedDir, id) {
		t.Errorf("expected dir to end with UUID %q, got %q", id, expectedDir)
	}
	scratchDir := paths.ScratchDir(id)
	if st, err := os.Stat(scratchDir); err != nil || !st.IsDir() {
		t.Fatalf("expected scratch dir to exist, stat: %v", err)
	}
}

func TestWorkspaceRootValidation_WSE02(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	homeDir := filepath.Join(f.tempDir, "userhome")

	validRepo := filepath.Join(f.tempDir, "valid-repo")
	if err := os.MkdirAll(validRepo, 0o700); err != nil {
		t.Fatal(err)
	}

	testInvalidNamesAndAccess(t, f.svc, validRepo, homeDir)
	testInvalidPathsAndZones(t, f, homeDir)
	testNestingAndSharing(t, f.svc, validRepo, homeDir)
}

func testInvalidNamesAndAccess(t *testing.T, svc *Service, validRepo, homeDir string) {
	t.Helper()
	ctx := context.Background()

	invalidNames := []string{"", "UPPER", "invalid_underscore", "-startwithhyphen", "toolongnamethatexceedsthirtytwocharacterslong"}
	for _, name := range invalidNames {
		_, err := svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
			Name:    testWorkspaceName,
			Roots:   []RootSpec{{Name: name, Path: validRepo, Access: AccessReadWrite}},
			HomeDir: homeDir,
		})
		if err == nil || CodeOf(err) != ErrRootInvalid {
			t.Errorf("expected ErrRootInvalid for root name %q, got %v", name, err)
		}
	}

	_, err := svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    testWorkspaceName,
		Roots:   []RootSpec{{Name: testCoreRoot, Path: validRepo, Access: "invalid"}},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrRootInvalid {
		t.Errorf("expected ErrRootInvalid for invalid access, got %v", err)
	}
}

func testInvalidPathsAndZones(t *testing.T, f *testFixture, homeDir string) {
	t.Helper()
	ctx := context.Background()

	// Non-existent path
	_, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    testWorkspaceName,
		Roots:   []RootSpec{{Name: testCoreRoot, Path: filepath.Join(f.tempDir, "none"), Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrRootInvalid {
		t.Errorf("expected ErrRootInvalid for missing path, got %v", err)
	}

	// Root "/"
	_, err = f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    testWorkspaceName,
		Roots:   []RootSpec{{Name: "rootfs", Path: "/", Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrPathDenied {
		t.Errorf("expected ErrPathDenied for '/', got %v", err)
	}

	// Inside Fugue home
	inHome := filepath.Join(f.paths.FugueHome, "sub")
	if mkErr := os.MkdirAll(inHome, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	_, err = f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    testWorkspaceName,
		Roots:   []RootSpec{{Name: "in-home", Path: inHome, Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrPathDenied {
		t.Errorf("expected ErrPathDenied for path inside Fugue home, got %v", err)
	}

	// Inside data dir
	inData := filepath.Join(f.paths.DataDir, "sub")
	if mkErr := os.MkdirAll(inData, 0o700); mkErr != nil {
		t.Fatal(mkErr)
	}
	_, err = f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    testWorkspaceName,
		Roots:   []RootSpec{{Name: "in-data", Path: inData, Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrPathDenied {
		t.Errorf("expected ErrPathDenied for path inside data dir, got %v", err)
	}
}

func testNestingAndSharing(t *testing.T, svc *Service, validRepo, homeDir string) {
	t.Helper()
	ctx := context.Background()

	// Nested roots rejected
	subRepo := filepath.Join(validRepo, "nested-sub")
	if err := os.MkdirAll(subRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name: "test",
		Roots: []RootSpec{
			{Name: "parent", Path: validRepo, Access: AccessReadWrite},
			{Name: "child", Path: subRepo, Access: AccessReadWrite},
		},
		HomeDir: homeDir,
	})
	if err == nil || CodeOf(err) != ErrRootInvalid {
		t.Errorf("expected ErrRootInvalid for nested roots, got %v", err)
	}

	// Shared root between two workspaces allowed
	ws1, err := svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "ws1",
		Roots:   []RootSpec{{Name: "shared", Path: validRepo, Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err != nil {
		t.Fatalf("create ws1: %v", err)
	}
	ws2, err := svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "ws2",
		Roots:   []RootSpec{{Name: "shared", Path: validRepo, Access: AccessReadOnly}},
		HomeDir: homeDir,
	})
	if err != nil {
		t.Fatalf("create ws2: %v", err)
	}
	if ws1.ID == ws2.ID {
		t.Errorf("workspaces should have different IDs")
	}
}

func TestWorkspaceRootMissing_WSE05(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := t.Context()

	targetDir := filepath.Join(f.tempDir, "temp-repo-to-delete")
	if err := os.MkdirAll(targetDir, 0o700); err != nil {
		t.Fatal(err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name: "ws-missing-test",
		Roots: []RootSpec{
			{Name: "fragile", Path: targetDir, Access: AccessReadWrite},
		},
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	verifyCwdMatches(t, f.svc, ws.ID, "fragile", targetDir)

	if removeErr := os.RemoveAll(targetDir); removeErr != nil {
		t.Fatal(removeErr)
	}

	_, err = f.svc.SelectSessionCwd(ctx, ws.ID, "fragile")
	if err == nil || CodeOf(err) != ErrRootMissing {
		t.Fatalf("expected ErrRootMissing, got %v", err)
	}

	loaded, err := f.svc.GetWorkspace(ctx, ws.ID)
	if err != nil {
		t.Fatalf("GetWorkspace: %v", err)
	}
	verifyMissingRoot(t, loaded, "fragile")
}

func verifyCwdMatches(t *testing.T, svc *Service, wsID, rootName, wantDir string) {
	t.Helper()
	realDir, err := filepath.EvalSymlinks(wantDir)
	if err != nil {
		t.Fatal(err)
	}
	cwd, err := svc.SelectSessionCwd(t.Context(), wsID, rootName)
	if err != nil || cwd != realDir {
		t.Fatalf("SelectSessionCwd(%s): %v, got=%q, want=%q", rootName, err, cwd, realDir)
	}
}

func verifyMissingRoot(t *testing.T, ws *Workspace, rootName string) {
	t.Helper()
	if !slices.Contains(ws.MissingRoots, rootName) {
		t.Errorf("expected %q in missing_roots, got %v", rootName, ws.MissingRoots)
	}
	if len(ws.Roots) != 1 || ws.Roots[0].Name != rootName {
		t.Errorf("missing root was unexpectedly removed from configuration: %v", ws.Roots)
	}
}

func TestWorkspaceForeignUIDFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	foreignUID := os.Getuid() + 9999
	statSeam := func(info os.FileInfo) (int, error) {
		return foreignUID, nil
	}

	f := newTestFixture(t, WithStatUID(statSeam))

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "foreign-uid-test",
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err == nil {
		t.Fatalf("expected create to fail closed on foreign UID, got workspace %v", ws)
	}
	if code := CodeOf(err); code != ErrOwnershipInvalid {
		t.Fatalf("expected ErrOwnershipInvalid, got %q (%v)", code, err)
	}
}

func testSetMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func testWriteFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Clean(path), data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePermissiveModeChmoddedWithOwnerAudit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, nil))
	f := newTestFixture(t, WithLogger(logger))

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "permissive-test",
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	wsDir := f.paths.WorkspaceDir(ws.ID)
	cfgPath := f.paths.ConfigPath(ws.ID)

	// Loosen permissions on directory and file to group/world writable
	testSetMode(t, wsDir, 0o777)
	testSetMode(t, cfgPath, 0o666)

	if checkErr := CheckAndEnforcePermissions(ctx, f.paths, ws.ID, nil, logger); checkErr != nil {
		t.Fatalf("CheckAndEnforcePermissions: %v", checkErr)
	}

	dirInfo, err := os.Stat(wsDir)
	if err != nil || dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("wsDir perm = %04o, want 0700", dirInfo.Mode().Perm())
	}

	cfgInfo, err := os.Stat(cfgPath)
	if err != nil || cfgInfo.Mode().Perm() != 0o600 {
		t.Errorf("fugue.toml perm = %04o, want 0600", cfgInfo.Mode().Perm())
	}

	out := logBuf.String()
	if !strings.Contains(out, "tightened workspace directory permissions") {
		t.Errorf("expected dir audit log, got:\n%s", out)
	}
	if !strings.Contains(out, "tightened workspace file permissions") {
		t.Errorf("expected file audit log, got:\n%s", out)
	}
}

func TestSelectSessionCwd(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := t.Context()
	homeDir := filepath.Join(f.tempDir, "userhome")

	repo1 := filepath.Join(f.tempDir, "repo1")
	repo2 := filepath.Join(f.tempDir, "repo2")
	if err := os.MkdirAll(repo1, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(repo2, 0o700); err != nil {
		t.Fatal(err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name: "multi-root-ws",
		Roots: []RootSpec{
			{Name: "frontend", Path: repo1, Access: AccessReadWrite},
			{Name: "backend", Path: repo2, Access: AccessReadOnly},
		},
		HomeDir: homeDir,
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	verifyCwdMatches(t, f.svc, ws.ID, "frontend", repo1)

	_, err = f.svc.SelectSessionCwd(ctx, ws.ID, "unknown")
	if err == nil || CodeOf(err) != ErrRootInvalid {
		t.Errorf("expected ErrRootInvalid for unknown root, got %v", err)
	}

	_, err = f.svc.SelectSessionCwd(ctx, ws.ID, "")
	if err == nil || CodeOf(err) != ErrRootInvalid {
		t.Errorf("expected ErrRootInvalid for unspecified root on multi-root ws, got %v", err)
	}
}

func TestListWorkspaces(t *testing.T) {
	t.Parallel()
	f := newTestFixture(t)
	ctx := t.Context()
	homeDir := filepath.Join(f.tempDir, "userhome")

	repoA := filepath.Join(f.tempDir, "repo-a")
	if err := os.MkdirAll(repoA, 0o700); err != nil {
		t.Fatal(err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "listed-ws",
		Roots:   []RootSpec{{Name: "root-a", Path: repoA, Access: AccessReadWrite}},
		HomeDir: homeDir,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}

	list, listErr := f.svc.ListWorkspaces(ctx)
	if listErr != nil {
		t.Fatalf("ListWorkspaces failed: %v", listErr)
	}
	if len(list) != 1 || list[0].ID != ws.ID {
		t.Fatalf("expected 1 workspace in list with id %s, got %+v", ws.ID, list)
	}

	wsNoHome, noHomeErr := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name: "no-home-ws",
	})
	if noHomeErr != nil {
		t.Fatalf("CreateWorkspace without HomeDir failed: %v", noHomeErr)
	}
	if wsNoHome.Name != "no-home-ws" {
		t.Errorf("wsNoHome.Name = %q", wsNoHome.Name)
	}
}

func TestTypedErrors(t *testing.T) {
	t.Parallel()
	simpleErr := NewError(ErrNotFound, "simple error")
	if !strings.Contains(simpleErr.Error(), "workspace.not_found: simple error") {
		t.Errorf("simpleErr.Error() = %q", simpleErr.Error())
	}

	baseErr := sql.ErrNoRows
	wrapped := WrapError(ErrNotFound, "sample message", baseErr)
	if !strings.Contains(wrapped.Error(), "sample message") {
		t.Errorf("wrapped.Error() = %q", wrapped.Error())
	}
	if !errors.Is(wrapped, baseErr) {
		t.Errorf("expected wrapped error to unwrap to baseErr")
	}
	if CodeOf(wrapped) != ErrNotFound {
		t.Errorf("CodeOf(wrapped) = %q, want %q", CodeOf(wrapped), ErrNotFound)
	}
	if CodeOf(baseErr) != "" {
		t.Errorf("CodeOf(baseErr) = %q, want empty", CodeOf(baseErr))
	}
}

func TestPathsResolution(t *testing.T) {
	t.Parallel()
	temp := t.TempDir()
	homeDir := filepath.Join(temp, "custom-home")
	dataDir := filepath.Join(temp, "custom-data")

	envEmpty := config.MapEnv{}
	paths, err := ResolvePaths(envEmpty, func() (string, error) { return homeDir, nil }, dataDir)
	if err != nil {
		t.Fatalf("ResolvePaths default: %v", err)
	}
	expectedHome := filepath.Join(homeDir, "Fugue")
	if paths.FugueHome != expectedHome || paths.WorkspacesDir != filepath.Join(expectedHome, "workspaces") {
		t.Errorf("unexpected default paths: %+v", paths)
	}

	customFugueHome := filepath.Join(temp, "override-fugue-home")
	pathsOverride, err := ResolvePaths(config.MapEnv{config.EnvFugueHome: customFugueHome}, func() (string, error) { return homeDir, nil }, dataDir)
	if err != nil || pathsOverride.FugueHome != customFugueHome {
		t.Fatalf("ResolvePaths override failed: %v", err)
	}

	pathsOpt, err := ResolvePaths(envEmpty, func() (string, error) { return homeDir, nil }, dataDir, WithWorkspacesDir(filepath.Join(temp, "ws-opt")))
	if err != nil || pathsOpt.WorkspacesDir != filepath.Join(temp, "ws-opt") {
		t.Fatalf("WithWorkspacesDir failed: %v", err)
	}
}

func TestPermissionsNeverFollowSymlinks(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)

	extFile := filepath.Join(f.tempDir, "external-target.txt")
	testWriteFile(t, extFile, []byte("sensitive-data"))
	testSetMode(t, extFile, 0o644)

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "symlink-test",
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	// Symlink in scratch/ pointing to external 0644 file: must not be followed or chmodded.
	scratchLink := filepath.Join(f.paths.ScratchDir(ws.ID), "scratch-symlink.txt")
	if err := os.Symlink(extFile, scratchLink); err != nil {
		t.Fatal(err)
	}

	if _, err := f.svc.GetWorkspace(ctx, ws.ID); err != nil {
		t.Fatalf("GetWorkspace should succeed with symlink in scratch: %v", err)
	}
	verifyFileMode(t, extFile, 0o644)

	// Symlink instead of fugue.toml pointing to external 0644 file: must fail closed and NOT chmod.
	cfgPath := f.paths.ConfigPath(ws.ID)
	if err := os.Remove(cfgPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(extFile, cfgPath); err != nil {
		t.Fatal(err)
	}

	checkErr := CheckAndEnforcePermissions(ctx, f.paths, ws.ID, nil, nil)
	if checkErr == nil || CodeOf(checkErr) != ErrOwnershipInvalid {
		t.Fatalf("expected ErrOwnershipInvalid for symlink config, got %v", checkErr)
	}
	verifyFileMode(t, extFile, 0o644)
}

func verifyFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != want {
		t.Fatalf("target %s mode = %04o, want %04o", path, st.Mode().Perm(), want)
	}
}

func TestPermissionsDoNotWalkScratchContents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "scratch-nowalk-test",
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	scratchFile := filepath.Join(f.paths.ScratchDir(ws.ID), "sub-item.txt")
	testWriteFile(t, scratchFile, []byte("content"))
	testSetMode(t, scratchFile, 0o644)

	if err := CheckAndEnforcePermissions(ctx, f.paths, ws.ID, nil, nil); err != nil {
		t.Fatalf("CheckAndEnforcePermissions failed: %v", err)
	}
	verifyFileMode(t, scratchFile, 0o644)
}

func TestRootsFromFileAreRevalidated(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)

	validRepo := filepath.Join(f.tempDir, "valid-base-repo")
	if err := os.MkdirAll(validRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	nestedRepo := filepath.Join(validRepo, "nested-sub")
	if err := os.MkdirAll(nestedRepo, 0o700); err != nil {
		t.Fatal(err)
	}
	inHome := filepath.Join(f.paths.FugueHome, "forbidden")
	if err := os.MkdirAll(inHome, 0o700); err != nil {
		t.Fatal(err)
	}
	inData := filepath.Join(f.paths.DataDir, "forbidden")
	if err := os.MkdirAll(inData, 0o700); err != nil {
		t.Fatal(err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "revalidate-roots",
		Roots:   []RootSpec{{Name: testMainRoot, Path: validRepo, Access: AccessReadWrite}},
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	cases := []struct {
		name     string
		rootName string
		rootPath string
		wantCode Code
	}{
		{"filesystem root", "fsroot", "/", ErrPathDenied},
		{"inside Fugue home", "inhome", filepath.Join(f.paths.FugueHome, "forbidden"), ErrPathDenied},
		{"inside data dir", "indata", filepath.Join(f.paths.DataDir, "forbidden"), ErrPathDenied},
		{"nested root", "nested", nestedRepo, ErrRootInvalid},
		{"invalid name", "INVALID_NAME", validRepo, ErrRootInvalid},
	}

	for _, tc := range cases {
		testInvalidRootRevalidated(t, ctx, f, ws.ID, validRepo, tc.rootName, tc.rootPath, tc.wantCode)
	}
}

func testInvalidRootRevalidated(t *testing.T, ctx context.Context, f *testFixture, wsID, validRepo, rootName, rootPath string, wantCode Code) {
	t.Helper()
	cfg := tomlConfig{
		Name: "revalidate-roots",
		Roots: []Root{
			{Name: testMainRoot, Path: validRepo, Access: AccessReadWrite},
			{Name: rootName, Path: rootPath, Access: AccessReadWrite},
		},
	}
	encoded, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := f.paths.ConfigPath(wsID)
	testWriteFile(t, cfgPath, encoded)
	testWriteFile(t, f.paths.AcceptedConfigPath(wsID), encoded)
	hash := computeConfigHash(encoded)
	if _, execErr := f.db.ExecContext(ctx, "UPDATE workspaces SET accepted_config_hash = ?, config_hash = ? WHERE workspace_id = ?", hash, hash, wsID); execErr != nil {
		t.Fatal(execErr)
	}

	loaded, err := f.svc.GetWorkspace(ctx, wsID)
	if err != nil {
		t.Fatalf("GetWorkspace should return workspace even with invalid root: %v", err)
	}
	invErr, ok := loaded.InvalidRoots[rootName]
	if !ok {
		t.Fatalf("expected root %q to be in InvalidRoots, got %+v", rootName, loaded.InvalidRoots)
	}
	if CodeOf(invErr) != wantCode {
		t.Fatalf("expected code %s for root %q, got %s (%v)", wantCode, rootName, CodeOf(invErr), invErr)
	}

	_, cwdErr := f.svc.SelectSessionCwd(ctx, wsID, rootName)
	if cwdErr == nil || CodeOf(cwdErr) != wantCode {
		t.Fatalf("expected SelectSessionCwd to reject invalid root with %s, got %v", wantCode, cwdErr)
	}
}

func TestSelectSessionCwdRefusesUnconfirmedConfig(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newTestFixture(t)

	repo := filepath.Join(f.tempDir, "unconf-repo")
	if err := os.MkdirAll(repo, 0o700); err != nil {
		t.Fatal(err)
	}

	ws, err := f.svc.CreateWorkspace(ctx, CreateWorkspaceRequest{
		Name:    "unconf-test",
		Roots:   []RootSpec{{Name: "repo", Path: repo, Access: AccessReadWrite}},
		HomeDir: filepath.Join(f.tempDir, "userhome"),
	})
	if err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}

	cfg := tomlConfig{
		Name:  "unconf-test-edited",
		Roots: []Root{{Name: "repo", Path: repo, Access: AccessReadOnly}},
	}
	encoded, marshalErr := toml.Marshal(cfg)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	testWriteFile(t, f.paths.ConfigPath(ws.ID), encoded)

	_, err = f.svc.SelectSessionCwd(ctx, ws.ID, "repo")
	if err == nil || CodeOf(err) != ErrConfigUnconfirmed {
		t.Fatalf("expected ErrConfigUnconfirmed when config hash diverges, got %v", err)
	}
}
