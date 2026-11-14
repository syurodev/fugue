//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/supervise"
)

// trackerInterval is how often the descendant tracker snapshots the OS process
// table. 100 ms catches short-lived forks and setsid helpers in normal CLI
// tool loops while keeping sysctl / /proc scan overhead under 1% of a core.
//
// Limitation: a child that calls setsid() and whose parent exits inside a single
// 100 ms window between two snapshots can escape tracking. Because user-space
// polling cannot close that race without a kernel container (cgroups v2), stop
// outcomes on macOS and uncontained Linux are always StopProofUnknown.
const trackerInterval = 100 * time.Millisecond

// procTableSource reads the OS process table for descendant tracking and start-time checks.
// Two implementations exist: procfsScanner on Linux (/proc/<pid>/stat) and
// sysctlScanner on macOS (sysctl kern.proc.all).
type procTableSource interface {
	scan() ([]procEntry, error)
	startToken(pid int) (string, error)
}

// descendantTracker records every process seen in the leader's process group
// or reachable from a tracked process via ppid, retaining processes even after
// they reparent to init or call setsid().
type descendantTracker struct {
	leaderPID int
	source    procTableSource
	clock     clock.Clock
	disabled  bool

	mu        sync.Mutex
	tracked   map[int]string // pid -> startToken when first observed
	task      *supervise.Task
	stop      context.CancelFunc
	closeOnce sync.Once
}

func newDescendantTracker(leaderPID int, leaderToken string, source procTableSource, clk clock.Clock) *descendantTracker {
	tracked := make(map[int]string)
	if leaderPID > 1 && leaderToken != "" {
		tracked[leaderPID] = leaderToken
	}
	return &descendantTracker{
		leaderPID: leaderPID,
		source:    source,
		clock:     clk,
		tracked:   tracked,
	}
}

func (d *descendantTracker) start(ctx context.Context) {
	d.snapshot()
	loopCtx, cancel := context.WithCancel(ctx)
	d.stop = cancel
	d.task = supervise.Go(loopCtx, "process.descendant_tracker", func(ctx context.Context) error {
		return d.runLoop(ctx)
	})
}

func (d *descendantTracker) runLoop(ctx context.Context) error {
	for {
		timer := d.clock.NewTimer(trackerInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C():
			d.snapshot()
		}
	}
}

func (d *descendantTracker) close() {
	d.closeOnce.Do(func() {
		if d.stop != nil {
			d.stop()
		}
		if d.task != nil {
			if err := d.task.Wait(); err != nil {
				return
			}
		}
	})
}

// snapshot scans the OS process table once and records any newly discovered
// group members or children of already-tracked processes.
func (d *descendantTracker) snapshot() {
	d.mu.Lock()
	if d.disabled {
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	entries, err := d.source.scan()
	if err != nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mergeDescendants(d.tracked, d.leaderPID, entries)
}

func mergeDescendants(tracked map[int]string, leaderPID int, entries []procEntry) {
	byPID := indexProcEntries(entries)
	pruneStaleTracked(tracked, leaderPID, byPID)
	leaderReused := syncLeaderTracked(tracked, leaderPID, byPID)
	// Fixed-point propagation through ppid chains and pgid membership.
	for {
		added := false
		for _, e := range byPID {
			if !shouldTrackEntry(tracked, leaderPID, leaderReused, e) {
				continue
			}
			tracked[e.pid] = e.startToken
			added = true
		}
		if !added {
			return
		}
	}
}

func indexProcEntries(entries []procEntry) map[int]procEntry {
	byPID := make(map[int]procEntry, len(entries))
	for _, e := range entries {
		if e.pid > 1 && e.startToken != "" {
			byPID[e.pid] = e
		}
	}
	return byPID
}

// pruneStaleTracked drops non-leader records whose PID exited or was reused
// before propagating ancestry so a reused PID cannot taint its new children.
func pruneStaleTracked(tracked map[int]string, leaderPID int, byPID map[int]procEntry) {
	for pid, token := range tracked {
		if pid == leaderPID {
			continue
		}
		if e, ok := byPID[pid]; !ok || e.startToken != token {
			delete(tracked, pid)
		}
	}
}

func syncLeaderTracked(tracked map[int]string, leaderPID int, byPID map[int]procEntry) bool {
	leader, ok := byPID[leaderPID]
	if !ok {
		return false
	}
	want, seen := tracked[leaderPID]
	if !seen {
		tracked[leaderPID] = leader.startToken
		return false
	}
	return want != leader.startToken
}

func shouldTrackEntry(tracked map[int]string, leaderPID int, leaderReused bool, e procEntry) bool {
	if _, already := tracked[e.pid]; already {
		return false
	}
	if leaderReused && (e.pgid == leaderPID || e.ppid == leaderPID) {
		return false
	}
	if e.pgid == leaderPID && leaderPID > 1 {
		return true
	}
	_, parentTracked := tracked[e.ppid]
	return parentTracked
}

// liveTrackedPIDs takes a fresh snapshot and returns all tracked PIDs (excluding
// leaderPID, which Stop signals via os.Process/group) whose current startToken
// still matches the recorded token. A PID whose startToken differs is never
// returned (protecting against PID reuse).
func (d *descendantTracker) liveTrackedPIDs() []int {
	d.mu.Lock()
	if d.disabled {
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()
	entries, err := d.source.scan()
	if err != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	mergeDescendants(d.tracked, d.leaderPID, entries)

	current := make(map[int]string, len(entries))
	for _, e := range entries {
		current[e.pid] = e.startToken
	}

	var live []int
	for pid, wantToken := range d.tracked {
		if pid == d.leaderPID || pid <= 1 || wantToken == "" {
			continue
		}
		if gotToken, ok := current[pid]; ok && gotToken == wantToken {
			live = append(live, pid)
		}
	}
	return live
}

// signalTracked sends sig to every verified live tracked descendant PID.
func (d *descendantTracker) signalTracked(sig syscall.Signal) error {
	var errs []error
	for _, pid := range d.liveTrackedPIDs() {
		if err := d.killVerifiedPID(pid, sig); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (d *descendantTracker) killVerifiedPID(pid int, sig syscall.Signal) error {
	err := syscall.Kill(pid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	// On macOS (XNU), signaling a process that just transitioned to SZOMB (for
	// example, after signalTree signaled its process group) returns EPERM once
	// it is reparented to launchd. Ignore EPERM if the PID is no longer active.
	if errors.Is(err, syscall.EPERM) && !d.isActivePID(pid) {
		return nil
	}
	return fmt.Errorf("signal tracked pid %d: %w", pid, err)
}

func (d *descendantTracker) isActivePID(pid int) bool {
	_, err := d.source.startToken(pid)
	return err == nil
}

func (d *descendantTracker) hasLiveDescendants() bool {
	return len(d.liveTrackedPIDs()) > 0
}
