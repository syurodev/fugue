package snapshot

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	idTurn1 = "turn-1"
	idTool1 = "tool-1"
)

func TestExpiredSnapshotsAppliesEveryRule(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	limits := Limits{
		TurnRetentionDays:  30,
		ToolRetentionDays:  7,
		MaxTurnsPerSession: 2,
		MaxToolsPerSession: 2,
		MaxProjectBytes:    1 << 40,
	}.withDefaults()

	rows := []Record{
		{ID: "old-turn", SessionID: "s1", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-31 * 24 * time.Hour)},
		{ID: "old-tool", SessionID: "s1", ProjectKey: "p", Kind: KindTool, CreatedAt: now.Add(-8 * 24 * time.Hour)},
		{ID: "stale-safety", SessionID: "s1", ProjectKey: "p", Kind: KindRestoreSafety, CreatedAt: now.Add(-8 * 24 * time.Hour)},
		{ID: "fresh-safety", SessionID: "s1", ProjectKey: "p", Kind: KindRewindSafety, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: idTurn1, SessionID: "s1", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-3 * time.Hour)},
		{ID: "turn-2", SessionID: "s1", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "turn-3", SessionID: "s1", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: idTool1, SessionID: "s1", ProjectKey: "p", Kind: KindTool, CreatedAt: now.Add(-3 * time.Hour)},
		{ID: "tool-2", SessionID: "s1", ProjectKey: "p", Kind: KindTool, CreatedAt: now.Add(-2 * time.Hour)},
		{ID: "tool-3", SessionID: "s1", ProjectKey: "p", Kind: KindTool, CreatedAt: now.Add(-1 * time.Hour)},
		{ID: "other-1", SessionID: "s2", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-5 * time.Hour)},
		{ID: "other-2", SessionID: "s2", ProjectKey: "p", Kind: KindTurn, CreatedAt: now.Add(-4 * time.Hour)},
	}
	got := expiredSnapshots(rows, now, t.TempDir(), limits)
	sort.Strings(got)
	want := []string{"old-tool", "old-turn", "stale-safety", idTool1, idTurn1}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("expired = %v, want %v", got, want)
	}
}

