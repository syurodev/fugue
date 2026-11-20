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

type exclusiveTTYProbe struct {
	fd     int
	isTTY  bool
	fgPgrp int
}

func prepareExclusiveProcess(stdin io.Reader) exclusiveProcessSpec {
	return selectExclusiveProcessSpec(probeExclusiveTTY(stdin), unix.Getpgrp())
}

func selectExclusiveProcessSpec(probe exclusiveTTYProbe, selfPgrp int) exclusiveProcessSpec {
	if probe.isTTY && probe.fgPgrp == selfPgrp {
		return exclusiveProcessSpec{
			sysProcAttr: &syscall.SysProcAttr{
				Setpgid:    true,
				Foreground: true,
				Ctty:       probe.fd,
			},
			ttyFD:         probe.fd,
			restorePgrp:   probe.fgPgrp,
			hasForeground: true,
		}
	}
	return exclusiveProcessSpec{
		sysProcAttr: &syscall.SysProcAttr{Setpgid: true},
	}
}

func probeExclusiveTTY(stdin io.Reader) exclusiveTTYProbe {
	f, ok := stdin.(*os.File)
	if !ok || f == nil {
		return exclusiveTTYProbe{}
	}
	conn, err := f.SyscallConn()
	if err != nil {
		return exclusiveTTYProbe{}
	}
	var res exclusiveTTYProbe
	cerr := conn.Control(func(rawFD uintptr) {
		if rawFD > math.MaxInt {
			return
		}
		fd := int(rawFD)
		if _, terr := unix.IoctlGetTermios(fd, ioctlReadTermios); terr != nil {
			return
		}
		fg, gerr := unix.IoctlGetInt(fd, unix.TIOCGPGRP)
		if gerr != nil {
			return
		}
		res = exclusiveTTYProbe{fd: fd, isTTY: true, fgPgrp: fg}
	})
	if cerr != nil || !res.isTTY {
		return exclusiveTTYProbe{}
	}
	return res
}

func defaultExclusiveHooks() exclusiveHooks {
	return exclusiveHooks{
		prepareProcess: prepareExclusiveProcess,
		restoreForeground: func(spec exclusiveProcessSpec) {
			restoreForegroundPgrp(spec, setForegroundPgrp)
		},
	}
}

func setForegroundPgrp(fd, pgrp int) error {
	if err := unix.IoctlSetPointerInt(fd, unix.TIOCSPGRP, pgrp); err != nil {
		return fmt.Errorf("tcsetpgrp: %w", err)
	}
	return nil
}

func restoreForegroundPgrp(spec exclusiveProcessSpec, setPgrp func(fd, pgrp int) error) {
	if !spec.hasForeground {
		return
	}
	signal.Ignore(unix.SIGTTOU)
	defer signal.Reset(unix.SIGTTOU)
	if err := setPgrp(spec.ttyFD, spec.restorePgrp); err != nil && !errors.Is(err, unix.ENOTTY) {
		slog.Error("restore exclusive foreground process group", "fd", spec.ttyFD, "err", err)
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
