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
		if int(proc.Eproc.Pgid) == pgid && proc.Proc.P_stat != 0 && proc.Proc.P_stat != sZomb {
			return true, nil
		}
	}
	return false, nil
}

// platformLeaderSignalError treats EPERM from signaling the leader as "gone"
// only when the leader is a zombie or absent. XNU returns EPERM for a
// zombie, and the leader is one between its exit and the Wait that reaps it,
// a window the exit channel does not yet report. A live leader keeps the
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
			return proc.Proc.P_stat != 0 && proc.Proc.P_stat != sZomb, nil
		}
	}
	return false, nil
}
