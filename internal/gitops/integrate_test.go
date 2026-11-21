package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func mustTeamBranch(t *testing.T, teamID string) string {
	t.Helper()
	br, err := TeamBranchName(teamID)
	if err != nil {
		t.Fatalf("TeamBranchName(%q): %v", teamID, err)
	}
	return br
}

func mustTaskBranch(t *testing.T, taskID string) string {
	t.Helper()
	br, err := TaskBranchName(taskID)
	if err != nil {
		t.Fatalf("TaskBranchName(%q): %v", taskID, err)
	}
	return br
}

func setupTeamRepo(t *testing.T, teamID string) (m *Manager, root, repoDir, baseHead, teamBranch string) {
	t.Helper()
	m, root, _ = testManager(t)
	repoDir = initRepo(t)
	baseHead = strings.TrimSpace(fixtureGit(t, repoDir, "rev-parse", "HEAD"))
	if err := m.CreateTeamBranch(t.Context(), repoDir, teamID, baseHead); err != nil {
		t.Fatalf("CreateTeamBranch: %v", err)
	}
	return m, root, repoDir, baseHead, mustTeamBranch(t, teamID)
}

func createAndCommitTask(t *testing.T, m *Manager, root, repoDir, taskID, parentBranch, file, content string) (wt, branch, sha string) {
	t.Helper()
	ctx := t.Context()
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, taskID, parentBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree(%s): %v", taskID, err)
	}
	writeFile(t, wt, file, content)
	res, err := m.CommitWorktree(ctx, wt, taskID)
	if err != nil {
		t.Fatalf("CommitWorktree(%s): %v", taskID, err)
	}
	return wt, mustTaskBranch(t, taskID), res.SHA
}

func TestRebaseAndFastForwardBranch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, repoDir, baseHead, teamBranch := setupTeamRepo(t, "team1")

	_, b1, _ := createAndCommitTask(t, m, root, repoDir, "t1", teamBranch, "one.txt", "task 1\n")
	wt2, b2, _ := createAndCommitTask(t, m, root, repoDir, "t2", teamBranch, "two.txt", "task 2\n")
	sha1, err := m.FastForwardBranch(ctx, repoDir, teamBranch, b1)
	if err != nil {
		t.Fatalf("FastForwardBranch t1: %v", err)
	}
	if _, ffErr := m.FastForwardBranch(ctx, repoDir, teamBranch, b2); errcode.CodeOf(ffErr) != errcode.FailedPrecondition {
		t.Fatalf("FastForwardBranch before rebase = %v, want FailedPrecondition", ffErr)
	}

	reb, err := m.Rebase(ctx, wt2, teamBranch)
	if err != nil || reb.Conflicted || reb.HeadSHA == "" {
		t.Fatalf("Rebase t2 = (%+v, %v), want clean rebase", reb, err)
	}
	sha2, err := m.FastForwardBranch(ctx, repoDir, teamBranch, b2)
	if err != nil {
		t.Fatalf("FastForwardBranch t2: %v", err)
	}
	for _, sha := range []string{sha1, sha2} {
		if ok, contErr := m.BranchContainsCommit(ctx, repoDir, teamBranch, sha); contErr != nil || !ok {
			t.Fatalf("BranchContainsCommit(%s) = (%v, %v), want true", sha, ok, contErr)
		}
	}
	if gotHead := strings.TrimSpace(fixtureGit(t, repoDir, "rev-parse", "HEAD")); gotHead != baseHead {
		t.Fatalf("repo HEAD moved from %s to %s", baseHead, gotHead)
	}
}

func TestRebaseConflictAbortsCleanlyAndListsFiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, repoDir, _, teamBranch := setupTeamRepo(t, "team-c")

	_, b1, _ := createAndCommitTask(t, m, root, repoDir, "c1", teamBranch, "a.txt", "from c1\n")
	wt2, _, _ := createAndCommitTask(t, m, root, repoDir, "c2", teamBranch, "a.txt", "from c2\n")
	if _, err := m.FastForwardBranch(ctx, repoDir, teamBranch, b1); err != nil {
		t.Fatalf("FastForwardBranch c1: %v", err)
	}
	beforeHead, err := m.WorktreeHead(ctx, wt2)
	if err != nil {
		t.Fatalf("WorktreeHead: %v", err)
	}

	reb, err := m.Rebase(ctx, wt2, teamBranch)
	if err != nil || !reb.Conflicted {
		t.Fatalf("Rebase = (%+v, %v), want Conflicted=true", reb, err)
	}
	if diff := cmp.Diff([]string{"a.txt"}, reb.ConflictedFiles); diff != "" {
		t.Fatalf("ConflictedFiles (-want +got):\n%s", diff)
	}
	if afterHead, headErr := m.WorktreeHead(ctx, wt2); headErr != nil || afterHead != beforeHead {
		t.Fatalf("HEAD after abort = (%q, %v), want %q", afterHead, headErr, beforeHead)
	}
	if active, actErr := m.hasActiveRebase(ctx, wt2); actErr != nil || active {
		t.Fatalf("hasActiveRebase = (%v, %v), want (false, nil)", active, actErr)
	}
}

func TestRebaseOntoDropsUpstreamCommitsAndFastForwardsCheckedOutManager(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, repoDir, _, teamBranch := setupTeamRepo(t, "team-onto")

	mgrWT, mgrBranch, mgrSHA := createAndCommitTask(t, m, root, repoDir, "mgr", teamBranch, "mgr.txt", "manager unmerged\n")
	childWT, childBranch, _ := createAndCommitTask(t, m, root, repoDir, "child", mgrBranch, "child.txt", "child work\n")
	_, peerBranch, _ := createAndCommitTask(t, m, root, repoDir, "peer", mgrBranch, "peer.txt", "peer work\n")

	if _, err := m.FastForwardBranch(ctx, repoDir, mgrBranch, peerBranch); err != nil {
		t.Fatalf("FastForwardBranch into checked-out mgrBranch: %v", err)
	}
	if _, err := os.Stat(filepath.Join(mgrWT, "peer.txt")); err != nil {
		t.Fatalf("mgrWT missing peer.txt after fast-forward: %v", err)
	}

	base, err := m.MergeBase(ctx, repoDir, "refs/heads/"+childBranch, "refs/heads/"+mgrBranch)
	if err != nil {
		t.Fatalf("MergeBase: %v", err)
	}
	reb, err := m.RebaseOnto(ctx, childWT, teamBranch, base)
	if err != nil || reb.Conflicted {
		t.Fatalf("RebaseOnto = (%+v, %v)", reb, err)
	}
	if _, err := m.FastForwardBranch(ctx, repoDir, teamBranch, childBranch); err != nil {
		t.Fatalf("FastForwardBranch child onto team: %v", err)
	}
	if ok, contErr := m.BranchContainsCommit(ctx, repoDir, teamBranch, mgrSHA); contErr != nil || ok {
		t.Fatalf("teamBranch unexpectedly contains manager commit: (%v, %v)", ok, contErr)
	}
	tree := fixtureGit(t, repoDir, "ls-tree", "--name-only", "refs/heads/"+teamBranch)
	if !strings.Contains(tree, "child.txt") || strings.Contains(tree, "mgr.txt") {
		t.Fatalf("unexpected teamBranch tree:\n%s", tree)
	}
}
