//go:build perf

package perf

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/snapshot"
)

const (
	spikeDirs         = 1000
	spikeFilesPerDir  = 100
	spikeChangedFiles = 100
	// turnSnapshotCap is the design's cap for one turn snapshot (git-guard §3.1).
	turnSnapshotCap = 10 * time.Second
)

func writeSpikeTree(t *testing.T, work string) {
	t.Helper()
	for d := range spikeDirs {
		dir := filepath.Join(work, fmt.Sprintf("pkg%04d", d))
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		for f := range spikeFilesPerDir {
			body := fmt.Sprintf("package p%d\n\nconst v%d = %d\n", d, f, d*f)
			if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("f%03d.go", f)), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// TestTurnSnapshot100kFilesFitsTheCap is the GG-E03 timing spike: one turn
// snapshot of a tree of 100 thousand files, cold (every file hashed) and warm
// (the next capture after 100 changed files, only those hashed), against the
// 10 s cap. Both numbers go to the log.
func TestTurnSnapshot100kFilesFitsTheCap(t *testing.T) {
	git := snapshot.Locate(t.Context())
	if !git.OK() {
		t.Fatalf("snapshot: git unavailable: %s", git.Reason)
	}
	work := t.TempDir()
	writeSpikeTree(t, work)
	clk := clock.Real{}
	engine := snapshot.NewWithDeps(filepath.Join(t.TempDir(), "snapshots"), git, snapshot.Limits{}, clk, ids.NewUUIDv7(clk, ids.NewDeterministic("spike")))
	repo, err := engine.Open(t.Context(), work)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := repo.Snapshot(t.Context(), "cold", snapshot.SlotBefore); err != nil {
		t.Fatal(err)
	}
	cold := time.Since(start)
	for f := range spikeChangedFiles {
		path := filepath.Join(work, "pkg0000", fmt.Sprintf("f%03d.go", f))
		if err := os.WriteFile(path, []byte("package p0\n\nconst changed = true\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	start = time.Now()
	if _, err := repo.Snapshot(t.Context(), "warm", snapshot.SlotAfter); err != nil {
		t.Fatal(err)
	}
	warm := time.Since(start)
	t.Logf("100k-file turn snapshot: cold %v, warm %v (cap %v)", cold, warm, turnSnapshotCap)
	if cold > turnSnapshotCap || warm > turnSnapshotCap {
		t.Fatalf("turn snapshot cold %v, warm %v; want each within %v", cold, warm, turnSnapshotCap)
	}
}
