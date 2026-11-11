//go:build darwin

package process

import (
	"errors"
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
		return false, err
	}
	for i := range kprocs {
		proc := &kprocs[i]
		if int(proc.Eproc.Pgid) == pgid && proc.Proc.P_stat != 0 && proc.Proc.P_stat != sZomb {
			return true, nil
		}
	}
	return false, nil
}
