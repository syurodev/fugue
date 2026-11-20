//go:build !unix

package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
)

func configureExclusiveCmd(*exec.Cmd) {}

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
