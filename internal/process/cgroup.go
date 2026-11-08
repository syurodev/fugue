//go:build unix

package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/syurodev/fugue/internal/platform/clock"
)

const (
	defaultCgroupRoot      = "/sys/fs/cgroup"
	defaultProcRoot        = "/proc"
	defaultSystemdRun      = "systemd-run"
	scopeUnitPrefix        = "fugue-vendor-"
	scopeUnitSuffix        = ".scope"
	maxScopeVerifyAttempts = 500000
)

func readRootFile(dir, rel string) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, fmt.Errorf("open root %s: %w", dir, err)
	}
	defer closeQuietly(root)
	data, err := root.ReadFile(filepath.FromSlash(rel))
	if err != nil {
		return nil, fmt.Errorf("read %s/%s: %w", dir, rel, err)
	}
	return data, nil
}

func writeRootFile(dir, rel string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open root %s: %w", dir, err)
	}
	defer closeQuietly(root)
	if err := root.WriteFile(filepath.FromSlash(rel), data, 0o600); err != nil {
		return fmt.Errorf("write %s/%s: %w", dir, rel, err)
	}
	return nil
}

// cgroupScope represents a verified Linux cgroup v2 scope directory.
type cgroupScope struct {
	unitName string
	dirPath  string
}

func (c *cgroupScope) isEmpty() bool {
	eventsData, err := readRootFile(c.dirPath, "cgroup.events")
	if err == nil && isCgroupEventsUnpopulated(string(eventsData)) {
		return true
	}
	procsData, err := readRootFile(c.dirPath, "cgroup.procs")
	if err != nil {
		return errors.Is(err, fs.ErrNotExist)
	}
	pids, ok := parseCgroupProcs(string(procsData))
	return ok && len(pids) == 0
}

func (c *cgroupScope) readProcs() ([]int, error) {
	data, err := readRootFile(c.dirPath, "cgroup.procs")
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cgroup.procs: %w", err)
	}
	pids, ok := parseCgroupProcs(string(data))
	if !ok {
		return nil, fmt.Errorf("%w: malformed cgroup.procs in %s", ErrStartFailed, c.unitName)
	}
	return pids, nil
}

func (c *cgroupScope) signalProcs(sig syscall.Signal, skipPID int) error {
	pids, err := c.readProcs()
	if err != nil {
		return err
	}
	var errs []error
	for _, pid := range pids {
		if pid <= 1 || pid == skipPID {
			continue
		}
		if kerr := syscall.Kill(pid, sig); kerr != nil && !errors.Is(kerr, syscall.ESRCH) {
			errs = append(errs, kerr)
		}
	}
	return errors.Join(errs...)
}

// kill writes "1" to cgroup.kill when supported (Linux >= 5.14), or falls back
// to sending SIGKILL to every PID listed in cgroup.procs.
func (c *cgroupScope) kill(skipPID int) error {
	killFile := filepath.Join(c.dirPath, "cgroup.kill")
	if _, statErr := os.Stat(killFile); statErr == nil {
		if werr := writeRootFile(c.dirPath, "cgroup.kill", []byte("1")); werr == nil {
			return nil
		}
	}
	return c.signalProcs(syscall.SIGKILL, skipPID)
}

// verifyCgroupScope reads <procRoot>/<pid>/cgroup and checks that the v2
// hierarchy path resolves to a directory under cgroupRoot ending in unitName.
// When systemd-run has just started, it waits for the scope assignment to
// become visible in <procRoot>/<pid>/cgroup while the process is alive.
func verifyCgroupScope(ctx context.Context, procRoot, cgroupRoot string, pid int, unitName string) (*cgroupScope, bool) {
	if !isValidScopeUnit(unitName) || pid <= 0 {
		return nil, false
	}
	realProcAttempts := 0
	for {
		scope, retry, isRealProcRetry, ok := tryVerifyCgroupScope(procRoot, cgroupRoot, pid, unitName)
		if ok {
			return scope, true
		}
		if !retry || ctx.Err() != nil {
			return nil, false
		}
		if isRealProcRetry {
			realProcAttempts++
			if realProcAttempts >= maxScopeVerifyAttempts {
				return nil, false
			}
		}
		runtime.Gosched()
	}
}

