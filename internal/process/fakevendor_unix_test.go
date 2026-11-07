//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
)

func unixModes() map[string]func() {
	return map[string]func(){
		"setsid-grandchild": runSetsidGrandchild,
		"hold-pipes":        runHoldPipes,
	}
}

// blockForever parks the fake vendor until a signal with a default action
// kills it. A bare `select {}` would not do: with no other goroutine the Go
// runtime aborts it as a deadlock; a registered signal keeps it alive.
func blockForever() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGUSR1)
	for {
		<-sigCh
	}
}

// runSetsidGrandchild starts a grandchild in its own session (the Codex
// orphan from the spike). It inherits stdout and stderr, so it holds the
// write ends after the group is killed.
func runSetsidGrandchild() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	grandchild := &exec.Cmd{
		Path:        exe,
		Args:        []string{exe, fakeVendorFlag, "hold-pipes"},
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	if startErr := grandchild.Start(); startErr != nil {
		os.Exit(1)
	}
	writeQuietly(os.Stdout, fmt.Sprintf("GRANDCHILD %d\n", grandchild.Process.Pid))
	writeQuietly(os.Stdout, "READY\n")
	blockForever()
}

func runHoldPipes() {
	blockForever()
}
