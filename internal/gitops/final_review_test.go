package gitops

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestFinalReviewGitOpsHelpers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	cur, err := m.CurrentBranch(ctx, repoDir)
	if err != nil || cur != "main" {
		t.Fatalf("CurrentBranch = (%q, %v), want main", cur, err)
	}
	if branchErr := m.CreateTeamBranch(ctx, repoDir, "team-fr", cur); branchErr != nil {
		t.Fatalf("CreateTeamBranch: %v", branchErr)
	}
	if unmerged, uErr := m.HasUnmergedCommits(ctx, repoDir, cur, "fugue/team/team-fr"); uErr != nil || unmerged {
		t.Fatalf("HasUnmergedCommits(empty) = (%t, %v), want (false, nil)", unmerged, uErr)
	}
	commitTeamWorktreeFile(t, m, root, repoDir, "team-fr", "added.txt", "hello\n")

	unmerged, err := m.HasUnmergedCommits(ctx, repoDir, cur, "fugue/team/team-fr")
	if err != nil || !unmerged {
		t.Fatalf("HasUnmergedCommits(after commit) = (%t, %v), want (true, nil)", unmerged, err)
	}
	diff, err := m.DiffBetween(ctx, repoDir, cur, "fugue/team/team-fr", 0)
	if err != nil || !strings.Contains(diff.Text, "added.txt") || !strings.Contains(diff.Text, "+hello") {
		t.Fatalf("unexpected DiffBetween: (%s, %v)", diff.Text, err)
	}
}

func commitTeamWorktreeFile(t *testing.T, m *Manager, root, repoDir, teamID, file, content string) {
	t.Helper()
	ctx := t.Context()
	wt, err := m.AddTeamWorktree(ctx, root, repoDir, testRepoID, teamID, "fugue/team/"+teamID)
	if err != nil {
		t.Fatalf("AddTeamWorktree: %v", err)
	}
	writeFile(t, wt, file, content)
	if _, commitErr := m.CommitWorktree(ctx, wt, "add file"); commitErr != nil {
		t.Fatalf("CommitWorktree: %v", commitErr)
	}
	if rmErr := m.RemoveTeamWorktree(ctx, repoDir, root, testRepoID, teamID); rmErr != nil {
		t.Fatalf("RemoveTeamWorktree: %v", rmErr)
	}
}

func TestDiffForFiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t-df", "main")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, wt, "a.txt", "aaa\n")
	writeFile(t, wt, "b.txt", "bbb\n")
	if _, commitErr := m.CommitWorktree(ctx, wt, "two files"); commitErr != nil {
		t.Fatal(commitErr)
	}
	out, err := m.DiffForFiles(ctx, root, testRepoID, "t-df", "main", []string{filepath.ToSlash("a.txt")})
	if err != nil {
		t.Fatalf("DiffForFiles: %v", err)
	}
	if !strings.Contains(out, "a.txt") || strings.Contains(out, "b.txt") {
		t.Fatalf("DiffForFiles should include only a.txt, got:\n%s", out)
	}
}
