package snapshot

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	testWithSpaceFile = "with space.txt"
	testSubFile       = "p/q.txt"
)

func TestFastImportColdAndWarmPreservesInvariants(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	engine, repo := testEngineWithClock(t, Limits{MaxFileBytes: 1024}, clock.NewFake(now))
	seedFastImportFixture(t, repo, 300)

	before := snap(t, repo, "cp-fi-1", SlotBefore)
	assertFastImportBeforeTree(t, repo, before)

	// Mutate a few entries and verify warm snapshot + diff + restore + maintain.
	write(t, repo, "bulk/f000.txt", "updated-0\n")
	if err := os.Remove(filepath.Join(repo.WorkTree(), "bulk", "f001.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "bulk/added.txt", "brand-new\n")
	after := snap(t, repo, "cp-fi-1", SlotAfter)

	assertFastImportDiffAndRestore(t, repo, before.Tree, after.Tree)
	assertFastImportMaintain(t, engine, repo, before.Tree, now)
}

func seedFastImportFixture(t *testing.T, repo *Repo, count int) {
	t.Helper()
	for i := range count {
		write(t, repo, fmt.Sprintf("bulk/f%03d.txt", i), fmt.Sprintf("body-%d\n", i))
	}
	write(t, repo, "exec.sh", "#!/bin/sh\necho ok\n")
	chmod(t, repo, "exec.sh", 0o750)
	if err := os.Symlink("bulk/f000.txt", filepath.Join(repo.WorkTree(), "sym-ok")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("does-not-exist", filepath.Join(repo.WorkTree(), "sym-dangling")); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		testWithSpaceFile,
		`with"quote.txt`,
		`"leadquote.txt`,
		`with\backslash.txt`,
		"sub/utf8-日本語.txt",
	} {
		write(t, repo, rel, "special:"+rel)
	}
	write(t, repo, "node_modules/ignored.js", "ignored\n")
	write(t, repo, ".DS_Store", "junk")
	write(t, repo, "too-big.bin", strings.Repeat("X", 2048))

	nested := filepath.Join(repo.WorkTree(), "nested-repo")
	if err := os.MkdirAll(nested, 0o750); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "git")
	cmd.Args = []string{"git", "init", "-q", nested}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init nested: %v: %s", err, out)
	}
	write(t, repo, "nested-repo/inside.txt", "nested\n")
}

func assertFastImportBeforeTree(t *testing.T, repo *Repo, before Snapshot) {
	t.Helper()
	entries, err := repo.readTree(t.Context(), before.Tree)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 307 {
		t.Fatalf("tree entry count = %d, want >= 307", len(entries))
	}
	wantModes := map[string]string{
		"exec.sh":      gitModeExec,
		"sym-ok":       gitModeSymlink,
		"sym-dangling": gitModeSymlink,
	}
	for path, want := range wantModes {
		if got := entries[path].mode; got != want {
			t.Fatalf("%s mode = %q, want %q", path, got, want)
		}
	}
	for _, forbidden := range []string{"node_modules/ignored.js", ".DS_Store", "too-big.bin", "nested-repo/inside.txt"} {
		if _, exists := entries[forbidden]; exists {
			t.Fatalf("excluded/skipped path leaked into tree: %s", forbidden)
		}
	}
	assertFastImportSpecialFiles(t, repo, before)
}

func assertFastImportSpecialFiles(t *testing.T, repo *Repo, before Snapshot) {
	t.Helper()
	for _, rel := range []string{testWithSpaceFile, `with"quote.txt`, `"leadquote.txt`, `with\backslash.txt`, "sub/utf8-日本語.txt"} {
		raw, tooLarge, readErr := repo.FileContent(t.Context(), before.Tree, rel, 4096)
		if readErr != nil || tooLarge || string(raw) != "special:"+rel {
			t.Fatalf("FileContent(%q) = %q, tooLarge=%v, err=%v", rel, string(raw), tooLarge, readErr)
		}
	}
	if skipReason(before, "too-big.bin") != SkipTooLarge {
		t.Fatalf("missing SkipTooLarge in %+v", before.Skipped)
	}
	if skipReason(before, "nested-repo") != SkipNestedRepo {
		t.Fatalf("missing SkipNestedRepo in %+v", before.Skipped)
	}
}

func assertFastImportDiffAndRestore(t *testing.T, repo *Repo, beforeTree, afterTree string) {
	t.Helper()
	changes, err := repo.Diff(t.Context(), beforeTree, afterTree)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]ChangeKind, len(changes))
	for _, c := range changes {
		kinds[c.Path] = c.Kind
	}
	if kinds["bulk/f000.txt"] != ChangeModified || kinds["bulk/f001.txt"] != ChangeDeleted || kinds["bulk/added.txt"] != ChangeAdded {
		t.Fatalf("unexpected diff kinds: %+v", kinds)
	}
	plan, err := repo.PlanRestore(t.Context(), beforeTree, afterTree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyRestore(t.Context(), plan, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repo, "bulk/f000.txt"); got != "body-0\n" {
		t.Fatalf("restored bulk/f000.txt = %q", got)
	}
	if got := read(t, repo, "bulk/f001.txt"); got != "body-1\n" {
		t.Fatalf("restored bulk/f001.txt = %q", got)
	}
}

