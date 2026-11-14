package gitops

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// parallelTasks is the worktree count of OR-E06, the spike size at which git
// started leaking ref-lock errors.
const parallelTasks = 16

// TestParallelWorktreesNoRefLockLeak is OR-E06: parallelTasks worktrees are
// created, committed and removed concurrently through gitops on one real
// repository, and no ref-lock error escapes.
func TestParallelWorktreesNoRefLockLeak(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	errs := make([]error, parallelTasks)
	tasks := make([]*supervise.Task, parallelTasks)
	var wg sync.WaitGroup
	for i := range parallelTasks {
		wg.Add(1)
		id := taskID(i)
		tasks[i] = supervise.Go(ctx, "test.parallel-task-"+id, func(ctx context.Context) error {
			defer wg.Done()
			errs[i] = runOneTask(ctx, m, root, repoDir, id)
			return nil
		})
	}
	wg.Wait()
	for _, task := range tasks {
		task.Stop()
		if err := task.Wait(); err != nil {
			t.Fatalf("supervised task %s: %v", task.Name(), err)
		}
	}
	assertNoRefLockLeak(t, errs)

	rows, err := m.worktreeListRows(ctx, repoDir)
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("worktrees after the run = %v, want only the main worktree", rows)
	}
	out := fixtureGit(t, repoDir, "branch", "--list", "fugue/task/*")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("task branches survived removal: %q", out)
	}
}

// assertNoRefLockLeak fails the test when any task error is a ref-lock error
// or when any task failed at all.
func assertNoRefLockLeak(t *testing.T, errs []error) {
	t.Helper()
	for i, err := range errs {
		if err == nil {
			continue
		}
		if errors.Is(err, ErrRefLockExhausted) || strings.Contains(err.Error(), "cannot lock ref") {
			t.Fatalf("task %s: a ref-lock error escaped: %v", taskID(i), err)
		}
		t.Fatalf("task %s: %v", taskID(i), err)
	}
}

func taskID(i int) string {
	return "t" + strconv.Itoa(i)
}

// TestRefAndPathComponentsAreValidated pins the id validation behind every
// operation (orchestration §9): an id must be a safe ref and path component.
func TestRefAndPathComponentsAreValidated(t *testing.T) {
	t.Parallel()
	assertIDTable(t)
	assertRefNameTable(t)
	assertRevTable(t)
	assertOperationsValidateInputs(t)
}

// assertIDTable pins the id rule: safe ref and path components pass,
// traversal and ref-lock shapes fail.
func assertIDTable(t *testing.T) {
	t.Helper()
	for _, id := range []string{"t1", "Team_2.x-y", "a", "0"} {
		if err := ValidateID("task", id); err != nil {
			t.Fatalf("ValidateID(%q) = %v, want valid", id, err)
		}
	}
	for _, id := range []string{
		"", ".", "..", "a/b", "a b", "ä", ".lead", "-lead", "lead.", "lead/",
		"a..b", "x.lock", strings.Repeat("a", 101), "sp@ce", "a{b",
	} {
		if err := ValidateID("task", id); err == nil {
			t.Fatalf("ValidateID(%q) = nil, want invalid", id)
		}
	}
}

// assertRefNameTable pins the ref-name rules of git-check-ref-format that
// stop a caller-supplied name from being parsed as a git flag.
func assertRefNameTable(t *testing.T) {
	t.Helper()
	for _, ok := range []string{"fugue/task/t1", "feature/x", "refs/heads/main", "core.tip"} {
		if err := ValidateRefName(ok); err != nil {
			t.Fatalf("ValidateRefName(%q) = %v, want valid", ok, err)
		}
	}
	for _, bad := range []string{
		"", "-c", "--upload-pack", ".hidden", "trailing.", "a..b", "tail.lock",
		"@{now}", "@", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", "a\\b",
		"lead//double", "head/", "tail/",
	} {
		if err := ValidateRefName(bad); err == nil {
			t.Fatalf("ValidateRefName(%q) = nil, want invalid", bad)
		}
	}
}

// assertRevTable pins the revision rule: hexadecimal object names (short or
// full) and ref names pass, anything that could change the command fails.
func assertRevTable(t *testing.T) {
	t.Helper()
	for _, ok := range []string{"0123abcd", strings.Repeat("a", 40), strings.Repeat("f", 64), "main"} {
		if err := ValidateRev(ok); err != nil {
			t.Fatalf("ValidateRev(%q) = %v, want valid", ok, err)
		}
	}
	for _, bad := range []string{"", "-deadbeef", "has space"} {
		if err := ValidateRev(bad); err == nil {
			t.Fatalf("ValidateRev(%q) = nil, want invalid", bad)
		}
	}
}

