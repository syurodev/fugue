//go:build unix

package process

import (
	"errors"
	"runtime"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int, sig syscall.Signal) error {
	if pid <= 1 {
		return nil
	}
	var firstErr error
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		firstErr = err
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		if firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// isGroupEmpty probes whether the process group has any surviving processes.
// It yields briefly to allow the kernel/init to reap terminated children.
func isGroupEmpty(pgid int) bool {
	if pgid <= 1 {
		return true
	}
	for range 5000 {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return true
		}
		runtime.Gosched()
	}
	return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}
