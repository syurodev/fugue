//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
)

const (
	fakeVendorModeHoldPipes        = "hold-pipes"
	fakeVendorModeSetsidGrandchild = "setsid-grandchild"
)

func unixModes() map[string]func() {
	return map[string]func(){
		fakeVendorModeSetsidGrandchild:  runSetsidGrandchild,
		"setsid-grandchild-stdout-only": runSetsidGrandchildStdoutOnly,
		"fake-systemd-run":              runFakeSystemdRun,
		fakeVendorModeHoldPipes:         runHoldPipes,
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
		Args:        []string{exe, fakeVendorFlag, fakeVendorModeHoldPipes},
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

// runSetsidGrandchildStdoutOnly starts a grandchild in its own session that
// inherits only stdout, pointing stderr to /dev/null. When the leader dies
// on SIGINT, stderr reaches EOF at once, isolating stdout as the sole pipe
// held open by an escaped process.
func runSetsidGrandchildStdoutOnly() {
	exe, err := os.Executable()
	if err != nil {
		os.Exit(1)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		os.Exit(1)
	}
	grandchild := &exec.Cmd{
		Path:        exe,
		Args:        []string{exe, fakeVendorFlag, fakeVendorModeHoldPipes},
		Stdout:      os.Stdout,
		Stderr:      devNull,
		SysProcAttr: &syscall.SysProcAttr{Setsid: true},
	}
	startErr := grandchild.Start()
	closeQuietly(devNull)
	if startErr != nil {
		os.Exit(1)
	}
	writeQuietly(os.Stdout, fmt.Sprintf("GRANDCHILD %d\n", grandchild.Process.Pid))
	writeQuietly(os.Stdout, "READY\n")
	blockForever()
}

func runHoldPipes() {
	blockForever()
}

// runFakeSystemdRun emulates systemd-run --user --scope --unit=<name> -- <argv...>
// by recording its PID into FUGUE_TEST_CGROUP_ROOT and FUGUE_TEST_PROC_ROOT
// before running the target fake vendor mode.
func runFakeSystemdRun() {
	idx := -1
	var unitName string
	for i, arg := range os.Args {
		if v, ok := cutPrefix(arg, "--unit="); ok {
			unitName = v
		}
		if arg == "--" {
			idx = i
			break
		}
	}
	if idx < 0 || idx+1 >= len(os.Args) {
		os.Exit(2)
	}
	targetArgv := os.Args[idx+1:]
	if len(targetArgv) == 1 && targetArgv[0] == "true" {
		os.Exit(0)
	}
	setupFakeSystemdFiles(unitName)
	for i, arg := range targetArgv {
		if arg == fakeVendorFlag && i+1 < len(targetArgv) {
			runFakeVendor(targetArgv[i+1])
			return
		}
	}
	os.Exit(2)
}

func cutPrefix(s, prefix string) (string, bool) {
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		return s[len(prefix):], true
	}
	return "", false
}

func setupFakeSystemdFiles(unitName string) {
	var cgRoot, procRoot, procCgOverride string
	for _, entry := range os.Environ() {
		if v, ok := cutPrefix(entry, "FUGUE_TEST_CGROUP_ROOT="); ok {
			cgRoot = v
		} else if v, ok := cutPrefix(entry, "FUGUE_TEST_PROC_ROOT="); ok {
			procRoot = v
		} else if v, ok := cutPrefix(entry, "FUGUE_TEST_PROC_CGROUP_OVERRIDE="); ok {
			procCgOverride = v
		}
	}
	pid := os.Getpid()
	if cgRoot != "" && unitName != "" {
		if !writeFakeCgroupUnit(cgRoot, unitName, pid) {
			os.Exit(1)
		}
	}
	if procRoot != "" && unitName != "" {
		if !writeFakeProcEntry(procRoot, unitName, procCgOverride, pid) {
			os.Exit(1)
		}
	}
}

func writeFakeCgroupUnit(cgRoot, unitName string, pid int) bool {
	root, err := os.OpenRoot(cgRoot)
	if err != nil {
		return false
	}
	defer closeQuietly(root)
	relDir := "user.slice/" + unitName
	if err := root.MkdirAll(relDir, 0o700); err != nil {
		return false
	}
	if err := root.WriteFile(relDir+"/cgroup.procs", []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		return false
	}
	return root.WriteFile(relDir+"/cgroup.events", []byte("populated 1\nfrozen 0\n"), 0o600) == nil
}

func writeFakeProcEntry(procRoot, unitName, procCgOverride string, pid int) bool {
	root, err := os.OpenRoot(procRoot)
	if err != nil {
		return false
	}
	defer closeQuietly(root)
	pidRel := strconv.Itoa(pid)
	if err := root.MkdirAll(pidRel, 0o700); err != nil {
		return false
	}
	statLine := fmt.Sprintf("%d (vendor) S 1 %d %d 0 -1 4194304 100 0 0 0 1 1 0 0 20 0 1 0 98765 1000 100 18446744073709551615 1 1 0 0 0 0 0 0 0 0 0 0 17 0 0 0 0 0 0\n", pid, pid, pid)
	if err := root.WriteFile(pidRel+"/stat.tmp", []byte(statLine), 0o600); err != nil {
		return false
	}
	if err := root.Rename(pidRel+"/stat.tmp", pidRel+"/stat"); err != nil {
		return false
	}
	cgText := fmt.Sprintf("0::/user.slice/%s\n", unitName)
	if procCgOverride != "" {
		cgText = procCgOverride + "\n"
	}
	if err := root.WriteFile(pidRel+"/cgroup.tmp", []byte(cgText), 0o600); err != nil {
		return false
	}
	return root.Rename(pidRel+"/cgroup.tmp", pidRel+"/cgroup") == nil
}
