package gitops

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TestBranchContainsCommitForReconciliation pins the reconciliation query
// behind the F11.4 restart note (OR-15): after a restart the merge queue
// confirms an integrating outcome by asking whether the task's head commit is
// already contained in the target branch instead of re-merging.
func TestBranchContainsCommitForReconciliation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	res, taskBranch := committedTaskWorktree(t, ctx, m, root, repoDir, "t1")
	assertContains(t, ctx, m, repoDir, taskBranch, res.SHA, true)
	assertContains(t, ctx, m, repoDir, "main", res.SHA, false)

	// The integration step: main takes the task commit; the query confirms
	// the outcome, which is what the merge queue does after a restart.
	fixtureGit(t, repoDir, "merge", "--ff-only", "--quiet", taskBranch)
	assertContains(t, ctx, m, repoDir, "main", res.SHA, true)

	// The base commit is an ancestor of every branch built on it.
	base := strings.TrimSpace(fixtureGit(t, repoDir, "rev-parse", taskBranch+"~1"))
	assertContains(t, ctx, m, repoDir, taskBranch, base, true)

	// A missing branch or commit is a typed not-found error, not a silent no.
	assertMissingRef(t, ctx, m, repoDir, "fugue/task/absent", res.SHA)
	assertMissingRef(t, ctx, m, repoDir, "main", strings.Repeat("d", 40))
}

// committedTaskWorktree creates a task worktree with one result commit and
// returns the commit and the task branch name.
func committedTaskWorktree(t *testing.T, ctx context.Context, m *Manager, root, repoDir, taskID string) (CommitResult, string) {
	t.Helper()
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, taskID, "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	writeFile(t, wt, "a.txt", "two\n")
	res, err := m.CommitWorktree(ctx, wt, "result of task "+taskID)
	if err != nil {
		t.Fatalf("commit result: %v", err)
	}
	branch, err := TaskBranchName(taskID)
	if err != nil {
		t.Fatalf("task branch name: %v", err)
	}
	return res, branch
}

// assertContains runs the containment query and checks the outcome.
func assertContains(t *testing.T, ctx context.Context, m *Manager, repoDir, branch, commit string, want bool) {
	t.Helper()
	contained, err := m.BranchContainsCommit(ctx, repoDir, branch, commit)
	if err != nil {
		t.Fatalf("branch contains commit: %v", err)
	}
	if contained != want {
		t.Fatalf("BranchContainsCommit(%s, %s) = %v, want %v", branch, commit, contained, want)
	}
}

// assertMissingRef checks that an unknown branch or commit is a typed
// not-found error.
func assertMissingRef(t *testing.T, ctx context.Context, m *Manager, repoDir, branch, commit string) {
	t.Helper()
	_, err := m.BranchContainsCommit(ctx, repoDir, branch, commit)
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Code() != errcode.NotFound {
		t.Fatalf("BranchContainsCommit(%s, %s) err = %v, want a typed not-found", branch, commit, err)
	}
}

// TestWorktreeHeadReadsTheTaskHead covers the head read the merge queue uses
// together with the containment query. The task branch is born at the parent
// commit, so the head is the base commit until the first result commit lands.
func TestWorktreeHeadReadsTheTaskHead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	base := strings.TrimSpace(fixtureGit(t, repoDir, "rev-parse", "main"))
	head, err := m.WorktreeHead(ctx, wt)
	if err != nil {
		t.Fatalf("worktree head before any result commit: %v", err)
	}
	if head != base {
		t.Fatalf("head = %q, want the parent commit %q", head, base)
	}

	writeFile(t, wt, "a.txt", "two\n")
	res, err := m.CommitWorktree(ctx, wt, "result of task t1")
	if err != nil {
		t.Fatalf("commit result: %v", err)
	}
	head, err = m.WorktreeHead(ctx, wt)
	if err != nil {
		t.Fatalf("worktree head: %v", err)
	}
	if head != res.SHA {
		t.Fatalf("head = %q, want the result commit %q", head, res.SHA)
	}
}
