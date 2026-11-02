package snapshot

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreKeepsSymlinksAndExecutableBit(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "target.txt", "payload\n")
	write(t, repo, "run.sh", "#!/bin/sh\n")
	chmod(t, repo, "run.sh", 0o750)
	link := filepath.Join(repo.WorkTree(), "link")
	if err := os.Symlink("target.txt", link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	before := snap(t, repo, "cp1", SlotBefore)

	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "link", "not a link any more\n")
	chmod(t, repo, "run.sh", 0o600)
	after := snap(t, repo, "cp1", SlotAfter)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	verifySymlinkAndExecutable(t, repo, link)
}

func verifySymlinkAndExecutable(t *testing.T, repo *Repo, link string) {
	t.Helper()
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Error("the symlink came back as a regular file")
	}
	if target, linkErr := os.Readlink(link); linkErr != nil || target != "target.txt" {
		t.Errorf("readlink = %q, %v; want target.txt", target, linkErr)
	}
	runInfo, err := os.Stat(filepath.Join(repo.WorkTree(), "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if runInfo.Mode()&0o111 == 0 {
		t.Error("the executable bit was not restored")
	}
}

func TestRestoreIsByteExactUnderProjectGitattributes(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, ".gitattributes", "* text=auto eol=crlf ident\n*.bin filter=lfs diff=lfs merge=lfs\n")
	sources := map[string]string{
		"text.txt":  "alpha\nbeta\ngamma\n",
		"stamp.txt": "header $Id$ footer\n",
		"mixed.bin": "alpha\nbeta\r\ngamma\n\x00trailing",
	}
	for path, content := range sources {
		write(t, repo, path, content)
	}
	before := snap(t, repo, "cp1", SlotBefore)

	for path := range sources {
		write(t, repo, path, "clobbered")
	}
	after := snap(t, repo, "cp1", SlotAfter)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	for path, want := range sources {
		if got := read(t, repo, path); got != want {
			t.Errorf("%s restored as %q, want %q", path, got, want)
		}
	}
}

func TestRestoreRefusesToWriteThroughASymlinkedDirectory(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "pkg/file.txt", "original\n")
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "pkg/file.txt", "changed\n")
	after := snap(t, repo, "cp1", SlotAfter)

	outside := t.TempDir()
	if err := os.RemoveAll(filepath.Join(repo.WorkTree(), "pkg")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(repo.WorkTree(), "pkg")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := conflictOn(plan, "pkg/file.txt")
	if !ok || conflict.Reason != ConflictUnsafePath {
		t.Fatalf("conflicts = %+v, want pkg/file.txt unsafe_path", plan.Conflicts)
	}
	if conflict.Reason.Overridable() {
		t.Error("an unsafe path must not be overridable")
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, []string{"pkg/file.txt"}); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("wrote through the symlink: %v %v", entries, err)
	}
}

func TestRestoreNeverTouchesASkippedPath(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{MaxFileBytes: 64})
	write(t, repo, "big.bin", "small\n")
	write(t, repo, "other.txt", contentOne)
	before := snap(t, repo, "cp1", SlotBefore)

	grown := strings.Repeat("Z", 200)
	write(t, repo, "big.bin", grown)
	write(t, repo, "other.txt", contentTwo)
	after := snap(t, repo, "cp1", SlotAfter)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := conflictOn(plan, "big.bin")
	if !ok || conflict.Reason != ConflictProtected {
		t.Fatalf("conflicts = %+v, want big.bin protected", plan.Conflicts)
	}
	if !hasActionOn(plan, "other.txt") {
		t.Fatalf("actions = %+v, want other.txt", plan.Actions)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, []string{"big.bin"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repo, "big.bin"); got != grown {
		t.Fatal("the skipped file was rewritten")
	}
	if got := read(t, repo, "other.txt"); got != contentOne {
		t.Fatalf("other.txt = %q, want the restored content", got)
	}
}

