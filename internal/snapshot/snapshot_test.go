package snapshot

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/supervise"
)

func TestDenyListBeatsGitignoreNegation(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		gitignore string
	}{
		{"silent", "*.log\n"},
		{"re-includes the directory", "node_modules/\n!node_modules/\n!node_modules/keep.js\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, repo := testEngine(t, Limits{})
			write(t, repo, ".gitignore", tc.gitignore)
			write(t, repo, "node_modules/keep.js", "module.exports = 1\n")
			write(t, repo, "node_modules/deep/other.js", "x\n")
			write(t, repo, "src/app.ts", "export const a = 1\n")

			first := snap(t, repo, "cp1", SlotBefore)
			paths := treePaths(t, repo, first.Tree)
			if !contains(paths, "src/app.ts") {
				t.Fatalf("source file missing from tree: %v", paths)
			}
			for _, path := range paths {
				if strings.HasPrefix(path, "node_modules/") {
					t.Errorf("node_modules leaked into the tree: %s", path)
				}
			}
			if len(first.Skipped) != 0 {
				t.Errorf("git walked into an excluded directory: %+v", first.Skipped)
			}
		})
	}
}

func TestDeniedPathAlreadyTrackedIsRemoved(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "node_modules/x.js", "old\n")
	if _, err := repo.git(context.Background(), joinNUL([]string{"node_modules/x.js"}),
		"update-index", "-z", "--add", "--stdin"); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "node_modules/x.js", "new\n")

	out := snap(t, repo, "cp1", SlotBefore)
	if contains(treePaths(t, repo, out.Tree), "node_modules/x.js") {
		t.Fatal("a tracked denied path stayed in the tree")
	}
	if got := skipReason(out, "node_modules/x.js"); got != SkipExcluded {
		t.Fatalf("skip reason = %q, want %q", got, SkipExcluded)
	}
}

func TestOversizedTrackedFileLeavesTheTree(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{MaxFileBytes: 64})
	write(t, repo, "data.bin", "small\n")
	first := snap(t, repo, "cp1", SlotBefore)
	if !contains(treePaths(t, repo, first.Tree), "data.bin") {
		t.Fatal("small file should be in the first tree")
	}

	write(t, repo, "data.bin", strings.Repeat("A", 200))
	second := snap(t, repo, "cp2", SlotBefore)
	if contains(treePaths(t, repo, second.Tree), "data.bin") {
		t.Fatal("a file that grew past the ceiling stayed in the tree")
	}
	if got := skipReason(second, "data.bin"); got != SkipTooLarge {
		t.Fatalf("skip reason = %q, want %q", got, SkipTooLarge)
	}
}

func TestNestedRepositoryIsSkippedAndReported(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "top.txt", "hi\n")
	sub := filepath.Join(repo.WorkTree(), "vendored")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	initCmd := exec.CommandContext(context.Background(), "git")
	initCmd.Args = []string{"git", "init", "-q", sub}
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init sub: %v: %s", err, out)
	}
	write(t, repo, "vendored/inner.txt", "inner\n")

	out := snap(t, repo, "cp1", SlotBefore)
	for _, path := range treePaths(t, repo, out.Tree) {
		if strings.HasPrefix(path, "vendored") {
			t.Errorf("nested repository leaked into the tree: %s", path)
		}
	}
	if got := skipReason(out, "vendored"); got != SkipNestedRepo {
		t.Fatalf("skip reason = %q, want %q (skips: %+v)", got, SkipNestedRepo, out.Skipped)
	}
}

func TestFileCeilingReportsProjectTooLarge(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{MaxFiles: 3})
	for _, name := range []string{"a", "b", "c", "d"} {
		write(t, repo, name+".txt", name)
	}
	_, err := repo.Snapshot(context.Background(), "cp1", SlotBefore)
	if !errors.Is(err, ErrProjectTooLarge) {
		t.Fatalf("err = %v, want %v", err, ErrProjectTooLarge)
	}
}