// assertOperationsValidateInputs checks the typed rejections surface through
// the public operations before any git call runs.
func assertOperationsValidateInputs(t *testing.T) {
	t.Helper()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	type op struct {
		name string
		call func() error
	}
	calls := []op{
		{"CreateTeamBranch bad team id", func() error { return m.CreateTeamBranch(ctx, repoDir, "a..b", "main") }},
		{"CreateTeamBranch bad base", func() error { return m.CreateTeamBranch(ctx, repoDir, "team1", "-oBase") }},
		{"CreateTaskWorktree bad task id", func() error {
			_, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "x.lock", "main")
			return err
		}},
		{"CreateTaskWorktree bad parent branch", func() error {
			_, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "-bNope")
			return err
		}},
		{"TaskWorktreePath bad repo id", func() error {
			_, err := TaskWorktreePath(root, "repo/id", "t1")
			return err
		}},
		{"TaskWorktreePath relative root", func() error {
			_, err := TaskWorktreePath("data/rel", testRepoID, "t1")
			return err
		}},
		{"CreateTaskWorktree relative root", func() error {
			_, err := m.CreateTaskWorktree(ctx, "data/rel", repoDir, testRepoID, "t1", "main")
			return err
		}},
		{"RemoveTaskWorktree relative root", func() error {
			return m.RemoveTaskWorktree(ctx, repoDir, "data/rel", testRepoID, "t1")
		}},
	}
	for _, tc := range calls {
		if err := tc.call(); errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("%s: err = %v, want invalid_argument", tc.name, err)
		}
	}

	path, err := TaskWorktreePath(root, testRepoID, "t1")
	if err != nil {
		t.Fatalf("TaskWorktreePath: %v", err)
	}
	if want := filepath.Join(root, "worktrees", testRepoID, "t1"); path != want {
		t.Fatalf("worktree path = %s, want %s", path, want)
	}
}

// TestTeamBranchLifecycle covers the branch and worktree lifecycle outside
// the concurrency evidence: creating a team branch at a base commit, a
// duplicate rejection, and idempotent worktree removal.
func TestTeamBranchLifecycle(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	if err := m.CreateTeamBranch(ctx, repoDir, "team1", "main"); err != nil {
		t.Fatalf("create team branch: %v", err)
	}
	if err := m.CreateTeamBranch(ctx, repoDir, "team1", "main"); errcode.CodeOf(err) != errcode.AlreadyExists {
		t.Fatalf("duplicate team branch err = %v, want already_exists", err)
	}
	out := fixtureGit(t, repoDir, "rev-parse", "--verify", "refs/heads/fugue/team/team1")
	if len(strings.TrimSpace(out)) != 40 {
		t.Fatalf("team branch head = %q, want a full sha", out)
	}

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "fugue/team/team1")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	if _, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main"); errcode.CodeOf(err) != errcode.AlreadyExists {
		t.Fatalf("duplicate task worktree err = %v, want already_exists", err)
	}
	if _, statErr := os.Stat(filepath.Join(wt, ".git")); statErr != nil {
		t.Fatalf("worktree missing .git: %v", statErr)
	}

	if err := m.RemoveTaskWorktree(ctx, repoDir, root, testRepoID, "t1"); err != nil {
		t.Fatalf("remove task worktree: %v", err)
	}
	if err := m.RemoveTaskWorktree(ctx, repoDir, root, testRepoID, "t1"); err != nil {
		t.Fatalf("second remove is not idempotent: %v", err)
	}
	if _, statErr := os.Lstat(wt); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("worktree still exists: %v", statErr)
	}
	out = fixtureGit(t, repoDir, "branch", "--list", "fugue/task/*")
	if strings.TrimSpace(out) != "" {
		t.Fatalf("task branch survived removal: %q", out)
	}
}

// TestFailedWorktreeAddLeavesNoBranch pins the failure cleanup of the task
// worktree creation: git can create the branch and the worktree before the
// add fails, and gitops removes what that call made — never a pre-existing
// branch — so a retry starts clean.
//
// Not parallel: it changes the process environment.
func TestFailedWorktreeAddLeavesNoBranch(t *testing.T) {
	t.Setenv("REAL_GIT", locateGit(t))
	t.Setenv("FAIL_ADD", "1")
	// The wrapper runs the real git and, when the invocation is a worktree
	// add, pretends it failed after git created the branch and the worktree.
	script := `is_add=""
prev=""
for a in "$@"; do
  if [ "$prev" = "worktree" ] && [ "$a" = "add" ]; then
    is_add=1
  fi
  prev="$a"
done
"$REAL_GIT" "$@"
status=$?
if [ -n "$is_add" ] && [ -n "$FAIL_ADD" ] && [ $status -eq 0 ]; then
  exit 1
fi
exit $status
`
	ctx := t.Context()
	m, root, _ := testManagerWithGit(t, wrapperGit(t, script))
	repoDir := initRepo(t)

	_, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err == nil {
		t.Fatal("the failing add did not surface")
	}
	if out := fixtureGit(t, repoDir, "branch", "--list", "fugue/task/t1"); strings.TrimSpace(out) != "" {
		t.Fatalf("the branch created by the failed add survived: %q", out)
	}
	if rows, rowsErr := m.worktreeListRows(ctx, repoDir); len(rows) != 1 {
		var dbg *GitError
		errors.As(rowsErr, &dbg)
		t.Fatalf("worktrees after the failed add = %v (detail %q), want only the main worktree", rowsErr, dbg.Detail)
	}

	// A retry starts clean and behaves like a first attempt.
	t.Setenv("FAIL_ADD", "")
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("retry after the failed add: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(wt, ".git")); statErr != nil {
		t.Fatalf("retried worktree missing .git: %v", statErr)
	}
	if err := m.RemoveTaskWorktree(ctx, repoDir, root, testRepoID, "t1"); err != nil {
		t.Fatalf("remove retried worktree: %v", err)
	}
}

// wrapperGit installs script at a fresh path and returns it as the manager's
// git binary.
func wrapperGit(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wrappergit")
	writeScript(t, path, script)
	return path
}
