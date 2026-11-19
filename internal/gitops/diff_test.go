package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func setupDiffWorktree(t *testing.T) (*Manager, string, string) {
	t.Helper()
	mgr, dataDir, _ := testManager(t)
	repoDir := initRepo(t)
	ctx := t.Context()
	head, err := mgr.WorktreeHead(ctx, repoDir)
	if err != nil {
		t.Fatalf("WorktreeHead: %v", err)
	}
	teamBranch, err := TeamBranchName("team-1")
	if err != nil {
		t.Fatalf("TeamBranchName: %v", err)
	}
	if branchErr := mgr.CreateTeamBranch(ctx, repoDir, "team-1", head); branchErr != nil {
		t.Fatalf("CreateTeamBranch: %v", branchErr)
	}
	wt, err := mgr.CreateTaskWorktree(ctx, dataDir, repoDir, "repo-1", "task-1", teamBranch)
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}
	if writeErr := os.WriteFile(filepath.Join(wt, "hello.txt"), []byte(strings.Repeat("line\n", 10)), 0o600); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}
	if _, commitErr := mgr.CommitWorktree(ctx, wt, "task-1 result"); commitErr != nil {
		t.Fatalf("CommitWorktree: %v", commitErr)
	}
	return mgr, dataDir, teamBranch
}

func TestDiffAgainstParentIsReadOnlyAndPaged(t *testing.T) {
	t.Parallel()
	mgr, dataDir, teamBranch := setupDiffWorktree(t)
	ctx := t.Context()

	page1, err := mgr.DiffAgainstParent(ctx, dataDir, "repo-1", "task-1", teamBranch, 0, 40)
	if err != nil {
		t.Fatalf("DiffAgainstParent page1: %v", err)
	}
	if !page1.HasMore || len(page1.Text) != 40 || page1.TotalBytes <= 40 {
		t.Fatalf("unexpected page1 = %+v", page1)
	}
	page2, err := mgr.DiffAgainstParent(ctx, dataDir, "repo-1", "task-1", teamBranch, 40, 1024)
	if err != nil {
		t.Fatalf("DiffAgainstParent page2: %v", err)
	}
	if page2.HasMore || !strings.Contains(page1.Text+page2.Text, "+line") {
		t.Fatalf("unexpected page2 = %+v", page2)
	}
}

func TestDiffAgainstParentRejectsUnsafeIDsAndRefs(t *testing.T) {
	t.Parallel()
	mgr, dataDir, _ := testManager(t)
	ctx := t.Context()

	if _, err := mgr.DiffAgainstParent(ctx, dataDir, "repo-1", "../escape", "fugue/team/team-1", 0, 100); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("invalid taskID code = %v, want InvalidArgument", errcode.CodeOf(err))
	}
	if _, err := mgr.DiffAgainstParent(ctx, dataDir, "repo-1", "task-1", "--output=/tmp/x", 0, 100); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("unsafe ref code = %v, want InvalidArgument", errcode.CodeOf(err))
	}
	if _, err := mgr.DiffAgainstParent(ctx, dataDir, "repo-1", "task-1", "fugue/team/team-1", -1, 100); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("negative offset code = %v, want InvalidArgument", errcode.CodeOf(err))
	}
}
