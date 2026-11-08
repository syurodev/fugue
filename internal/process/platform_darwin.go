//go:build darwin

package process

import (
	"context"
	"fmt"
	"strconv"

	"golang.org/x/sys/unix"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	// sZomb is the XNU process state for a zombie (bsd/sys/proc.h SZOMB = 5).
	sZomb = 5
)

func prepareSpawnArgv(ctx context.Context, opts *Options) ([]string, string, bool) {
	if opts != nil && opts.CgroupRoot != "" && opts.ProcRoot != "" {
		return prepareLinuxSpawnArgv(ctx, opts)
	}
	return opts.Argv, "", false
}

func attachPlatformContainment(
	ctx context.Context,
	p *Process,
	opts *Options,
	unitName string,
	usedSystemd bool,
) {
	if opts != nil && opts.CgroupRoot != "" && opts.ProcRoot != "" {
		attachLinuxContainment(ctx, p, opts.CgroupRoot, opts.ProcRoot, unitName, usedSystemd)
		return
	}
	scanner := sysctlScanner{}
	token, tokErr := scanner.startToken(p.PID())
	if tokErr != nil {
		token = ""
	}
	p.identity = Identity{
		PID:        p.PID(),
		StartToken: token,
		ScopeUnit:  "",
	}
	tracker := newDescendantTracker(p.PID(), token, scanner, p.clock)
	tracker.start(ctx)
	p.tracker = tracker
}

func reapPlatform(ctx context.Context, id Identity, clk clock.Clock, cgroupRoot, procRoot string) (StopProof, error) {
	if cgroupRoot != "" || procRoot != "" {
		if id.ScopeUnit != "" {
			return reapContainedScope(ctx, id.ScopeUnit, clk, cgroupRoot)
		}
		return reapByStartToken(ctx, id, clk, newProcfsScanner(procRoot))
	}
	if id.ScopeUnit != "" {
		return StopProof{Status: StopProofUnknown, Reason: ReasonScopeUnavailable}, ErrIdentityMismatch
	}
	return reapByStartToken(ctx, id, clk, sysctlScanner{})
}

// sysctlScanner implements procTableSource on macOS using sysctl kern.proc.all
// without cgo.
type sysctlScanner struct{}

func (sysctlScanner) scan() ([]procEntry, error) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	out := make([]procEntry, 0, len(kprocs))
	for i := range kprocs {
		entry, ok := kinfoToEntry(&kprocs[i])
		if ok {
			out = append(out, entry)
		}
	}
	return out, nil
}

func (sysctlScanner) startToken(pid int) (string, error) {
	if pid <= 1 {
		return "", fmt.Errorf("%w: invalid pid %d", ErrIdentityMismatch, pid)
	}
	kp, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil {
		return "", fmt.Errorf("%w: sysctl kern.proc.pid %d: %w", ErrIdentityMismatch, pid, err)
	}
	entry, ok := kinfoToEntry(kp)
	if !ok || entry.pid != pid {
		return "", fmt.Errorf("%w: pid %d not active", ErrIdentityMismatch, pid)
	}
	return entry.startToken, nil
}

func kinfoToEntry(kp *unix.KinfoProc) (procEntry, bool) {
	pid := int(kp.Proc.P_pid)
	if pid <= 1 || kp.Proc.P_stat == sZomb || kp.Proc.P_stat == 0 {
		return procEntry{}, false
	}
	sec := kp.Proc.P_starttime.Sec
	usec := int64(kp.Proc.P_starttime.Usec)
	if sec <= 0 && usec <= 0 {
		return procEntry{}, false
	}
	token := strconv.FormatInt(sec, 10) + "." + strconv.FormatInt(usec, 10)
	return procEntry{
		pid:        pid,
		ppid:       int(kp.Eproc.Ppid),
		pgid:       int(kp.Eproc.Pgid),
		startToken: token,
	}, true
}
