package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseSessionDropsRecordsAndRefsAndWarmLeavesNone(t *testing.T) {
	t.Parallel()
	engine, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", contentOne)
	if err := engine.Warm(t.Context(), repo.WorkTree()); err != nil {
		t.Fatal(err)
	}
	store := NewMemoryStore()
	for range 2 {
		if _, _, err := engine.CaptureTurn(t.Context(), store, "s1", repo.WorkTree()); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := engine.CaptureTurn(t.Context(), store, "s2", repo.WorkTree()); err != nil {
		t.Fatal(err)
	}
	if err := engine.ReleaseSession(t.Context(), store, "s1", repo.WorkTree()); err != nil {
		t.Fatal(err)
	}
	rows, err := store.ListCheckpoints(t.Context())
	if err != nil || len(rows) != 1 || rows[0].SessionID != "s2" {
		t.Fatalf("records after release = %+v, %v; want only s2", rows, err)
	}
	refs, err := repo.git(t.Context(), nil, "for-each-ref", "--format=%(refname)", refPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Fields(string(refs))); got != 1 {
		t.Fatalf("refs after release = %d, want 1 (the s2 start)", got)
	}
}

// A git killed at its cap leaves index.lock behind; the next snapshot clears it
// instead of failing.
func TestStaleIndexLockDoesNotBreakTheNextSnapshot(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", contentOne)
	snap(t, repo, "cp1", SlotBefore)
	for _, name := range staleLockFiles() {
		if err := os.WriteFile(filepath.Join(repo.GitDir(), name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(t, repo, "a.txt", contentTwo)
	if out := snap(t, repo, "cp2", SlotAfter); out.Tree == "" {
		t.Fatal("snapshot after a stale lock returned no tree")
	}
	for _, name := range staleLockFiles() {
		if _, err := os.Stat(filepath.Join(repo.GitDir(), name)); err == nil {
			t.Fatalf("stale %s was not cleared", name)
		}
	}
}
