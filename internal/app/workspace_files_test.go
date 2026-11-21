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

// TestReadWorkspaceFileConfinesToRegisteredRoots proves HA-E09 (F10.4e): a
// range read answers the exact lines with total_lines; "..", absolute paths
// and symlinks resolving outside the root are refused with
// workspace.path_outside_root; over-limit and binary files are refused with
// typed codes; the session principal (and every non-app client kind) is
// denied; and ListWorkspaceDir lists sorted with the 1,000-entry cap.
func TestReadWorkspaceFileConfinesToRegisteredRoots(t *testing.T) {
	t.Parallel()
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
	body := make([]string, 0, 300)
	for i := 1; i <= 300; i++ {
		body = append(body, "line "+strconv.Itoa(i))
	}
	if err := os.WriteFile(filepath.Join(rootDir, "notes.md"), []byte(strings.Join(body, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "nested", "inner.go"), []byte("package nested\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "binary.dat"), append([]byte("ok\n"), make([]byte, 64)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootDir, "big.txt"), []byte(strings.Repeat("x", workspace.FileReadMaxBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(t.TempDir(), "outside")
	if err := os.MkdirAll(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsideDir, "secret.txt"), []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsideDir, "secret.txt"), filepath.Join(rootDir, "link-out.txt")); err != nil {
		t.Fatal(err)
	}
	if _, addErr := rt.AddWorkspaceRoot(ctx, string(KindApp), created.ID, "main", rootDir, "rw", created.ConfigHash); addErr != nil {
		t.Fatal(addErr)
	}

	// A whole read answers the file with its language hint and line count.
	whole, wholeErr := rt.ReadWorkspaceFile(ctx, string(KindApp), created.ID, "main", "notes.md", 0, 0)
	if wholeErr != nil {
		t.Fatalf("whole read: %v", wholeErr)
	}
	if whole.TotalLines != 300 || whole.Language != "markdown" || whole.Truncated {
		t.Fatalf("whole read = (%d lines, %q, truncated %v)", whole.TotalLines, whole.Language, whole.Truncated)
	}
	if !strings.HasPrefix(whole.Content, "line 1\n") || !strings.HasSuffix(whole.Content, "line 300") {
		t.Fatalf("whole read content edges do not match: %q...", whole.Content[:32])
	}

	// A range read answers exactly that range and marks the rest cut.
	ranged, rangeErr := rt.ReadWorkspaceFile(ctx, string(KindApp), created.ID, "main", "notes.md", 10, 19)
	if rangeErr != nil {
		t.Fatalf("range read: %v", rangeErr)
	}
	if !ranged.Truncated || ranged.TotalLines != 300 {
		t.Fatalf("range read = (%d lines, truncated %v)", ranged.TotalLines, ranged.Truncated)
	}
	wantRange := strings.Join(body[9:19], "\n")
	if ranged.Content != wantRange {
		t.Fatalf("range read content = %q, want %q", ranged.Content, wantRange)
	}

	// A nested read stays confined inside the root.
	if _, nestedErr := rt.ReadWorkspaceFile(ctx, string(KindApp), created.ID, "main", "nested/inner.go", 0, 0); nestedErr != nil {
		t.Fatalf("nested read: %v", nestedErr)
	}

	// Escapes, absolute paths and outside symlinks are refused with one code.
	for name, tc := range map[string]struct {
		path string
		code errcode.Code
	}{
		"dotdot":     {"../outside/secret.txt", errcode.WorkspacePathOutsideRoot},
		"deepdotdot": {"nested/../../outside/secret.txt", errcode.WorkspacePathOutsideRoot},
		"absolute":   {filepath.Join(outsideDir, "secret.txt"), errcode.WorkspacePathOutsideRoot},
		"symlink":    {"link-out.txt", errcode.WorkspacePathOutsideRoot},
		"missing":    {"nope.md", errcode.WorkspaceFileNotFound},
		"binary":     {"binary.dat", errcode.WorkspaceNotText},
		"oversize":   {"big.txt", errcode.WorkspaceFileTooLarge},
	} {
		_, readErr := rt.ReadWorkspaceFile(ctx, string(KindApp), created.ID, "main", tc.path, 0, 0)
		if errcode.CodeOf(readErr) != tc.code {
			t.Fatalf("%s read error = %v, want %v", name, readErr, tc.code)
		}
	}

	// Every non-app principal is denied, fail closed, even for an existing file.
	for _, principal := range []string{"session", string(KindTUI), string(KindCLI), string(KindAppBackground)} {
		if _, deniedErr := rt.ReadWorkspaceFile(ctx, principal, created.ID, "main", "notes.md", 0, 0); errcode.CodeOf(deniedErr) != errcode.PermissionDenied {
			t.Fatalf("%s file read error = %v, want permission_denied", principal, deniedErr)
		}
		if _, deniedErr := rt.ListWorkspaceDir(ctx, principal, created.ID, "main", "."); errcode.CodeOf(deniedErr) != errcode.PermissionDenied {
			t.Fatalf("%s dir list error = %v, want permission_denied", principal, deniedErr)
		}
	}

	// A listing answers sorted names with kinds, capped at 1,000 entries.
	entries, listErr := rt.ListWorkspaceDir(ctx, string(KindApp), created.ID, "main", ".")
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

	// More than 1,000 entries list exactly the cap, in sorted order.
	crowded := filepath.Join(rootDir, "nested", "crowd")
	if err := os.MkdirAll(crowded, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range 1100 {
		if err := os.WriteFile(filepath.Join(crowded, strconv.Itoa(i)+".txt"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	crowd, crowdErr := rt.ListWorkspaceDir(ctx, string(KindApp), created.ID, "main", "nested/crowd")
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

	// An unknown root or workspace fails typed, not with a leak.
	if _, rootErr := rt.ReadWorkspaceFile(ctx, string(KindApp), created.ID, "nope", "notes.md", 0, 0); errcode.CodeOf(rootErr) != errcode.WorkspaceRootInvalid {
		t.Fatalf("unknown root error = %v", rootErr)
	}
	if _, wsErr := rt.ReadWorkspaceFile(ctx, string(KindApp), "ws-none", "main", "notes.md", 0, 0); errcode.CodeOf(wsErr) != errcode.WorkspaceNotFound {
		t.Fatalf("unknown workspace error = %v", wsErr)
	}
}
