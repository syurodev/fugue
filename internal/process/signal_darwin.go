//go:build darwin

package process

import (
	"errors"
	"fmt"
	"syscall"

	"golang.org/x/sys/unix"
)

func platformGroupSignalError(pgid int, err error) error {
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	live, scanErr := sysctlGroupHasLiveMembers(pgid)
	if scanErr == nil && !live {
		return nil
	}
	return err
}

func sysctlGroupHasLiveMembers(pgid int) (bool, error) {
	if pgid <= 1 {
		return false, nil
	}
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return false, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	for i := range kprocs {
		proc := &kprocs[i]
		if int(proc.Eproc.Pgid) == pgid && kinfoCanBeSignaled(proc) {
			return true, nil
		}
	}
	return false, nil
}

// platformLeaderSignalError treats EPERM from signaling the leader as "gone"
// only when the leader is a zombie, mid-exit or absent. XNU returns EPERM for
// both, and the leader is one of them between its death and the Wait that
// reaps it, a window the exit channel does not yet report; in a one-process
// group it is the same process the group signal just failed on. A live leader keeps the
// error, and so does an unreadable process table: fail toward continuing.
func platformLeaderSignalError(pid int, err error) error {
	if !errors.Is(err, syscall.EPERM) {
		return err
	}
	live, scanErr := sysctlPIDIsLive(pid)
	if scanErr == nil && !live {
		return nil
	}
	return err
}

func sysctlPIDIsLive(pid int) (bool, error) {
	kprocs, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return false, fmt.Errorf("sysctl kern.proc.all: %w", err)
	}
	for i := range kprocs {
		proc := &kprocs[i]
		if int(proc.Proc.P_pid) == pid {
			return kinfoCanBeSignaled(proc), nil
		}
	}
	return false, nil
}
