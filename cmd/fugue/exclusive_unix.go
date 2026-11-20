//go:build unix

package main

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"golang.org/x/sys/unix"
)

func exclusiveProcAttr(stdin io.Reader) *syscall.SysProcAttr {
	fd, isTTY := probeExclusiveTTY(stdin)
	return buildExclusiveProcAttr(isTTY, fd)
}

func buildExclusiveProcAttr(isTTY bool, fd int) *syscall.SysProcAttr {
	if isTTY {
		return &syscall.SysProcAttr{
			Setpgid:    true,
			Foreground: true,
			Ctty:       fd,
		}
	}
	return &syscall.SysProcAttr{Setpgid: true}
}

func probeExclusiveTTY(stdin io.Reader) (int, bool) {
	f, ok := stdin.(*os.File)
	if !ok || f == nil {
		return 0, false
	}
	conn, err := f.SyscallConn()
	if err != nil {
		return 0, false
	}
	var fd int
	var isTTY bool
	cerr := conn.Control(func(rawFD uintptr) {
		if rawFD > math.MaxInt {
			return
		}
		candidate := int(rawFD)
		if _, ioctlErr := unix.IoctlGetInt(candidate, unix.TIOCGPGRP); ioctlErr == nil {
			fd = candidate
			isTTY = true
		}
	})
	if cerr != nil || !isTTY {
		return 0, false
	}
	return fd, true
}

func defaultExclusiveHooks() exclusiveHooks {
	return exclusiveHooks{
		prepareProcess: exclusiveProcAttr,
		restoreForeground: func(attr *syscall.SysProcAttr) {
			if attr == nil || !attr.Foreground {
				return
			}
			restoreForegroundPgrp(attr.Ctty, setForegroundPgrp, unix.Getpgrp)
		},
	}
}

func setForegroundPgrp(fd, pgrp int) error {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgrp); err != nil {
		return fmt.Errorf("tcsetpgrp: %w", err)
	}
	return nil
}

func restoreForegroundPgrp(fd int, setPgrp func(fd, pgrp int) error, getPgrp func() int) {
	signal.Ignore(unix.SIGTTOU)
	defer signal.Reset(unix.SIGTTOU)
	if err := setPgrp(fd, getPgrp()); err != nil && !errors.Is(err, unix.ENOTTY) {
		slog.Error("restore exclusive foreground process group", "fd", fd, "err", err)
	}
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
