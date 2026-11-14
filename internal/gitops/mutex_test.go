package gitops

import (
	"os"
	"path/filepath"
	"testing"
)

// TestPerRepoMutexKeysOnCanonicalCommonDir pins the per-repo mutex key
// (orchestration §9, OR-E06): two paths to the same repository — a symlinked
// path and a linked worktree — resolve to one gate, so their repo-level
// operations serialise against each other.
func TestPerRepoMutexKeysOnCanonicalCommonDir(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	m, root, _ := testManager(t)
	base := t.TempDir()
	repoDir := filepath.Join(base, "real")
	if err := os.MkdirAll(repoDir, 0o700); err != nil {
		t.Fatal(err)
	}
	initRepoAt(t, repoDir)

	linkDir := filepath.Join(base, "link")
	if err := os.Symlink(repoDir, linkDir); err != nil {
		t.Fatal(err)
	}

	viaReal, err := m.gateFor(ctx, repoDir)
	if err != nil {
		t.Fatalf("gate for real path: %v", err)
	}
	viaLink, err := m.gateFor(ctx, linkDir)
	if err != nil {
		t.Fatalf("gate for symlink path: %v", err)
	}
	if viaReal != viaLink {
		t.Fatal("the symlinked path took a different repo lock than the real path")
	}

	wt, err := m.CreateTaskWorktree(ctx, root, repoDir, testRepoID, "t1", "main")
	if err != nil {
		t.Fatalf("create task worktree: %v", err)
	}
	viaWorktree, err := m.gateFor(ctx, wt)
	if err != nil {
		t.Fatalf("gate for worktree path: %v", err)
	}
	if viaWorktree != viaReal {
		t.Fatal("the linked worktree took a different repo lock than the repository")
	}
	if len(m.gates) != 1 {
		t.Fatalf("gate count = %d, want 1 shared gate", len(m.gates))
	}
}
