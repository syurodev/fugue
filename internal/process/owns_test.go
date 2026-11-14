//go:build unix

package process

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

const (
	outOfOrderLeader = "leader-100"
	outOfOrderChild  = "child-101"
)

type gatedProcTable struct {
	mu           sync.Mutex
	calls        int
	scan1Entered chan struct{}
	releaseScan1 chan struct{}
}

func (g *gatedProcTable) scan() ([]procEntry, error) {
	g.mu.Lock()
	g.calls++
	call := g.calls
	g.mu.Unlock()
	switch call {
	case 1:
		close(g.scan1Entered)
		<-g.releaseScan1
		return []procEntry{{pid: 100, ppid: 50, pgid: 100, startToken: outOfOrderLeader}}, nil
	case 2:
		return []procEntry{
			{pid: 100, ppid: 50, pgid: 100, startToken: outOfOrderLeader},
			{pid: 101, ppid: 100, pgid: 100, startToken: outOfOrderChild},
		}, nil
	default:
		return []procEntry{
			{pid: 100, ppid: 50, pgid: 100, startToken: outOfOrderLeader},
			{pid: 101, ppid: 1, pgid: 101, startToken: outOfOrderChild},
		}, nil
	}
}

func (*gatedProcTable) startToken(int) (string, error) { return "", ErrIdentityMismatch }

type fakeProcTable struct {
	entries []procEntry
	err     error
}

func (f fakeProcTable) scan() ([]procEntry, error) { return f.entries, f.err }

func (fakeProcTable) startToken(int) (string, error) { return "", ErrIdentityMismatch }

// testScopeOwnership checks the cgroup path of OwnsPID: membership is what
// cgroup.procs of the session's scope lists, and a scope that cannot be read
// is an error, never "not a member".
func testScopeOwnership(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := writeRootFile(dir, "cgroup.procs", []byte("4242\n4243\n")); err != nil {
		t.Fatal(err)
	}
	proc := &Process{scope: &cgroupScope{unitName: "fugue-vendor-s1.scope", dirPath: dir}}
	for pid, want := range map[int]bool{4242: true, 4243: true, 4244: false} {
		if got, err := proc.OwnsPID(pid); err != nil || got != want {
			t.Errorf("OwnsPID(%d) = %v, %v; want %v", pid, got, err, want)
		}
	}
	if _, err := proc.OwnsPID(0); !errors.Is(err, ErrIdentityMismatch) {
		t.Errorf("OwnsPID(0) = %v, want an identity error", err)
	}
	if err := writeRootFile(dir, "cgroup.procs", []byte("not-a-pid\n")); err != nil {
		t.Fatal(err)
	}
	if owned, err := proc.OwnsPID(4242); err == nil || owned {
		t.Errorf("OwnsPID on an unreadable scope = %v, %v; want an error", owned, err)
	}
}

// testTrackerOwnership checks the process-tree path of OwnsPID with a fixed
// process table: a descendant forked since the last tick is reached through
// its parent chain, a stranger is not, and every unknown fails closed.
func testTrackerOwnership(t *testing.T) {
	t.Helper()
	table := fakeProcTable{entries: []procEntry{
		{pid: 100, ppid: 50, pgid: 100, startToken: "leader"},
		{pid: 101, ppid: 100, pgid: 100, startToken: "child"},
		{pid: 102, ppid: 101, pgid: 102, startToken: "gc-9"},
		{pid: 200, ppid: 50, pgid: 200, startToken: "stranger"},
		{pid: 300, ppid: 50, pgid: 300, startToken: "reused"},
		{pid: 301, ppid: 300, pgid: 301, startToken: "child-of-reused"},
	}}
	tracker := newDescendantTracker(100, "leader", table, clock.NewFake(testEpoch()))
	// 300 was a descendant once; the pid now belongs to another process, so
	// neither 300 nor its child 301 may be treated as vendor-owned.
	tracker.tracked[300] = "exited-descendant"
	proc := &Process{tracker: tracker}
	for pid, want := range map[int]bool{100: true, 101: true, 102: true, 200: false, 300: false, 301: false} {
		if got, err := proc.OwnsPID(pid); err != nil || got != want {
			t.Errorf("OwnsPID(%d) = %v, %v; want %v", pid, got, err, want)
		}
	}
	assertReusedLeaderOwnsNothing(t)
	if owned, err := proc.OwnsPID(999); err == nil || owned {
		t.Errorf("OwnsPID of a pid outside the table = %v, %v; want an error", owned, err)
	}
	blind := &Process{tracker: newDescendantTracker(100, "leader", fakeProcTable{err: os.ErrPermission}, clock.NewFake(testEpoch()))}
	if owned, err := blind.OwnsPID(101); err == nil || owned {
		t.Errorf("OwnsPID with an unreadable process table = %v, %v; want an error", owned, err)
	}
	if owned, err := (&Process{}).OwnsPID(101); err == nil || owned {
		t.Errorf("OwnsPID without a containment record = %v, %v; want an error", owned, err)
	}
}