func TestCleanGitEnvStripsInheritedGitVariables(t *testing.T) {
	t.Parallel()
	env := cleanGitEnv("/tmp/shadow-git")
	for _, kv := range env {
		key, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(key, "GIT_") {
			switch key {
			case "GIT_TERMINAL_PROMPT", "GIT_OPTIONAL_LOCKS", "GIT_CONFIG_NOSYSTEM", "GIT_ATTR_NOSYSTEM", "GIT_INDEX_FILE":
			default:
				t.Fatalf("unexpected GIT_ variable in cleaned env: %s", kv)
			}
		}
	}
}

func TestSnapshotHonoursTheCallerDeadline(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", "one\n")

	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	_, err := repo.Snapshot(ctx, "cp1", SlotBefore)
	if err == nil {
		t.Fatal("expected the expired deadline to fail the snapshot")
	}
}

func TestConcurrentSnapshotsSerialise(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	for i := range 20 {
		write(t, repo, "f"+string(rune('a'+i))+".txt", "content")
	}
	tasks := make([]*supervise.Task, 0, 8)
	for i := range 8 {
		id := "cp" + string(rune('a'+i))
		tasks = append(tasks, supervise.Go(context.Background(), "snap-"+id, func(ctx context.Context) error {
			_, err := repo.Snapshot(ctx, id, SlotBefore)
			return err
		}))
	}
	for _, task := range tasks {
		if err := task.Wait(); err != nil {
			t.Fatalf("concurrent snapshot: %v", err)
		}
	}
}

func TestProjectKeyResolvesSymlinks(t *testing.T) {
	t.Parallel()
	real := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	direct, _, err := ProjectKey(real)
	if err != nil {
		t.Fatal(err)
	}
	viaLink, resolved, err := ProjectKey(link)
	if err != nil {
		t.Fatal(err)
	}
	if direct != viaLink {
		t.Fatalf("keys differ: %s vs %s", direct, viaLink)
	}
	if want, evalErr := filepath.EvalSymlinks(real); evalErr == nil && resolved != want {
		t.Fatalf("resolved = %s, want %s", resolved, want)
	}
}

func TestDiffReportsKindsModesAndLineCounts(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "keep.txt", "one\ntwo\n")
	write(t, repo, "gone.txt", "bye\n")
	before := snap(t, repo, "cp1", SlotBefore)

	write(t, repo, "keep.txt", "one\ntwo\nthree\n")
	if err := os.Remove(filepath.Join(repo.WorkTree(), "gone.txt")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "new.sh", "#!/bin/sh\necho hi\n")
	chmod(t, repo, "new.sh", 0o750)
	write(t, repo, "blob.bin", string([]byte{0, 1, 2, 0, 3}))
	after := snap(t, repo, "cp1", SlotAfter)

	changes, err := repo.Diff(context.Background(), before.Tree, after.Tree)
	if err != nil {
		t.Fatal(err)
	}
	byPath := make(map[string]Change, len(changes))
	for _, change := range changes {
		byPath[change.Path] = change
	}
	if got := byPath["keep.txt"]; got.Kind != ChangeModified || got.Added != 1 || got.Removed != 0 {
		t.Errorf("keep.txt = %+v, want modified +1 -0", got)
	}
	if got := byPath["gone.txt"]; got.Kind != ChangeDeleted {
		t.Errorf("gone.txt = %+v, want deleted", got)
	}
	if got := byPath["new.sh"]; got.Kind != ChangeAdded || got.Mode != "100755" {
		t.Errorf("new.sh = %+v, want added mode 100755", got)
	}
	if got := byPath["blob.bin"]; !got.Binary() {
		t.Errorf("blob.bin = %+v, want binary", got)
	}
}

func TestDirectoryBecomingASymlinkStillSnapshots(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "pkg/file.txt", "inside\n")
	snap(t, repo, "cp1", SlotBefore)

	if err := os.RemoveAll(filepath.Join(repo.WorkTree(), "pkg")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "elsewhere.txt", "target\n")
	if err := os.Symlink("elsewhere.txt", filepath.Join(repo.WorkTree(), "pkg")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	after := snap(t, repo, "cp1", SlotAfter)
	paths := treePaths(t, repo, after.Tree)
	if contains(paths, "pkg/file.txt") {
		t.Error("the replaced directory's file stayed in the tree")
	}
	if !contains(paths, "pkg") {
		t.Errorf("the new symlink is missing from the tree: %v", paths)
	}

	if err := os.Remove(filepath.Join(repo.WorkTree(), "pkg")); err != nil {
		t.Fatal(err)
	}
	write(t, repo, "pkg/file.txt", "inside again\n")
	back := snap(t, repo, "cp2", SlotAfter)
	paths = treePaths(t, repo, back.Tree)
	if contains(paths, "pkg") || !contains(paths, "pkg/file.txt") {
		t.Errorf("tree after the reverse type change = %v", paths)
	}
}