func tryVerifyCgroupScope(procRoot, cgroupRoot string, pid int, unitName string) (*cgroupScope, bool, bool, bool) {
	data, err := readRootFile(procRoot, strconv.Itoa(pid)+"/cgroup")
	if err != nil {
		// In fake-systemd-run tests (procRoot != defaultProcRoot), the child
		// atomically renames <procRoot>/<pid>/cgroup into place before exec.
		retry := procRoot != defaultProcRoot && errors.Is(err, fs.ErrNotExist)
		return nil, retry, false, false
	}
	rel, ok := parseProcCgroupRelPath(string(data), unitName)
	if !ok {
		// On real /proc, systemd-run starts in the caller's cgroup and moves
		// itself into unitName over D-Bus before execing the target command.
		isReal := procRoot == defaultProcRoot && isProcLiveOnRealProc(procRoot, pid)
		return nil, isReal, isReal, false
	}
	dir := filepath.Join(cgroupRoot, filepath.FromSlash(rel))
	if filepath.Base(dir) != unitName {
		return nil, false, false, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		isReal := procRoot == defaultProcRoot && isProcLiveOnRealProc(procRoot, pid)
		return nil, isReal, isReal, false
	}
	return &cgroupScope{
		unitName: unitName,
		dirPath:  dir,
	}, false, false, true
}

func isProcLiveOnRealProc(procRoot string, pid int) bool {
	data, err := readRootFile(procRoot, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return false
	}
	_, err = parseProcStatRaw(string(data))
	return err == nil
}

func isValidScopeUnit(unitName string) bool {
	return strings.HasPrefix(unitName, scopeUnitPrefix) &&
		strings.HasSuffix(unitName, scopeUnitSuffix) &&
		len(unitName) > len(scopeUnitPrefix)+len(scopeUnitSuffix) &&
		!strings.Contains(unitName, "/") &&
		!strings.Contains(unitName, "..")
}

// findScopeDir searches cgroupRoot for a directory whose base name is unitName.
func findScopeDir(cgroupRoot, unitName string) (*cgroupScope, bool) {
	if !isValidScopeUnit(unitName) {
		return nil, false
	}
	var found string
	if err := filepath.WalkDir(cgroupRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if d.IsDir() && d.Name() == unitName {
			found = p
			return fs.SkipAll
		}
		return nil
	}); err != nil {
		return nil, false
	}
	if found == "" {
		return nil, false
	}
	return &cgroupScope{unitName: unitName, dirPath: found}, true
}

func buildScopeUnitName(sessionID string) string {
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, sessionID)
	if clean == "" {
		clean = "default"
	}
	return scopeUnitPrefix + clean + scopeUnitSuffix
}

func attachLinuxContainment(
	ctx context.Context,
	p *Process,
	cgroupRoot string,
	procRoot string,
	unitName string,
	usedSystemd bool,
) {
	var verifiedScope *cgroupScope
	if usedSystemd {
		if scope, ok := verifyCgroupScope(ctx, procRoot, cgroupRoot, p.PID(), unitName); ok {
			verifiedScope = scope
		} else {
			p.uncontainedReason = ReasonScopeUnverified
		}
	} else {
		p.uncontainedReason = ReasonScopeUnavailable
	}

	scanner := newProcfsScanner(procRoot)
	token, tokErr := scanner.startToken(p.PID())
	if tokErr != nil {
		token = ""
	}

	p.identity = Identity{
		PID:        p.PID(),
		StartToken: token,
		ScopeUnit:  "",
	}
	if verifiedScope != nil {
		p.scope = verifiedScope
		p.identity.ScopeUnit = unitName
		return
	}

	tracker := newDescendantTracker(p.PID(), token, scanner, p.clock)
	tracker.start(ctx)
	p.tracker = tracker
}

func resolveContainmentPaths(opts *Options) (cgroupRoot, procRoot, systemdBin string) {
	cgroupRoot = defaultCgroupRoot
	if opts != nil && opts.CgroupRoot != "" {
		cgroupRoot = opts.CgroupRoot
	}
	procRoot = defaultProcRoot
	if opts != nil && opts.ProcRoot != "" {
		procRoot = opts.ProcRoot
	}
	systemdBin = defaultSystemdRun
	if opts != nil && opts.SystemdRunPath != "" {
		systemdBin = opts.SystemdRunPath
	}
	return cgroupRoot, procRoot, systemdBin
}

func prepareLinuxSpawnArgv(ctx context.Context, opts *Options) ([]string, string, bool) {
	cgroupRoot, _, systemdBin := resolveContainmentPaths(opts)
	probe := opts.Probe
	if probe == nil {
		probe = NewSystemdProbe()
	}
	if !probe.check(ctx, systemdBin, cgroupRoot) {
		return opts.Argv, "", false
	}
	resolved, err := exec.LookPath(systemdBin)
	if err != nil {
		return opts.Argv, "", false
	}
	sessionID := opts.SessionID
	if sessionID == "" {
		sessionID = uniqueScopeID(opts.Clock)
	}
	unitName := buildScopeUnitName(sessionID)
	args := buildSystemdRunArgs(resolved, unitName, opts.MemoryMax, opts.CPUWeight, opts.Argv)
	return args, unitName, true
}

