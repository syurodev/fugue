package gitops

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeStatusReportsCleanAndDirtyWorktrees(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, dataDir, _ := testManager(t)
	repoDir := initRepo(t)
	wtPath, err := m.CreateTaskWorktree(ctx, dataDir, repoDir, "repo-status", "task-s1", "main")
	if err != nil {
		t.Fatalf("CreateTaskWorktree: %v", err)
	}

	clean, err := m.WorktreeStatus(ctx, wtPath)
	if err != nil {
		t.Fatalf("WorktreeStatus(clean): %v", err)
	}
	if clean.Dirty || clean.ChangedFiles != 0 || clean.HeadSHA == "" || !strings.Contains(clean.Summary, "status=clean") {
		t.Fatalf("unexpected clean status: %+v", clean)
	}

	if writeErr := os.WriteFile(filepath.Join(wtPath, "dirty.txt"), []byte("change\n"), 0o600); writeErr != nil {
		t.Fatalf("write dirty file: %v", writeErr)
	}
	dirty, err := m.WorktreeStatus(ctx, wtPath)
	if err != nil {
		t.Fatalf("WorktreeStatus(dirty): %v", err)
	}
	if !dirty.Dirty || dirty.ChangedFiles != 1 || !strings.Contains(dirty.Summary, "status=dirty (1 changed)") {
		t.Fatalf("unexpected dirty status: %+v", dirty)
	}
}
