//go:build unix

package process

import (
	"errors"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

func killGroup(pid int, sig syscall.Signal) {
	if pid <= 1 {
		return
	}
	// Signal the process group (-pid) and direct pid.
	// Ignore ESRCH since process may have already exited.
	if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return
	}
	if err := syscall.Kill(pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
		return
	}
}
