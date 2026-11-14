package gitops

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TestResultCommitCapturesUncommittedChanges pins the result commit of
// orchestration §9: everything uncommitted in the task worktree — modified
// tracked files and untracked files, but not ignored files — lands in one
// commit on the task branch; with no changes the commit is skipped without
// failing; the user's identity produces the commit.
func TestResultCommitCapturesUncommittedChanges(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	writeFile(t, wt, "a.txt", "two\n")
	writeFile(t, wt, "new.txt", "created by the agent\n")
	writeFile(t, wt, "ignored.txt", "must not be committed\n")

	res, err := m.CommitWorktree(ctx, wt, "result of task t1")
	if err != nil {
		t.Fatalf("commit result: %v", err)
	}
	assertResultCommit(t, wt, repoDir, res)
	assertResultAuthor(t, wt)
	assertSecondCommitSkips(t, ctx, m, wt)
}

// assertResultCommit checks that the task branch head is the result commit
// and that its tree holds the tracked modification and the untracked file but
// not the ignored one.
func assertResultCommit(t *testing.T, wt, repoDir string, res CommitResult) {
	t.Helper()
	if !res.Committed {
		t.Fatal("the result commit was skipped with changes present")
	}
	if len(res.SHA) != 40 {
		t.Fatalf("sha = %q, want a full object name", res.SHA)
	}
	head := fixtureGit(t, repoDir, "rev-parse", "refs/heads/fugue/task/t1")
	if strings.TrimSpace(head) != res.SHA {
		t.Fatalf("task branch head = %q, want the result commit %q", head, res.SHA)
	}
	raw := fixtureGit(t, wt, "ls-tree", "-r", "--name-only", "HEAD")
	files := strings.Split(strings.TrimSpace(raw), "\n")
	if !containsExact(files, "a.txt") || !containsExact(files, "new.txt") {
		t.Fatalf("committed files = %v, want the modified and the untracked file", files)
	}
	if containsExact(files, "ignored.txt") {
		t.Fatalf("committed files = %v, want the ignored file excluded", files)
	}
	if content := fixtureGit(t, wt, "show", "HEAD:a.txt"); content != "two\n" {
		t.Fatalf("committed a.txt = %q, want the tracked modification", content)
	}
}

// assertResultAuthor checks the commit was made with the configured identity.
func assertResultAuthor(t *testing.T, wt string) {
	t.Helper()
	author := fixtureGit(t, wt, "log", "-1", "--format=%an <%ae>")
	if !strings.Contains(author, "Fugue Test") {
		t.Fatalf("commit author = %q, want the configured identity", author)
	}
}

// assertSecondCommitSkips pins the no-changes path: no commit, no failure.
func assertSecondCommitSkips(t *testing.T, ctx context.Context, m *Manager, wt string) {
	t.Helper()
	again, err := m.CommitWorktree(ctx, wt, "nothing changed")
	if err != nil {
		t.Fatalf("second commit failed: %v", err)
	}
	if again.Committed || again.SHA != "" {
		t.Fatalf("second commit = %+v, want no commit", again)
	}
}

// TestResultCommitSubjectIsValidated pins the subject rules: non-empty, one
// line, within the size bound; the failures are typed before git runs.
func TestResultCommitSubjectIsValidated(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	for _, subject := range []string{"", "   ", "two\nlines", strings.Repeat("x", 201)} {
		if _, subjectErr := m.CommitWorktree(ctx, wt, subject); errcode.CodeOf(subjectErr) != errcode.InvalidArgument {
			t.Fatalf("subject %q err = %v, want invalid_argument", subject, subjectErr)
		}
	}
}

// TestResultCommitWithoutIdentityFailsTyped pins the design note that a
// result commit refused because of the user's identity configuration is a
// typed error, not a crash or a silent failure. An empty local identity is
// the deterministic way to make git refuse the commit.
func TestResultCommitWithoutIdentityFailsTyped(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)
	fixtureGit(t, repoDir, "config", "user.name", "")
	fixtureGit(t, repoDir, "config", "user.email", "")

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	writeFile(t, wt, "a.txt", "changed by the agent\n")

	_, err = m.CommitWorktree(ctx, wt, "result without identity")
	var gitErr *GitError
	if !errors.As(err, &gitErr) || gitErr.Kind != failureIdentity {
		t.Fatalf("err = %v, want a typed identity failure", err)
	}
	if !strings.Contains(err.Error(), "no usable git identity") || strings.Contains(err.Error(), "who you are") {
		t.Fatalf("error text %q leaks git stderr", err.Error())
	}
	if detail := gitErr.Detail; detail == "" {
		t.Fatalf("detail = %q, want git's identity message for the logs", detail)
	}
	if _, statErr := os.Stat(wt); statErr != nil {
		t.Fatalf("worktree vanished after the failed commit: %v", statErr)
	}
}