func TestExtraExcludesFromSettingsApply(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{ExtraExcludes: []string{"generated/", "*.snapshot"}})
	write(t, repo, "generated/schema.ts", "export type A = 1\n")
	write(t, repo, "src/main.ts", "1\n")
	write(t, repo, "src/big.snapshot", "blob\n")

	out := snap(t, repo, "cp1", SlotBefore)
	paths := treePaths(t, repo, out.Tree)
	if !contains(paths, "src/main.ts") {
		t.Fatalf("source file missing: %v", paths)
	}
	for _, path := range paths {
		if strings.HasPrefix(path, "generated/") || strings.HasSuffix(path, ".snapshot") {
			t.Errorf("a Settings exclude leaked into the tree: %s", path)
		}
	}
}

func TestDenyPatternsAreDirectoryShaped(t *testing.T) {
	t.Parallel()
	patterns := denyPatterns([]string{" spaced/ ", "", "   "})
	if !contains(patterns, "node_modules/") || !contains(patterns, ".DS_Store") {
		t.Fatalf("patterns = %v", patterns)
	}
	if !contains(patterns, "spaced/") {
		t.Errorf("a Settings pattern was not trimmed: %v", patterns)
	}
	for _, pattern := range patterns {
		if pattern == "" || strings.TrimSpace(pattern) != pattern {
			t.Errorf("empty or padded pattern %q would match everything", pattern)
		}
	}
}

func TestWaitingForTheProjectLockHonoursTheDeadline(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", "one\n")

	held := make(chan struct{})
	release := make(chan struct{})
	holder := supervise.Go(context.Background(), "hold-lock", func(ctx context.Context) error {
		return repo.runLocked(ctx, func() error {
			close(held)
			<-release
			return nil
		})
	})
	<-held

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := repo.Snapshot(ctx, "cp1", SlotBefore)
	close(release)
	if waitErr := holder.Wait(); waitErr != nil {
		t.Fatal(waitErr)
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline error", err)
	}
}

func TestPorcelainAndRawParsers(t *testing.T) {
	t.Parallel()
	out := []byte(" M services/refund/src/A.java\x00?? services/refund/src/B.java\x00R  services/refund/src/New.java\x00services/refund/src/Old.java\x00 D services/refund/pom.xml\x00M  other/C.java\x00")
	got := Porcelain(out, "services/refund/")
	want := map[string]string{
		"src/A.java":   "M",
		"src/B.java":   "A",
		"src/New.java": "R",
		"pom.xml":      "D",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Porcelain = %v, want %v", got, want)
	}
	if _, _, err := ParseRawDiff([]byte(":100644 100644\x00keep.txt\x00")); err == nil {
		t.Fatal("a short raw record was accepted")
	}
}

func TestEngineAccessorsAndUnavailableGit(t *testing.T) {
	t.Parallel()
	unavail := Availability{Reason: "no git"}
	engine := New(t.TempDir(), unavail, Limits{})
	if engine.Availability() != unavail {
		t.Fatalf("Availability = %+v, want %+v", engine.Availability(), unavail)
	}
	if engine.Limits().MaxToolsPerSession != DefaultMaxToolsPerSession {
		t.Fatalf("MaxToolsPerSession = %d, want %d", engine.Limits().MaxToolsPerSession, DefaultMaxToolsPerSession)
	}
	if engine.Activity() == nil {
		t.Fatal("Activity() returned nil")
	}
	if _, err := engine.Open(context.Background(), t.TempDir()); !errors.Is(err, ErrGitUnavailable) {
		t.Fatalf("Open with unavailable git = %v, want ErrGitUnavailable", err)
	}
}
