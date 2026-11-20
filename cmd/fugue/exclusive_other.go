//go:build !unix

package main

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"syscall"
)

func exclusiveProcAttr(io.Reader) *syscall.SysProcAttr {
	// Non-Unix platforms (including Windows) do not have POSIX-style process
	// groups via Setpgid, so child commands run without Unix pgid attributes.
	return nil
}

func defaultExclusiveHooks() exclusiveHooks {
	return exclusiveHooks{
		prepareProcess: exclusiveProcAttr,
	}
}

func killExclusiveProcessGroup(proc *os.Process) {
	if proc == nil {
		return
	}
	if err := proc.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		slog.Error("kill exclusive child process", "err", err)
	}
}

func signalExitCode(*exec.ExitError) (int, bool) {
	return 0, false
}