func assertReusedLeaderOwnsNothing(t *testing.T) {
	t.Helper()
	tracker := newDescendantTracker(100, "leader", fakeProcTable{entries: []procEntry{
		{pid: 100, ppid: 50, pgid: 100, startToken: "other"},
		{pid: 103, ppid: 100, pgid: 100, startToken: "other-child"},
	}}, clock.NewFake(testEpoch()))
	proc := &Process{tracker: tracker}
	for _, pid := range []int{100, 103} {
		if got, err := proc.OwnsPID(pid); err != nil || got {
			t.Errorf("OwnsPID(%d) with reused leader = %v, %v; want false", pid, got, err)
		}
	}
}

func TestOwnsPIDReadsTheCgroupScope(t *testing.T) {
	t.Parallel()
	testScopeOwnership(t)
}

func TestOwnsPIDFollowsTheTrackedTree(t *testing.T) {
	t.Parallel()
	testTrackerOwnership(t)
}

func TestOwnsPIDKeepsReparentedChildAcrossOutOfOrderScans(t *testing.T) {
	t.Parallel()
	table := &gatedProcTable{
		scan1Entered: make(chan struct{}),
		releaseScan1: make(chan struct{}),
	}
	tracker := newDescendantTracker(100, outOfOrderLeader, table, clock.NewFake(testEpoch()))
	proc := &Process{tracker: tracker}

	scan1Done := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() {
			close(table.releaseScan1)
		})
	}
	t.Cleanup(func() {
		release()
		<-scan1Done
	})
	supervise.Go(t.Context(), "test.stale-tracker-scan", func(context.Context) error {
		defer close(scan1Done)
		tracker.snapshot()
		return nil
	})
	<-table.scan1Entered

	// Newer scan observes the child under leader and records it before the older
	// pre-fork snapshot finishes scanning.
	if owned, err := proc.OwnsPID(101); err != nil || !owned {
		t.Fatalf("OwnsPID(101) during newer scan = %v, %v; want true", owned, err)
	}

	// Unblock the older snapshot (missing 101) so it merges after the newer scan.
	release()
	<-scan1Done

	// Child has now reparented to PID 1 with its own process group; it stays owned
	// only if the stale scan did not prune the record added by the newer scan.
	if owned, err := proc.OwnsPID(101); err != nil || !owned {
		t.Fatalf("OwnsPID(101) after reparenting and out-of-order scan = %v, %v; want true", owned, err)
	}
}

func TestOwnsPIDOfARealProcessTree(t *testing.T) {
	t.Parallel()
	helper := spawnReadyHelper(t.Context(), t, fakeVendorExe(t))
	if owned, err := helper.OwnsPID(helper.PID()); err != nil || !owned {
		t.Fatalf("OwnsPID(leader) = %v, %v; want true", owned, err)
	}
	if empty, err := helper.TreeEmpty(); err != nil || empty {
		t.Fatalf("TreeEmpty() while leader alive = %v, %v; want false, nil", empty, err)
	}
	if owned, err := helper.OwnsPID(os.Getpid()); err != nil || owned {
		t.Fatalf("OwnsPID(the test process) = %v, %v; want false", owned, err)
	}
}

func TestProcessTreeEmpty(t *testing.T) {
	t.Parallel()
	assertScopedTreeEmpty(t)
	assertTrackerTreeEmpty(t)
}

func assertScopedTreeEmpty(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := writeRootFile(dir, "cgroup.procs", []byte("4242\n")); err != nil {
		t.Fatal(err)
	}
	scoped := &Process{scope: &cgroupScope{unitName: "fugue-vendor-s2.scope", dirPath: dir}}
	if empty, err := scoped.TreeEmpty(); err != nil || empty {
		t.Fatalf("scoped TreeEmpty(non-empty) = %v, %v; want false, nil", empty, err)
	}
	if err := writeRootFile(dir, "cgroup.procs", nil); err != nil {
		t.Fatal(err)
	}
	if empty, err := scoped.TreeEmpty(); err != nil || !empty {
		t.Fatalf("scoped TreeEmpty(empty) = %v, %v; want true, nil", empty, err)
	}
	if err := writeRootFile(dir, "cgroup.procs", []byte("bad\n")); err != nil {
		t.Fatal(err)
	}
	if empty, err := scoped.TreeEmpty(); err == nil || empty {
		t.Fatalf("scoped TreeEmpty(unreadable) = %v, %v; want error", empty, err)
	}
}

func assertTrackerTreeEmpty(t *testing.T) {
	t.Helper()
	aliveTable := fakeProcTable{entries: []procEntry{{pid: 100, ppid: 50, pgid: 100, startToken: outOfOrderLeader}}}
	trackerAlive := newDescendantTracker(100, outOfOrderLeader, aliveTable, clock.NewFake(testEpoch()))
	if empty, err := (&Process{tracker: trackerAlive}).TreeEmpty(); err != nil || empty {
		t.Fatalf("tracker TreeEmpty(leader alive) = %v, %v; want false, nil", empty, err)
	}
	trackerAlive.source = fakeProcTable{}
	if empty, err := (&Process{tracker: trackerAlive}).TreeEmpty(); err != nil || !empty {
		t.Fatalf("tracker TreeEmpty(all exited) = %v, %v; want true, nil", empty, err)
	}
	if empty, err := (&Process{}).TreeEmpty(); err == nil || empty {
		t.Fatalf("TreeEmpty(no containment) = %v, %v; want error", empty, err)
	}
}