func assertFastImportMaintain(t *testing.T, engine *Engine, repo *Repo, keepTree string, now time.Time) {
	t.Helper()
	store := NewMemoryStore()
	if err := store.PutSnapshot(t.Context(), &Record{
		ID: "cp-fi-1", SessionID: "s1", ProjectKey: repo.Key(), Kind: KindTurn, BeforeTree: keepTree, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	report, err := engine.Maintain(t.Context(), store)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(report.PackedProjects, repo.Key()) {
		t.Fatalf("Maintain did not pack project %s: %+v", repo.Key(), report)
	}
	if !contains(treePaths(t, repo, keepTree), "bulk/f000.txt") {
		t.Fatal("kept tree lost bulk/f000.txt after Maintain")
	}
}

func TestFastImportIncrementalBulkAddWithTrackedChanges(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{MaxFileBytes: 512})
	write(t, repo, "keep.txt", "keep\n")
	write(t, repo, "drop.txt", "drop\n")
	write(t, repo, "grow.txt", "small\n")
	if err := os.Symlink("keep.txt", filepath.Join(repo.WorkTree(), "replaced")); err != nil {
		t.Fatal(err)
	}
	snap(t, repo, "base", SlotBefore)

	if err := os.Remove(filepath.Join(repo.WorkTree(), "drop.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "grow.txt", strings.Repeat("G", 1024))
	if err := os.Remove(filepath.Join(repo.WorkTree(), "replaced")); err != nil {
		t.Fatal(err)
	}
	for i := range 300 {
		write(t, repo, fmt.Sprintf("replaced/n%03d.txt", i), "new\n")
	}

	bulk := snap(t, repo, "bulk", SlotAfter)
	paths := treePaths(t, repo, bulk.Tree)
	if !contains(paths, "keep.txt") || !contains(paths, "replaced/n299.txt") {
		t.Fatalf("missing expected paths in bulk tree (len=%d)", len(paths))
	}
	for _, absent := range []string{"drop.txt", "grow.txt", "replaced"} {
		if contains(paths, absent) {
			t.Fatalf("removed/replaced path stayed in bulk tree: %s", absent)
		}
	}
	if skipReason(bulk, "grow.txt") != SkipTooLarge {
		t.Fatalf("expected SkipTooLarge for grow.txt, got %+v", bulk.Skipped)
	}
}

func TestIsDeniedZeroAllocAndEdgeCases(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	cases := []struct {
		path string
		want bool
	}{
		{"node_modules", true},
		{"node_modules/", true},
		{"a/node_modules/pkg/index.js", true},
		{"src/.git/config", true},
		{".DS_Store", true},
		{"sub/.DS_Store", true},
		{"sub/.DS_Store/", true},
		{"Thumbs.db", true},
		{"", false},
		{"/", false},
		{"node_modules_extra/a.go", false},
		{"src/distribution.go", false},
		{".DS_Store.bak", false},
	}
	for _, tc := range cases {
		if got := repo.isDenied(tc.path); got != tc.want {
			t.Errorf("isDenied(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestDropReplacedByDirectoryFiltersByTracked(t *testing.T) {
	t.Parallel()
	untracked := []candidateEntry{{rel: "a/b/c.txt", mode: gitModeRegular}}
	if got := dropReplacedByDirectory(nil, []string{"x"}, []string{testSubFile}, untracked); len(got) != 1 || got[0] != "x" {
		t.Fatalf("empty tracked modified toRemove: %v", got)
	}
	tracked := []string{"a", testSubFile}
	got := dropReplacedByDirectory(tracked, []string{"existing"}, []string{testSubFile}, untracked)
	if !slices.Equal(got, []string{"existing", "a"}) {
		t.Fatalf("got = %v, want [existing a]", got)
	}
}

func TestWriteFastImportPathEscapesSpecialBytes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"plain/file.go", "plain/file.go"},
		{testWithSpaceFile, testWithSpaceFile},
		{`"leading.txt`, `"\"leading.txt"`},
		{"a\"b\\c\nd\re\x01f\x7fg", `"a\"b\\c\nd\re\001f\177g"`},
	}
	for _, tc := range cases {
		var buf bytes.Buffer
		writeFastImportPath(&buf, tc.in)
		if got := buf.String(); got != tc.want {
			t.Errorf("writeFastImportPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestReadEntryPayloadHandlesMissingAndTooLarge(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{MaxFileBytes: 16})
	write(t, repo, "big.bin", strings.Repeat("Z", 32))
	wsRoot, err := os.OpenRoot(repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	defer closeStreamQuietly(wsRoot)

	if _, skip, ok := repo.readEntryPayload(wsRoot, candidateEntry{rel: "missing.txt", mode: gitModeRegular}); ok || skip == nil || skip.Reason != SkipUnreadable {
		t.Fatalf("missing regular = (%v, %+v), want SkipUnreadable", ok, skip)
	}
	if _, skip, ok := repo.readEntryPayload(wsRoot, candidateEntry{rel: "missing-link", mode: gitModeSymlink}); ok || skip == nil || skip.Reason != SkipUnreadable {
		t.Fatalf("missing symlink = (%v, %+v), want SkipUnreadable", ok, skip)
	}
	if _, skip, ok := repo.readEntryPayload(wsRoot, candidateEntry{rel: "big.bin", mode: gitModeRegular}); ok || skip == nil || skip.Reason != SkipTooLarge {
		t.Fatalf("oversized = (%v, %+v), want SkipTooLarge", ok, skip)
	}
}
