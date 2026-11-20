//go:build unix

package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

func configureExclusiveCmd(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killExclusiveProcessGroup(proc *os.Process) {
	if proc == nil || proc.Pid <= 0 {
		return
	}
	if err := syscall.Kill(-proc.Pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		slog.Error("kill exclusive child process group", "pid", proc.Pid, "err", err)
	}
}

func signalExitCode(exitErr *exec.ExitError) (int, bool) {
	status, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return exitSignaled + int(status.Signal()), true
}
