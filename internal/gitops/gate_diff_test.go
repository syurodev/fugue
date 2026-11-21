package gitops

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestGateDiffHelpers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	repoDir := initRepo(t)
	head, err := m.WorktreeHead(ctx, repoDir)
	if err != nil {
		t.Fatal(err)
	}
	teamBranch, err := TeamBranchName("t1")
	if err != nil {
		t.Fatal(err)
	}
	if branchErr := m.CreateTeamBranch(ctx, repoDir, "t1", head); branchErr != nil {
		t.Fatalf("CreateTeamBranch: %v", branchErr)
	}
	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "task-g1", teamBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	writeAndCommitGateFiles(t, m, wt)

	files, err := m.ChangedFiles(ctx, root, testRepoID, "task-g1", teamBranch)
	if err != nil {
		t.Fatalf("ChangedFiles: %v", err)
	}
	slices.Sort(files)
	if !slices.Equal(files, []string{"b.txt", "pkg/a.go"}) {
		t.Fatalf("ChangedFiles = %v, want [b.txt pkg/a.go]", files)
	}

	stat, err := m.DiffNumstat(ctx, root, testRepoID, "task-g1", teamBranch)
	if err != nil {
		t.Fatalf("DiffNumstat: %v", err)
	}
	if stat.Files != 2 || stat.Lines != 4 {
		t.Fatalf("DiffNumstat = %+v, want {Files:2 Lines:4}", stat)
	}

	added, err := m.DiffAddedByFile(ctx, root, testRepoID, "task-g1", teamBranch)
	if err != nil {
		t.Fatalf("DiffAddedByFile: %v", err)
	}
	if len(added) != 2 {
		t.Fatalf("DiffAddedByFile len = %d, want 2", len(added))
	}
}

func writeAndCommitGateFiles(t *testing.T, m *Manager, wt string) {
	t.Helper()
	if mkdirErr := os.MkdirAll(filepath.Join(wt, "pkg"), 0o750); mkdirErr != nil {
		t.Fatal(mkdirErr)
	}
	if writeErr := os.WriteFile(filepath.Join(wt, "pkg", "a.go"), []byte("package pkg\nvar x = 1\nvar y = 2\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if writeErr := os.WriteFile(filepath.Join(wt, "b.txt"), []byte("hello\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, commitErr := m.CommitWorktree(t.Context(), wt, "add test files"); commitErr != nil {
		t.Fatalf("CommitWorktree: %v", commitErr)
	}
}
