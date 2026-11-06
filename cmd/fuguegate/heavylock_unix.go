//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

// tryLock reports whether another process already holds an exclusive flock on
// f, acquiring it otherwise.
func tryLock(f *os.File) (busy bool, err error) {
	var ferr error
	conn, cerr := f.SyscallConn()
	if cerr != nil {
		return false, fmt.Errorf("flock: %w", cerr)
	}
	if cerr := conn.Control(func(fd uintptr) {
		ferr = syscall.Flock(int(fd), syscall.LOCK_EX|syscall.LOCK_NB)
	}); cerr != nil {
		return false, fmt.Errorf("flock: %w", cerr)
	}
	switch {
	case ferr == nil:
		return false, nil
	case errors.Is(ferr, syscall.EWOULDBLOCK), errors.Is(ferr, syscall.EAGAIN):
		return true, nil
	default:
		return false, fmt.Errorf("flock: %w", ferr)
	}
}

// signalExitCode maps a child that a signal killed to the conventional
// 128+signal exit code.
func signalExitCode(exit *exec.ExitError) (int, bool) {
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 0, false
	}
	return exitSignaled + int(status.Signal()), true
}
