//go:build unix

package process

import (
	"errors"
	"os"
	"testing"

	"github.com/syurodev/fugue/internal/platform/clock"
)

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
	}}
	tracker := newDescendantTracker(100, "leader", table, clock.NewFake(testEpoch()))
	// 300 was a descendant once; the pid now belongs to another process.
	tracker.tracked[300] = "exited-descendant"
	proc := &Process{tracker: tracker}
	for pid, want := range map[int]bool{100: true, 101: true, 102: true, 200: false, 300: false} {
		if got, err := proc.OwnsPID(pid); err != nil || got != want {
			t.Errorf("OwnsPID(%d) = %v, %v; want %v", pid, got, err, want)
		}
	}
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

func TestOwnsPIDReadsTheCgroupScope(t *testing.T) {
	t.Parallel()
	testScopeOwnership(t)
}

func TestOwnsPIDFollowsTheTrackedTree(t *testing.T) {
	t.Parallel()
	testTrackerOwnership(t)
}

func TestOwnsPIDOfARealProcessTree(t *testing.T) {
	t.Parallel()
	helper := spawnReadyHelper(t.Context(), t, fakeVendorExe(t))
	if owned, err := helper.OwnsPID(helper.PID()); err != nil || !owned {
		t.Fatalf("OwnsPID(leader) = %v, %v; want true", owned, err)
	}
	if owned, err := helper.OwnsPID(os.Getpid()); err != nil || owned {
		t.Fatalf("OwnsPID(the test process) = %v, %v; want false", owned, err)
	}
}
