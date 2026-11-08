//go:build unix

package process

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strconv"
	"syscall"
)

const (
	fakeVendorModeHoldPipes        = "hold-pipes"
	fakeVendorModeSetsidGrandchild = "setsid-grandchild"
)

func isFakeSystemdRunInvocation() bool {
	return len(os.Args) > 2 && os.Args[1] == "--user" && os.Args[2] == "--scope"
}

func unixModes() map[string]func() {
	if !isFakeSystemdRunInvocation() {
		maybeWriteStandaloneFakeProcStat()
	}
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

func maybeWriteStandaloneFakeProcStat() {
	var procRoot, skipStat string
	for _, entry := range os.Environ() {
		if v, ok := cutPrefix(entry, "FUGUE_TEST_PROC_ROOT="); ok {
			procRoot = v
		} else if v, ok := cutPrefix(entry, "FUGUE_TEST_SKIP_PROC_STAT="); ok {
			skipStat = v
		}
	}
	if procRoot == "" || skipStat == "1" {
		return
	}
	pid := os.Getpid()
	if !writeFakeProcStatOnly(procRoot, pid) {
		os.Exit(1)
	}
}

// runFakeSystemdRun emulates systemd-run --user --scope --unit=<name> -- <argv...>
// by verifying XDG_RUNTIME_DIR is present in its environment, starting in the
// parent's cgroup, moving into the scope under the current user's
// user@<uid>.service subtree, and then executing the target fake vendor mode.
func runFakeSystemdRun() {
	if !hasFakeXDGRuntimeDir() {
		os.Exit(1)
	}
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

func hasFakeXDGRuntimeDir() bool {
	for _, entry := range os.Environ() {
		if v, ok := cutPrefix(entry, "XDG_RUNTIME_DIR="); ok && v != "" {
			return true
		}
	}
	return false
}

func fakeUserManagerRel() string {
	uid := os.Getuid()
	return fmt.Sprintf("user.slice/user-%d.slice/user@%d.service", uid, uid)
}

func readFakeSystemdEnv() (cgRoot, procRoot, procCgOverride string) {
	for _, entry := range os.Environ() {
		if v, ok := cutPrefix(entry, "FUGUE_TEST_CGROUP_ROOT="); ok {
			cgRoot = v
		} else if v, ok := cutPrefix(entry, "FUGUE_TEST_PROC_ROOT="); ok {
			procRoot = v
		} else if v, ok := cutPrefix(entry, "FUGUE_TEST_PROC_CGROUP_OVERRIDE="); ok {
			procCgOverride = v
		}
	}
	return cgRoot, procRoot, procCgOverride
}

func setupFakeSystemdFiles(unitName string) {
	if unitName == "" {
		return
	}
	cgRoot, procRoot, procCgOverride := readFakeSystemdEnv()
	pid := os.Getpid()
	if procRoot != "" {
		stageParentCgroup(procRoot, pid)
	}
	if cgRoot != "" && !writeFakeCgroupUnit(cgRoot, unitName, pid) {
		os.Exit(1)
	}
	if procRoot != "" {
		stageMovedScopeCgroup(procRoot, unitName, procCgOverride, pid)
	}
}

func stageParentCgroup(procRoot string, pid int) {
	parentCg := "0::/" + fakeUserManagerRel() + "/init.scope"
	if !writeFakeProcStatAndCgroup(procRoot, parentCg, pid) {
		os.Exit(1)
	}
	waitFakeScopeMoveGate(procRoot)
}

func stageMovedScopeCgroup(procRoot, unitName, procCgOverride string, pid int) {
	scopeCg := "0::/" + fakeUserManagerRel() + "/app.slice/" + unitName
	if procCgOverride != "" {
		scopeCg = procCgOverride
	}
	if !writeFakeProcCgroup(procRoot, scopeCg, pid) {
		os.Exit(1)
	}
	if err := writeRootFile(procRoot, "stage-scope-moved", []byte("1")); err != nil {
		os.Exit(1)
	}
}

func waitFakeScopeMoveGate(procRoot string) {
	if err := writeRootFile(procRoot, "stage-parent-ready", []byte("1")); err != nil {
		os.Exit(1)
	}
	if _, err := readRootFile(procRoot, "wait-for-poll"); err != nil {
		return
	}
	for {
		if _, err := readRootFile(procRoot, "allow-scope-move"); err == nil {
			return
		}
		runtime.Gosched()
	}
}

func writeFakeCgroupUnit(cgRoot, unitName string, pid int) bool {
	root, err := os.OpenRoot(cgRoot)
	if err != nil {
		return false
	}
	defer closeQuietly(root)
	relDir := fakeUserManagerRel() + "/app.slice/" + unitName
	if err := root.MkdirAll(relDir, 0o700); err != nil {
		return false
	}
	if err := root.WriteFile(relDir+"/cgroup.procs", []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		return false
	}
	return root.WriteFile(relDir+"/cgroup.events", []byte("populated 1\nfrozen 0\n"), 0o600) == nil
}

func writeFakeProcStatOnly(procRoot string, pid int) bool {
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
	return root.WriteFile("stat-ready", []byte("1"), 0o600) == nil
}

func writeFakeProcStatAndCgroup(procRoot, cgLine string, pid int) bool {
	if !writeFakeProcStatOnly(procRoot, pid) {
		return false
	}
	return writeFakeProcCgroup(procRoot, cgLine, pid)
}

func writeFakeProcCgroup(procRoot, cgLine string, pid int) bool {
	root, err := os.OpenRoot(procRoot)
	if err != nil {
		return false
	}
	defer closeQuietly(root)
	pidRel := strconv.Itoa(pid)
	if err := root.WriteFile(pidRel+"/cgroup.tmp", []byte(cgLine+"\n"), 0o600); err != nil {
		return false
	}
	return root.Rename(pidRel+"/cgroup.tmp", pidRel+"/cgroup") == nil
}
