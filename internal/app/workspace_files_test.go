package app

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

// fileReadFixture is one host with a workspace whose single root main holds
// the files the HA-E09 assertions read: a 300-line text file, a nested text
// file, a binary file, an over-limit file, and a symlink pointing outside.
type fileReadFixture struct {
	rt         *HostRuntime
	ws         *WorkspaceView
	rootDir    string
	outsideDir string
	lines      []string
}

func newFileReadFixture(t *testing.T) *fileReadFixture {
	t.Helper()
	ctx := t.Context()
	dataDir := t.TempDir()
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(dataDir, clock.NewFake(time.Unix(100, 0)), newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	host, startErr := StartHost(ctx, deps)
	if startErr != nil {
		t.Fatal(startErr)
	}
	t.Cleanup(func() {
		if closeErr := host.Close(); closeErr != nil {
			t.Errorf("close host: %v", closeErr)
		}
	})
	rt := host.Runtime()

	created, createErr := rt.CreateWorkspace(ctx, string(KindApp), "file-reads")
	if createErr != nil {
		t.Fatal(createErr)
	}
	rootDir := filepath.Join(t.TempDir(), "main")
	if err := os.MkdirAll(filepath.Join(rootDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	lines := make([]string, 0, 300)
	for i := 1; i <= 300; i++ {
		lines = append(lines, "line "+strconv.Itoa(i))
	}
	f := &fileReadFixture{rt: rt, ws: created, rootDir: rootDir, lines: lines}
	f.write(t, "notes.md", strings.Join(lines, "\n")+"\n")
	f.write(t, filepath.Join("nested", "inner.go"), "package nested\n")
	f.write(t, "binary.dat", string(append([]byte("ok\n"), make([]byte, 64)...)))
	f.write(t, "big.txt", strings.Repeat("x", workspace.FileReadMaxBytes+1))
	f.outsideDir = filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(f.outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.outsideDir, "secret.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(f.outsideDir, "secret.txt"), filepath.Join(rootDir, "link-out.txt")); err != nil {
		t.Fatal(err)
	}
	if _, addErr := rt.AddWorkspaceRoot(ctx, string(KindApp), created.ID, "main", rootDir, "rw", created.ConfigHash); addErr != nil {
		t.Fatal(addErr)
	}
	return f
}

func (f *fileReadFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(f.rootDir, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestReadWorkspaceFileConfinesToRegisteredRoots proves HA-E09 (F10.4e): a
// range read answers the exact lines with total_lines; "..", absolute paths
// and symlinks resolving outside the root are refused with
// workspace.path_outside_root; over-limit and binary files are refused with
// typed codes; the session principal (and every non-app client kind) is
// denied; and ListWorkspaceDir lists sorted with the 1,000-entry cap.
func TestReadWorkspaceFileConfinesToRegisteredRoots(t *testing.T) {
	t.Parallel()
	f := newFileReadFixture(t)

	assertFileReadRangeAndTotals(t, f)
	assertFileReadConfinesPaths(t, f)
	assertFileReadDeniesNonAppPrincipals(t, f)
	assertWorkspaceDirListsSortedWithKinds(t, f)
	assertWorkspaceDirListCapsAtThousandEntries(t, f)
}

// assertFileReadRangeAndTotals covers the whole and range reads: language
// hint, total_lines, the exact requested slice, and the truncated flag.
func assertFileReadRangeAndTotals(t *testing.T, f *fileReadFixture) {
	t.Helper()
	ctx := t.Context()
	whole, wholeErr := f.rt.ReadWorkspaceFile(ctx, string(KindApp), f.ws.ID, "main", "notes.md", 0, 0)
	if wholeErr != nil {
		t.Fatalf("whole read: %v", wholeErr)
	}
	if whole.TotalLines != 300 || whole.Language != "markdown" || whole.Truncated {
		t.Fatalf("whole read = (%d lines, %q, truncated %v)", whole.TotalLines, whole.Language, whole.Truncated)
	}
	if !strings.HasPrefix(whole.Content, "line 1\n") || !strings.HasSuffix(whole.Content, "line 300") {
		t.Fatalf("whole read content edges do not match: %q...", whole.Content[:32])
	}

	ranged, rangeErr := f.rt.ReadWorkspaceFile(ctx, string(KindApp), f.ws.ID, "main", "notes.md", 10, 19)
	if rangeErr != nil {
		t.Fatalf("range read: %v", rangeErr)
	}
	if !ranged.Truncated || ranged.TotalLines != 300 {
		t.Fatalf("range read = (%d lines, truncated %v)", ranged.TotalLines, ranged.Truncated)
	}
	if want := strings.Join(f.lines[9:19], "\n"); ranged.Content != want {
		t.Fatalf("range read content = %q, want %q", ranged.Content, want)
	}

	if _, nestedErr := f.rt.ReadWorkspaceFile(ctx, string(KindApp), f.ws.ID, "main", "nested/inner.go", 0, 0); nestedErr != nil {
		t.Fatalf("nested read: %v", nestedErr)
	}
}

// assertFileReadConfinesPaths covers the refusals: escapes, absolute paths,
// outside symlinks, missing, binary, over-limit, and unknown root or
// workspace, each with its typed code.
func assertFileReadConfinesPaths(t *testing.T, f *fileReadFixture) {
	t.Helper()
	ctx := t.Context()
	for name, tc := range map[string]struct {
		path string
		code errcode.Code
	}{
		"dotdot":      {"../outside/secret.txt", errcode.WorkspacePathOutsideRoot},
		"deepdotdot":  {"nested/../../outside/secret.txt", errcode.WorkspacePathOutsideRoot},
		"absolute":    {filepath.Join(f.outsideDir, "secret.txt"), errcode.WorkspacePathOutsideRoot},
		"symlink":     {"link-out.txt", errcode.WorkspacePathOutsideRoot},
		"missing":     {"nope.md", errcode.WorkspaceFileNotFound},
		"binary":      {"binary.dat", errcode.WorkspaceNotText},
		"oversize":    {"big.txt", errcode.WorkspaceFileTooLarge},
		"unknownroot": {"notes.md", errcode.WorkspaceRootInvalid},
	} {
		root := "main"
		if name == "unknownroot" {
			root = "nope"
		}
		_, readErr := f.rt.ReadWorkspaceFile(ctx, string(KindApp), f.ws.ID, root, tc.path, 0, 0)
		if errcode.CodeOf(readErr) != tc.code {
			t.Fatalf("%s read error = %v, want %v", name, readErr, tc.code)
		}
	}
	if _, wsErr := f.rt.ReadWorkspaceFile(ctx, string(KindApp), "ws-none", "main", "notes.md", 0, 0); errcode.CodeOf(wsErr) != errcode.WorkspaceNotFound {
		t.Fatalf("unknown workspace error = %v", wsErr)
	}
}

// assertFileReadDeniesNonAppPrincipals covers the fail-closed gate: every
// principal but the app client kind is refused, even for an existing file.
func assertFileReadDeniesNonAppPrincipals(t *testing.T, f *fileReadFixture) {
	t.Helper()
	ctx := t.Context()
	for _, principal := range []string{PrincipalSession, string(KindTUI), string(KindCLI), string(KindAppBackground)} {
		if _, deniedErr := f.rt.ReadWorkspaceFile(ctx, principal, f.ws.ID, "main", "notes.md", 0, 0); errcode.CodeOf(deniedErr) != errcode.PermissionDenied {
			t.Fatalf("%s file read error = %v, want permission_denied", principal, deniedErr)
		}
		if _, deniedErr := f.rt.ListWorkspaceDir(ctx, principal, f.ws.ID, "main", "."); errcode.CodeOf(deniedErr) != errcode.PermissionDenied {
			t.Fatalf("%s dir list error = %v, want permission_denied", principal, deniedErr)
		}
	}
}

// assertWorkspaceDirListsSortedWithKinds covers the root listing: sorted
// names with kinds and sizes.
func assertWorkspaceDirListsSortedWithKinds(t *testing.T, f *fileReadFixture) {
	t.Helper()
	entries, listErr := f.rt.ListWorkspaceDir(t.Context(), string(KindApp), f.ws.ID, "main", ".")
	if listErr != nil {
		t.Fatal(listErr)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	if len(entries) != 5 || !slices.IsSorted(names) {
		t.Fatalf("root listing = %v (len %d)", names, len(entries))
	}
	if entries[0].Name != "big.txt" || entries[0].Kind != "file" || entries[0].Size != workspace.FileReadMaxBytes+1 {
		t.Fatalf("first entry = %+v", entries[0])
	}
	if entries[3].Name != "nested" || entries[3].Kind != "dir" {
		t.Fatalf("nested entry = %+v", entries[3])
	}
}

// assertWorkspaceDirListCapsAtThousandEntries covers the cap: more than
// 1,000 entries list exactly the cap, in sorted order.
func assertWorkspaceDirListCapsAtThousandEntries(t *testing.T, f *fileReadFixture) {
	t.Helper()
	crowded := filepath.Join(f.rootDir, "nested", "crowd")
	if err := os.MkdirAll(crowded, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 1100 {
		if err := os.WriteFile(filepath.Join(crowded, strconv.Itoa(i)+".txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	crowd, crowdErr := f.rt.ListWorkspaceDir(t.Context(), string(KindApp), f.ws.ID, "main", "nested/crowd")
	if crowdErr != nil {
		t.Fatal(crowdErr)
	}
	if len(crowd) != workspace.DirListMaxEntries {
		t.Fatalf("crowded listing = %d entries, want %d", len(crowd), workspace.DirListMaxEntries)
	}
	for i := 1; i < len(crowd); i++ {
		if crowd[i-1].Name >= crowd[i].Name {
			t.Fatalf("crowded listing not sorted at %d: %s >= %s", i, crowd[i-1].Name, crowd[i].Name)
		}
	}
}