// TestToolSnapshotQuotaNeverEvictsTurnSnapshots verifies tool-system §9: tool
// snapshots have their own circular per-session quota and never evict turn
// snapshots.
func TestToolSnapshotQuotaNeverEvictsTurnSnapshots(t *testing.T) {
	t.Parallel()
	fakeClock := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	engine, repo := testEngineWithClock(t, Limits{
		MaxTurnsPerSession: 2,
		MaxToolsPerSession: 3,
	}, fakeClock)
	ctx := context.Background()
	store := NewMemoryStore()

	write(t, repo, "app.go", "package main\n")
	turn1, _, err := engine.CaptureTurn(ctx, store, "sess-1", repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	fakeClock.Advance(time.Minute)
	turn2, _, err := engine.CaptureTurn(ctx, store, "sess-1", repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}

	toolIDs := make([]string, 0, 5)
	for i := range 5 {
		fakeClock.Advance(time.Minute)
		write(t, repo, "app.go", "package main // v"+strconv.Itoa(i)+"\n")
		rec, _, err := engine.CaptureToolBefore(ctx, store, "sess-1", repo.WorkTree())
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := engine.CaptureToolAfter(ctx, store, rec.ID, repo.WorkTree()); err != nil {
			t.Fatal(err)
		}
		toolIDs = append(toolIDs, rec.ID)
	}

	assertTwoQuotasIsolated(t, repo, store, turn1.ID, turn2.ID, toolIDs)
}

func assertTwoQuotasIsolated(t *testing.T, repo *Repo, store *MemoryStore, turn1ID, turn2ID string, toolIDs []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := store.GetSnapshot(ctx, turn1ID); err != nil {
		t.Fatalf("turn1 was evicted by tool snapshots: %v", err)
	}
	if _, err := store.GetSnapshot(ctx, turn2ID); err != nil {
		t.Fatalf("turn2 was evicted by tool snapshots: %v", err)
	}
	// The oldest two tool snapshots (0 and 1) must be evicted; the newest three (2, 3, 4) survive.
	for _, evictedID := range toolIDs[:2] {
		if _, err := store.GetSnapshot(ctx, evictedID); err == nil {
			t.Fatalf("expected tool snapshot %s to be evicted by circular quota", evictedID)
		}
		if refExists(t, repo, evictedID, SlotBefore) || refExists(t, repo, evictedID, SlotAfter) {
			t.Fatalf("refs for evicted tool snapshot %s survived", evictedID)
		}
	}
	for _, keptID := range toolIDs[2:] {
		if _, err := store.GetSnapshot(ctx, keptID); err != nil {
			t.Fatalf("expected tool snapshot %s to survive: %v", keptID, err)
		}
		if !refExists(t, repo, keptID, SlotBefore) || !refExists(t, repo, keptID, SlotAfter) {
			t.Fatalf("refs for surviving tool snapshot %s were dropped", keptID)
		}
	}
}

// TestSoftCapPrunesToolSnapshotsBeforeTurnSnapshots verifies tool-system §9:
// when a project's shadow repository exceeds the shared disk cap, tool
// snapshots are pruned before turn snapshots even when the turn snapshots are
// older.
func TestSoftCapPrunesToolSnapshotsBeforeTurnSnapshots(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const key = "shared-cap-project"
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	limits := Limits{MaxProjectBytes: 1 << 20}.withDefaults()
	rows := []Record{
		// Older turn snapshots:
		{ID: idTurn1, SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now.Add(-20 * time.Minute)},
		{ID: "turn-2", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now.Add(-19 * time.Minute)},
		// Newer tool snapshots:
		{ID: idTool1, SessionID: "s1", ProjectKey: key, Kind: KindTool, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "tool-2", SessionID: "s1", ProjectKey: key, Kind: KindTool, CreatedAt: now.Add(-1 * time.Minute)},
	}

	expired := expiredSnapshots(rows, now, root, limits)
	expiredSet := toSet(expired)
	if !expiredSet[idTool1] || !expiredSet["tool-2"] {
		t.Fatalf("expected tool snapshots to be pruned first, got expired = %v", expired)
	}
	if expiredSet[idTurn1] || expiredSet["turn-2"] {
		t.Fatalf("turn snapshots were pruned while tool snapshots sufficed: %v", expired)
	}
}

func TestMaintainDropsOrphanRefsAndKeepsLiveTrees(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(now)
	engine, repo := testEngineWithClock(t, Limits{}, fakeClock)
	ctx := context.Background()

	write(t, repo, "a.txt", contentOne)
	keep := snap(t, repo, "keep", SlotBefore)
	write(t, repo, "a.txt", contentTwo)
	snap(t, repo, "drop", SlotBefore)

	store := NewMemoryStore()
	if err := store.PutSnapshot(ctx, &Record{
		ID: "keep", SessionID: "s1", ProjectKey: repo.Key(), Kind: KindTurn, BeforeTree: keep.Tree, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutSnapshot(ctx, &Record{
		ID: "drop", SessionID: "s1", ProjectKey: repo.Key(), Kind: KindTurn, CreatedAt: now.Add(-90 * 24 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}

	report, err := engine.Maintain(ctx, store)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Expired) != 1 || report.Expired[0] != "drop" {
		t.Fatalf("expired = %v, want [drop]", report.Expired)
	}
	if refExists(t, repo, "drop", SlotBefore) {
		t.Error("the expired checkpoint's ref survived")
	}
	if !refExists(t, repo, "keep", SlotBefore) {
		t.Error("the surviving checkpoint's ref was dropped")
	}
	if _, err := repo.git(ctx, nil, "gc", "--prune=now", "--quiet"); err != nil {
		t.Fatal(err)
	}
	if paths := treePaths(t, repo, keep.Tree); !contains(paths, "a.txt") {
		t.Fatalf("the live tree did not survive gc: %v", paths)
	}
}

func TestMaintainRemovesAShadowRepositoryWithNoRowsLeft(t *testing.T) {
	t.Parallel()
	engine, repo := testEngine(t, Limits{})
	write(t, repo, "a.txt", contentOne)
	snap(t, repo, "only", SlotBefore)
	shadow := repo.GitDir()
	if _, err := os.Stat(shadow); err != nil {
		t.Fatal(err)
	}

	store := NewMemoryStore()
	report, err := engine.Maintain(context.Background(), store)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.RemovedProjects) != 1 || report.RemovedProjects[0] != repo.Key() {
		t.Fatalf("removed = %v, want [%s]", report.RemovedProjects, repo.Key())
	}
	if _, statErr := os.Stat(shadow); !os.IsNotExist(statErr) {
		t.Fatalf("the shadow repository survived: %v", statErr)
	}
	reopened, err := engine.Open(context.Background(), repo.WorkTree())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Snapshot(context.Background(), "again", SlotBefore); err != nil {
		t.Fatalf("snapshot after the directory was reclaimed: %v", err)
	}
}

func TestShadowDirectoryRecordsItsProject(t *testing.T) {
	t.Parallel()
	_, repo := testEngine(t, Limits{})
	gitRoot, err := os.OpenRoot(repo.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := gitRoot.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	raw, err := gitRoot.ReadFile(projectFileName)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != repo.WorkTree() {
		t.Fatalf("marker = %q, want %q", strings.TrimSpace(string(raw)), repo.WorkTree())
	}
	info, err := os.Stat(repo.GitDir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("shadow directory mode = %v, want 0700", info.Mode().Perm())
	}
}

func TestSoftCapDeletesOnlyWhatItMustGo(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const key = "project-key"
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	limits := Limits{MaxProjectBytes: 1 << 20}.withDefaults()
	rows := make([]Record, 0, 20)
	for i := range 20 {
		rows = append(rows, Record{
			ID:         "cp" + strconv.Itoa(i),
			SessionID:  "s1",
			ProjectKey: key,
			Kind:       KindTurn,
			CreatedAt:  now.Add(-time.Duration(20-i) * time.Minute),
		})
	}

	expired := expiredSnapshots(rows, now, root, limits)
	if len(expired) < 8 || len(expired) > 12 {
		t.Fatalf("expired %d of 20, want about half: %v", len(expired), expired)
	}
	if expired[0] != "cp0" {
		t.Fatalf("expired = %v, want the oldest first", expired)
	}
	for _, id := range expired {
		if id == "cp19" {
			t.Fatal("the newest checkpoint was deleted for size")
		}
	}
}

func TestSoftCapLeavesAProjectUnderTheLineAlone(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const key = "small-project"
	if err := os.MkdirAll(filepath.Join(root, key), 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	rows := []Record{
		{ID: "a", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now.Add(-time.Hour)},
		{ID: "b", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now},
	}
	if expired := expiredSnapshots(rows, now, root, DefaultLimits()); len(expired) != 0 {
		t.Fatalf("expired = %v, want none", expired)
	}
}

func TestSoftCapNeverDeletesEveryCheckpointOfAProject(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	const key = "enormous"
	dir := filepath.Join(root, key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pack"), make([]byte, 8<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	limits := Limits{MaxProjectBytes: 40 << 10}.withDefaults()

	rows := []Record{
		{ID: "old", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now.Add(-2 * time.Minute)},
		{ID: "mid", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now.Add(-time.Minute)},
		{ID: "newest", SessionID: "s1", ProjectKey: key, Kind: KindTurn, CreatedAt: now},
	}
	expired := expiredSnapshots(rows, now, root, limits)
	if len(expired) != 2 {
		t.Fatalf("expired = %v, want all but one", expired)
	}
	for _, id := range expired {
		if id == "newest" {
			t.Fatal("the last checkpoint a project has was deleted for size")
		}
	}
}
