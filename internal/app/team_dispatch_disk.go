package app

// The §12 disk-full handling of the scheduler (F11.9): a worktree creation or
// task start that hits ENOSPC ends that task failed(disk) and pauses new
// worktree-bound dispatches until space is free again. Free space comes from
// the injected DiskSpaceChecker port at the platform boundary, re-checked on
// a timer of the injected clock; tasks that need no new worktree (the lead
// binding) and tasks already running are unaffected.

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	// diskFreeThresholdBytes is the free-space floor under the data directory
	// below which the scheduler stops creating worktrees (row note F11.9):
	// 1 GiB, well above the largest expected task worktree plus git scratch.
	diskFreeThresholdBytes = 1 << 30
	// diskRecheckInterval is how often the pause re-checks free space (row
	// note F11.9).
	diskRecheckInterval = 30 * time.Second
	// diskFullText is the kernel message ENOSPC surfaces in error strings.
	diskFullText = "no space left on device"
	// waitDisk is the visible queue wait reason while the disk pause holds
	// new worktree-bound starts (§12).
	waitDisk = "disk"
)

// isDiskFull reports whether err is the §12 disk-full failure: the gitops
// sentinel, the OS ENOSPC, or the kernel's own message.
func isDiskFull(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, gitops.ErrDiskFull) ||
		errors.Is(err, syscall.ENOSPC) ||
		strings.Contains(strings.ToLower(err.Error()), diskFullText)
}

// diskRoomForWorktree reports whether a task may create a worktree now: the
// pause blocks it, and the injected port is checked once per start while the
// pause is clear. A port error is not evidence of a full disk, so the start
// proceeds and the real failure path classifies it.
func (d *teamDispatcher) diskRoomForWorktree(ctx context.Context) bool {
	if d.diskPaused() {
		return false
	}
	if d.deps.Disk == nil {
		return true
	}
	free, err := d.deps.Disk.FreeDiskBytes(d.deps.DataDir)
	if err == nil && free >= diskFreeThresholdBytes {
		return true
	}
	if err == nil {
		slog.WarnContext(ctx, "pausing worktree creation: free disk space under the data directory is low", "free_bytes", free, "threshold_bytes", diskFreeThresholdBytes)
	}
	d.setDiskPaused(true)
	return false
}

// noteDiskFullFailure pauses worktree creation after a task start hit the
// disk-full failure (§12): the task itself ends failed(disk).
func (d *teamDispatcher) noteDiskFullFailure(ctx context.Context, err error) {
	slog.WarnContext(ctx, "pausing worktree creation after a disk-full failure", "error_code", string(errcode.CodeOf(err)))
	d.setDiskPaused(true)
}

func (d *teamDispatcher) diskPaused() bool {
	d.diskMu.Lock()
	defer d.diskMu.Unlock()
	return d.diskWait
}

func (d *teamDispatcher) setDiskPaused(paused bool) {
	d.diskMu.Lock()
	changed := d.diskWait != paused
	d.diskWait = paused
	d.diskMu.Unlock()
	if changed && paused {
		select {
		case d.diskWake <- struct{}{}:
		default:
		}
	}
}

// Start runs the disk re-check loop on the host's lifetime. While the pause
// holds, the loop re-checks the injected port every diskRecheckInterval and
// resumes dispatching when space is free again; the fake clock drives the
// timer, so tests never sleep.
func (d *teamDispatcher) Start(base context.Context) {
	if d.deps.GoTask != nil {
		d.deps.GoTask("team-dispatch-disk-watch", d.diskLoop)
		return
	}
	supervise.Go(base, "team-dispatch-disk-watch", d.diskLoop)
}

func (d *teamDispatcher) diskLoop(ctx context.Context) error {
	var timer clock.Timer
	for {
		if !d.diskPaused() {
			select {
			case <-ctx.Done():
				return nil
			case <-d.diskWake:
			}
			continue
		}
		timer = d.deps.Clock.NewTimer(diskRecheckInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-d.diskWake:
			timer.Stop()
		case <-timer.C():
		}
		if d.recheckDisk() {
			if err := d.DispatchAll(ctx); err != nil && ctx.Err() == nil {
				slog.WarnContext(ctx, "resuming dispatch after the disk pause failed", "error_code", string(errcode.CodeOf(err)))
			}
		}
	}
}

// recheckDisk clears the pause when the injected port reports at least the
// threshold free; a port error keeps the pause (fail closed after real
// evidence of a full disk).
func (d *teamDispatcher) recheckDisk() bool {
	if d.deps.Disk == nil {
		d.setDiskPaused(false)
		return true
	}
	free, err := d.deps.Disk.FreeDiskBytes(d.deps.DataDir)
	if err != nil || free < diskFreeThresholdBytes {
		return false
	}
	d.setDiskPaused(false)
	return true
}

// failDiskFull ends a task failed(disk) after a start hit the disk-full
// failure and pauses the scheduler (§12). The usual failure digest reaches
// the delegator through failTaskInState.
func (d *teamDispatcher) failDiskFull(ctx context.Context, teamID, taskID string, expectedState vendorstore.TaskState, sessionID string, cause error) error {
	d.noteDiskFullFailure(ctx, cause)
	return d.failTaskInState(ctx, teamID, taskID, expectedState, sessionID, failDisk)
}
