package gitops

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// TestPruneRemovesOnlyExpiredWorktrees pins the prune primitive of
// orchestration §9: task worktrees and branches whose team ended more than
// the retention duration ago (24 hours by default) are removed, fresher ones
// are kept, and stale worktree admin entries are pruned. The caller supplies
// the ended-at times; the injected clock supplies now.
func TestPruneRemovesOnlyExpiredWorktrees(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, clk := testManager(t)
	repoDir := initRepo(t)

	oldWT := createPruneTask(t, ctx, m, root, repoDir, "told")
	freshWT := createPruneTask(t, ctx, m, root, repoDir, "tfresh")
	boundaryWT := createPruneTask(t, ctx, m, root, repoDir, "tbound")
	staleWT := createPruneTask(t, ctx, m, root, repoDir, "tstale")
	// A stale admin entry: the worktree directory vanished without git
	// learning about it.
	if err := os.RemoveAll(staleWT); err != nil {
		t.Fatal(err)
	}

	res, err := m.Prune(ctx, endedTasksSpec(root, repoDir, clk.Now()))
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	assertExpiredRemoved(t, res, oldWT, boundaryWT)
	assertFreshKept(t, repoDir, freshWT, res)
	if res.RemovedAdminEntries != 1 {
		t.Fatalf("admin entries pruned = %d, want the one stale entry", res.RemovedAdminEntries)
	}
	assertWorktreeRows(t, ctx, m, repoDir, 2)
}

// createPruneTask creates one task worktree with a result commit, the state a
// team leaves behind when it ends.
func createPruneTask(t *testing.T, ctx context.Context, m *Manager, root, repoDir, taskID string) string {
	t.Helper()
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, taskID, "main")
	if err != nil {
		t.Fatalf("create worktree %s: %v", taskID, err)
	}
	writeFile(t, wt, "patch.txt", taskID+"\n")
	if _, err := m.CommitWorktree(ctx, wt, "result "+taskID); err != nil {
		t.Fatalf("commit %s: %v", taskID, err)
	}
	return wt
}

// endedTasksSpec builds the prune spec with the ended-at times relative to
// now: 25h, 1h, exactly 24h and 30h ago.
func endedTasksSpec(root, repoDir string, now time.Time) *PruneSpec {
	return &PruneSpec{
		Root:    root,
		RepoID:  testRepoID,
		RepoDir: repoDir,
		Tasks: []PruneTask{
			{TaskID: "told", TeamEndedAt: now.Add(-25 * time.Hour)},
			{TaskID: "tfresh", TeamEndedAt: now.Add(-1 * time.Hour)},
			{TaskID: "tbound", TeamEndedAt: now.Add(-DefaultRetention)},
			{TaskID: "tstale", TeamEndedAt: now.Add(-30 * time.Hour)},
		},
	}
}

// assertExpiredRemoved checks the 25h and boundary (exactly 24h) tasks are
// gone, worktree and branch.
func assertExpiredRemoved(t *testing.T, res PruneResult, oldWT, boundaryWT string) {
	t.Helper()
	for _, want := range []string{oldWT, boundaryWT} {
		if !containsExact(res.RemovedWorktrees, want) {
			t.Fatalf("removed worktrees = %v, want %s", res.RemovedWorktrees, want)
		}
		if _, statErr := os.Lstat(want); !os.IsNotExist(statErr) {
			t.Fatalf("expired worktree %s still exists: %v", want, statErr)
		}
	}
	for _, branch := range []string{"fugue/task/told", "fugue/task/tbound", "fugue/task/tstale"} {
		if !containsExact(res.RemovedBranches, branch) {
			t.Fatalf("removed branches = %v, want %s", res.RemovedBranches, branch)
		}
	}
}

// assertFreshKept checks the 1h task survives the prune, worktree and branch.
func assertFreshKept(t *testing.T, repoDir, freshWT string, res PruneResult) {
	t.Helper()
	if containsExact(res.RemovedWorktrees, freshWT) {
		t.Fatalf("removed worktrees = %v, want the fresh worktree kept", res.RemovedWorktrees)
	}
	if _, statErr := os.Lstat(freshWT); statErr != nil {
		t.Fatalf("fresh worktree was removed: %v", statErr)
	}
	if out := fixtureGit(t, repoDir, "rev-parse", "--verify", "refs/heads/fugue/task/tfresh"); len(strings.TrimSpace(out)) != 40 {
		t.Fatalf("fresh branch head = %q, want it kept", out)
	}
}

// assertWorktreeRows checks how many worktrees git knows about after a prune.
func assertWorktreeRows(t *testing.T, ctx context.Context, m *Manager, repoDir string, want int) {
	t.Helper()
	rows, err := m.worktreeListRows(ctx, repoDir)
	if err != nil {
		t.Fatalf("worktree list: %v", err)
	}
	if len(rows) != want {
		t.Fatalf("worktrees after prune = %v, want %d rows", rows, want)
	}
}

// TestPruneRejectsInvalidSpecs pins the typed rejections before anything is
// removed: a nil spec, a negative retention and an invalid task id.
func TestPruneRejectsInvalidSpecs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, clk := testManager(t)
	repoDir := initRepo(t)
	now := clk.Now()

	cases := []struct {
		name string
		spec *PruneSpec
	}{
		{"nil spec", nil},
		{"negative retention", &PruneSpec{Root: root, RepoID: testRepoID, RepoDir: repoDir, Retention: -time.Hour}},
		{
			"invalid task id",
			&PruneSpec{
				Root: root, RepoID: testRepoID, RepoDir: repoDir,
				Tasks: []PruneTask{{TaskID: "../escape", TeamEndedAt: now.Add(-48 * time.Hour)}},
			},
		},
	}
	for _, tc := range cases {
		res, err := m.Prune(ctx, tc.spec)
		if errcode.CodeOf(err) != errcode.InvalidArgument || len(res.RemovedWorktrees) != 0 {
			t.Fatalf("%s: prune = %+v, %v, want a typed invalid_argument rejection", tc.name, res, err)
		}
	}
}