func TestRestoreRemovesOnlyTheDirectoriesItEmptied(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "keep/kept.txt", "stay\n")
	before := snap(t, repo, "cp1", SlotBefore)

	write(t, repo, "fresh/added.txt", "new\n")
	write(t, repo, "keep/extra.txt", "new\n")
	write(t, repo, "mixed/added.txt", "new\n")
	after := snap(t, repo, "cp1", SlotAfter)
	write(t, repo, "mixed/.DS_Store", "junk")

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo.WorkTree(), "fresh")); !os.IsNotExist(err) {
		t.Errorf("the emptied directory survived: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.WorkTree(), "keep", "kept.txt")); err != nil {
		t.Errorf("a directory with surviving content was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(repo.WorkTree(), "mixed", ".DS_Store")); err != nil {
		t.Errorf("a directory holding a skipped file was removed: %v", err)
	}
}

func TestRestoreReportsAndCanOverrideAConflict(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "file.txt", contentOne)
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "file.txt", contentTwo)
	after := snap(t, repo, "cp1", SlotAfter)

	write(t, repo, "file.txt", contentThree)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	conflict, ok := conflictOn(plan, "file.txt")
	if !ok || conflict.Reason != ConflictChangedSince {
		t.Fatalf("conflicts = %+v, want file.txt changed_since", plan.Conflicts)
	}
	if !plan.Empty() {
		t.Fatalf("actions = %+v, want none", plan.Actions)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repo, "file.txt"); got != contentThree {
		t.Fatalf("file.txt = %q, want the user's edit left alone", got)
	}
	if _, err := repo.ApplyRestore(context.Background(), plan, []string{"file.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repo, "file.txt"); got != contentOne {
		t.Fatalf("file.txt = %q, want the forced restore", got)
	}
}

func TestApplyRefusesAStalePlan(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "file.txt", contentOne)
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "file.txt", contentTwo)
	after := snap(t, repo, "cp1", SlotAfter)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasActionOn(plan, "file.txt") {
		t.Fatalf("actions = %+v, want file.txt", plan.Actions)
	}

	write(t, repo, "file.txt", contentThree)

	if _, err := repo.ApplyRestore(context.Background(), plan, nil); !errors.Is(err, ErrPlanStale) {
		t.Fatalf("err = %v, want ErrPlanStale", err)
	}
	if got := read(t, repo, "file.txt"); got != contentThree {
		t.Fatalf("file.txt = %q, want it untouched", got)
	}
}

func TestPlanRestoreNarrowsToTheGivenPaths(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "mine.txt", contentOne)
	write(t, repo, "theirs.txt", contentOne)
	before := snap(t, repo, "cp1", SlotBefore)

	write(t, repo, "mine.txt", contentTwo)
	write(t, repo, "theirs.txt", contentTwo)
	after := snap(t, repo, "cp1", SlotAfter)

	narrowed, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, []string{"mine.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(narrowed.Actions) != 1 || narrowed.Actions[0].Path != "mine.txt" {
		t.Fatalf("actions = %+v, want only mine.txt", narrowed.Actions)
	}

	wide, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wide.Actions) != 2 {
		t.Fatalf("actions = %+v, want both", wide.Actions)
	}

	if _, err := repo.ApplyRestore(context.Background(), narrowed, nil); err != nil {
		t.Fatal(err)
	}
	if got := read(t, repo, "mine.txt"); got != contentOne {
		t.Errorf("mine.txt = %q, want restored", got)
	}
	if got := read(t, repo, "theirs.txt"); got != contentTwo {
		t.Errorf("theirs.txt = %q, want untouched", got)
	}
}