func uniqueScopeID(clk clock.Clock) string {
	f, err := os.CreateTemp("", "fugue-")
	if err != nil {
		return strconv.FormatInt(clk.Now().UnixNano(), 36)
	}
	name := f.Name()
	closeQuietly(f)
	if rerr := os.Remove(name); rerr != nil {
		return strings.TrimPrefix(filepath.Base(name), "fugue-")
	}
	return strings.TrimPrefix(filepath.Base(name), "fugue-")
}

// buildSystemdRunArgs constructs the systemd-run command line for a scope unit.
func buildSystemdRunArgs(systemdBin, unitName, memoryMax string, cpuWeight int, argv []string) []string {
	args := []string{
		systemdBin,
		"--user",
		"--scope",
		"--unit=" + unitName,
		"--quiet",
	}
	if memoryMax != "" {
		args = append(args, "--property=MemoryMax="+memoryMax)
	}
	if cpuWeight > 0 {
		args = append(args, fmt.Sprintf("--property=CPUWeight=%d", cpuWeight))
	}
	args = append(args, "--")
	args = append(args, argv...)
	return args
}

// SystemdProbe caches the result of probing systemd-run and cgroup v2 per
// (systemdRunPath, cgroupRoot) pair.
type SystemdProbe struct {
	mu    sync.Mutex
	cache map[string]bool
}

// NewSystemdProbe returns an empty probe cache for Linux systemd user scope checks.
func NewSystemdProbe() *SystemdProbe {
	return &SystemdProbe{cache: make(map[string]bool)}
}

func (sp *SystemdProbe) check(ctx context.Context, systemdBin, cgroupRoot string) bool {
	key := systemdBin + "\x00" + cgroupRoot
	sp.mu.Lock()
	if res, ok := sp.cache[key]; ok {
		sp.mu.Unlock()
		return res
	}
	sp.mu.Unlock()

	ok := runSystemdProbe(ctx, systemdBin, cgroupRoot)

	sp.mu.Lock()
	sp.cache[key] = ok
	sp.mu.Unlock()
	return ok
}

func runSystemdProbe(ctx context.Context, systemdBin, cgroupRoot string) bool {
	if !isCgroupV2Mounted(cgroupRoot) {
		return false
	}
	resolved, err := exec.LookPath(systemdBin)
	if err != nil {
		return false
	}
	cmd := exec.CommandContext(ctx, "/usr/bin/systemd-run")
	cmd.Path = resolved
	cmd.Args = []string{resolved, "--user", "--scope", "--quiet", "--", "true"}
	cmd.Err = nil
	return cmd.Run() == nil
}

func isCgroupV2Mounted(cgroupRoot string) bool {
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.controllers")); err == nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(cgroupRoot, "cgroup.procs")); err == nil {
		return true
	}
	return false
}

// procfsScanner implements procTableSource over a Linux /proc directory.
type procfsScanner struct {
	procRoot string
}

func newProcfsScanner(procRoot string) *procfsScanner {
	if procRoot == "" {
		procRoot = defaultProcRoot
	}
	return &procfsScanner{procRoot: procRoot}
}

func (s *procfsScanner) readBootID() (string, error) {
	data, err := readRootFile(s.procRoot, "sys/kernel/random/boot_id")
	if err != nil {
		return "", fmt.Errorf("read boot_id: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func (s *procfsScanner) startToken(pid int) (string, error) {
	if pid <= 1 {
		return "", fmt.Errorf("%w: invalid pid %d", ErrIdentityMismatch, pid)
	}
	bootID, err := s.readBootID()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIdentityMismatch, err)
	}
	data, err := readRootFile(s.procRoot, strconv.Itoa(pid)+"/stat")
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrIdentityMismatch, err)
	}
	entry, err := parseProcStat(string(data), bootID)
	if err != nil {
		return "", err
	}
	return entry.startToken, nil
}

func (s *procfsScanner) scan() ([]procEntry, error) {
	bootID, err := s.readBootID()
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(s.procRoot)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", s.procRoot, err)
	}
	var out []procEntry
	for _, d := range dirEntries {
		if !d.IsDir() {
			continue
		}
		pid, convErr := strconv.Atoi(d.Name())
		if convErr != nil || pid <= 1 {
			continue
		}
		data, rerr := readRootFile(s.procRoot, d.Name()+"/stat")
		if rerr != nil {
			continue
		}
		entry, perr := parseProcStat(string(data), bootID)
		if perr == nil {
			out = append(out, entry)
		}
	}
	return out, nil
}
