//go:build unix

package process

import (
	"errors"
	"os"
	"syscall"
)

func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true}
}

// groupHasMembers probes the process group once. Any answer other than
// ESRCH (including EPERM) counts as members present, so the stop path fails
// toward continuing escalation.
func groupHasMembers(pgid int) bool {
	if pgid <= 1 {
		return false
	}
	return !errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
}

// signalTree sends sig to the targets planSignal allows. The leader is
// signaled through os.Process, which refuses a process that Wait has
// already reaped.
func (p *Process) signalTree(sig syscall.Signal) error {
	pid := p.PID()
	if pid <= 1 {
		return nil
	}
	exited := p.isLeaderExited()
	plan := planSignal(exited, !exited || groupHasMembers(pid))

	var errs []error
	if plan.group {
		if err := syscall.Kill(-pid, sig); err != nil && !errors.Is(err, syscall.ESRCH) {
			errs = append(errs, err)
		}
	}
	if plan.leader {
		if err := p.cmd.Process.Signal(sig); err != nil && !errors.Is(err, os.ErrProcessDone) {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
