//go:build windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

// ErrNoHeavyLock reports the one platform without flock. Local gate machines
// are macOS and Linux, so exclusive fails closed there instead of running
// unlocked and silently dropping the machine-wide cap.
var ErrNoHeavyLock = errors.New("heavy gate lock is unsupported on this platform")

func tryLock(*os.File) (bool, error) {
	return false, ErrNoHeavyLock
}

func lockBlocking(*os.File) error {
	return ErrNoHeavyLock
}

func signalExitCode(*exec.ExitError) (int, bool) {
	return 0, false
}