func TestPlanRestoreTreatsAnEmptyScopeAsNothing(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", contentOne)
	before := snap(t, repo, "cp1", SlotBefore)
	write(t, repo, "a.txt", contentTwo)
	after := snap(t, repo, "cp1", SlotAfter)

	plan, err := repo.PlanRestore(context.Background(), before.Tree, after.Tree, []string{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Empty() || len(plan.Conflicts) != 0 {
		t.Fatalf("plan = %+v, want nothing", plan)
	}
}

func TestFileContentRefusesAFileOverTheCeiling(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	small := "one\ntwo\n"
	write(t, repo, "small.txt", small)
	write(t, repo, "big.txt", strings.Repeat("x", 4096))
	snapshot := snap(t, repo, "cp1", SlotBefore)

	body, tooLarge, err := repo.FileContent(context.Background(), snapshot.Tree, "small.txt", 1024)
	if err != nil || tooLarge || string(body) != small {
		t.Fatalf("small.txt = %q, tooLarge=%v, err=%v", string(body), tooLarge, err)
	}

	body, tooLarge, err = repo.FileContent(context.Background(), snapshot.Tree, "big.txt", 1024)
	if err != nil || !tooLarge || len(body) != 0 {
		t.Fatalf("big.txt = %d bytes, tooLarge=%v, err=%v", len(body), tooLarge, err)
	}

	body, tooLarge, err = repo.FileContent(context.Background(), snapshot.Tree, "big.txt", 0)
	if err != nil || tooLarge || len(body) != 4096 {
		t.Fatalf("unbounded read = %d bytes, tooLarge=%v, %v", len(body), tooLarge, err)
	}
}

// TestRestoreToolTouchesOnlyTheToolsDiff verifies tool-system §9: each tool
// invocation gets an opaque snapshot ID (with tree hashes kept inside the
// store), and restoring a single tool call touches only the files in that
// tool's before/after diff while leaving edits from other tools or the user
// intact.
func TestRestoreToolTouchesOnlyTheToolsDiff(t *testing.T) {
	t.Parallel()
	engine, repo := testEngine(t, Limits{})
	ctx := context.Background()
	store := NewMemoryStore()

	write(t, repo, "tool1.txt", "before-tool1\n")
	write(t, repo, "shared.txt", "before-tool1\n")
	write(t, repo, "other.txt", "before-all\n")

	rec1, _, err := engine.CaptureToolBefore(ctx, store, "sess-1", repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	if rec1.ID == "" || rec1.ID == rec1.BeforeTree {
		t.Fatalf("snapshot ID %q must be an opaque random ID, not the tree hash %q", rec1.ID, rec1.BeforeTree)
	}

	// Tool 1 edits tool1.txt and shared.txt, leaving other.txt untouched.
	write(t, repo, "tool1.txt", "after-tool1\n")
	write(t, repo, "shared.txt", "after-tool1\n")
	rec1, _, err = engine.CaptureToolAfter(ctx, store, rec1.ID, repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}

	// Later, tool 2 edits other.txt and shared.txt.
	write(t, repo, "other.txt", "after-tool2\n")
	write(t, repo, "shared.txt", "after-tool2\n")

	touched, plan, err := engine.RestoreTool(ctx, store, rec1.ID, repo.WorkTree(), nil)
	if err != nil {
		t.Fatal(err)
	}
	assertToolRestoreOutcome(t, repo, touched, plan)
}

func assertToolRestoreOutcome(t *testing.T, repo *Repo, touched []string, plan *RestorePlan) {
	t.Helper()
	if len(touched) != 1 || touched[0] != "tool1.txt" {
		t.Fatalf("touched = %v, want only [tool1.txt]", touched)
	}
	conflict, ok := conflictOn(plan, "shared.txt")
	if !ok || conflict.Reason != ConflictChangedSince {
		t.Fatalf("conflicts = %+v, want shared.txt changed_since", plan.Conflicts)
	}
	if got := read(t, repo, "tool1.txt"); got != "before-tool1\n" {
		t.Fatalf("tool1.txt = %q, want before-tool1", got)
	}
	if got := read(t, repo, "other.txt"); got != "after-tool2\n" {
		t.Fatalf("other.txt = %q, want untouched after-tool2", got)
	}
	if got := read(t, repo, "shared.txt"); got != "after-tool2\n" {
		t.Fatalf("shared.txt = %q, want conflicting file left alone", got)
	}
}
