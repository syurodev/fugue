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
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	defaultCgroupRoot = "/sys/fs/cgroup"
	defaultProcRoot   = "/proc"
	defaultSystemdRun = "systemd-run"
	scopeUnitPrefix   = "fugue-vendor-"
	scopeUnitSuffix   = ".scope"

	// scopeVerifyTimeout bounds the wait on the injected clock for systemd-run
	// to move the spawned process into its transient scope unit and for
	// <procRoot>/<pid>/cgroup to resolve to that directory under the current
	// user's systemd manager cgroup subtree.
	scopeVerifyTimeout = 2 * time.Second
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

func resolveUserManagerCgroupRel(procRoot string, uid int) (string, error) {
	data, err := readRootFile(procRoot, "self/cgroup")
	if err != nil {
		return "", fmt.Errorf("%w: read self/cgroup: %w", ErrIdentityMismatch, err)
	}
	rel, ok := extractUserManagerCgroupRel(string(data), uid)
	if !ok {
		return "", fmt.Errorf("%w: self/cgroup not in user@%d.service subtree", ErrIdentityMismatch, uid)
	}
	return rel, nil
}

// verifyCgroupScope polls <procRoot>/<pid>/cgroup on clk (every reapPollInterval
// up to scopeVerifyTimeout) and checks that the v2 hierarchy path resolves to a
// directory ending in unitName inside the current user's user@<uid>.service
// subtree under cgroupRoot.
func verifyCgroupScope(ctx context.Context, clk clock.Clock, procRoot, cgroupRoot string, pid int, unitName string) (*cgroupScope, bool) {
	if !isValidScopeUnit(unitName) || pid <= 0 {
		return nil, false
	}
	var verified *cgroupScope
	ok := waitPollCondition(ctx, clk, scopeVerifyTimeout, func() bool {
		scope, match := checkCgroupScopeOnce(procRoot, cgroupRoot, pid, unitName)
		if match {
			verified = scope
			return true
		}
		return false
	})
	return verified, ok
}

func checkCgroupScopeOnce(procRoot, cgroupRoot string, pid int, unitName string) (*cgroupScope, bool) {
	userMgrRel, err := resolveUserManagerCgroupRel(procRoot, os.Getuid())
	if err != nil {
		return nil, false
	}
	data, err := readRootFile(procRoot, strconv.Itoa(pid)+"/cgroup")
	if err != nil {
		return nil, false
	}
	rel, ok := parseProcCgroupRelPath(string(data), unitName)
	if !ok || !strings.HasPrefix(rel, userMgrRel+"/") {
		return nil, false
	}
	dir := filepath.Join(cgroupRoot, filepath.FromSlash(rel))
	if filepath.Base(dir) != unitName {
		return nil, false
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return nil, false
	}
	return &cgroupScope{
		unitName: unitName,
		dirPath:  dir,
	}, true
}

func isValidScopeUnit(unitName string) bool {
	return strings.HasPrefix(unitName, scopeUnitPrefix) &&
		strings.HasSuffix(unitName, scopeUnitSuffix) &&
		len(unitName) > len(scopeUnitPrefix)+len(scopeUnitSuffix) &&
		!strings.Contains(unitName, "/") &&
		!strings.Contains(unitName, "..")
}

// findScopeDir searches only the current user's user@<uid>.service subtree
// (resolved from <procRoot>/self/cgroup) for unitName, requiring exactly one
// match.
func findScopeDir(cgroupRoot, procRoot, unitName string) (*cgroupScope, error) {
	if !isValidScopeUnit(unitName) {
		return nil, fmt.Errorf("%w: invalid scope unit %q", ErrIdentityMismatch, unitName)
	}
	userMgrRel, err := resolveUserManagerCgroupRel(procRoot, os.Getuid())
	if err != nil {
		return nil, err
	}
	searchRoot := filepath.Join(cgroupRoot, filepath.FromSlash(userMgrRel))
	info, statErr := os.Stat(searchRoot)
	if statErr != nil || !info.IsDir() {
		return nil, fmt.Errorf("%w: user manager cgroup %s not found", ErrIdentityMismatch, userMgrRel)
	}
	matches := collectScopeMatches(searchRoot, unitName)
	if len(matches) != 1 {
		return nil, fmt.Errorf("%w: expected 1 scope directory for %q under %s, found %d", ErrIdentityMismatch, unitName, userMgrRel, len(matches))
	}
	return &cgroupScope{unitName: unitName, dirPath: matches[0]}, nil
}

func collectScopeMatches(searchRoot, unitName string) []string {
	var matches []string
	if err := filepath.WalkDir(searchRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return fs.SkipDir
		}
		if d.IsDir() && d.Name() == unitName {
			matches = append(matches, p)
			return fs.SkipDir
		}
		return nil
	}); err != nil {
		return nil
	}
	return matches
}

func buildScopeUnitName(sessionID string) (string, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return "", errcode.New(errcode.InvalidArgument, "process spawn requires SessionID when systemd scope containment is enabled")
	}
	clean := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, trimmed)
	if strings.Trim(clean, "-_") == "" {
		return "", errcode.New(errcode.InvalidArgument, "process spawn requires alphanumeric SessionID")
	}
	return scopeUnitPrefix + clean + scopeUnitSuffix, nil
}

func attachLinuxContainment(
	ctx context.Context,
	p *Process,
	cgroupRoot string,
	procRoot string,
	unitName string,
	usedSystemd bool,
) error {
	var verifiedScope *cgroupScope
	if usedSystemd {
		if scope, ok := verifyCgroupScope(ctx, p.clock, procRoot, cgroupRoot, p.PID(), unitName); ok {
			verifiedScope = scope
		} else {
			p.uncontainedReason = ReasonScopeUnverified
		}
	} else {
		p.uncontainedReason = ReasonScopeUnavailable
	}

	scanner := newProcfsScanner(procRoot)
	token, tokErr := readLinuxStartToken(ctx, p.clock, scanner, p.PID())
	if tokErr != nil {
		return tokErr
	}

	p.identity = Identity{
		PID:        p.PID(),
		StartToken: token,
		ScopeUnit:  "",
	}
	if verifiedScope != nil {
		p.scope = verifiedScope
		p.identity.ScopeUnit = unitName
		return nil
	}

	tracker := newDescendantTracker(p.PID(), token, scanner, p.clock)
	tracker.start(ctx)
	p.tracker = tracker
	return nil
}

func readLinuxStartToken(ctx context.Context, clk clock.Clock, scanner *procfsScanner, pid int) (string, error) {
	var token string
	var lastErr error
	ok := waitPollCondition(ctx, clk, scopeVerifyTimeout, func() bool {
		tok, err := scanner.startToken(pid)
		if err == nil && tok != "" {
			token = tok
			return true
		}
		lastErr = err
		return false
	})
	if !ok {
		return "", fmt.Errorf("%w: read process start token: %w", ErrStartFailed, lastErr)
	}
	return token, nil
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

func prepareLinuxSpawnArgv(ctx context.Context, opts *Options) ([]string, string, bool, error) {
	if opts.Probe == nil {
		return nil, "", false, errcode.New(errcode.InvalidArgument, "process spawn requires Options.Probe")
	}
	cgroupRoot, _, systemdBin := resolveContainmentPaths(opts)
	if !opts.Probe.check(ctx, systemdBin, cgroupRoot) {
		return opts.Argv, "", false, nil
	}
	unitName, err := buildScopeUnitName(opts.SessionID)
	if err != nil {
		return nil, "", false, err
	}
	resolved, err := exec.LookPath(systemdBin)
	if err != nil {
		return nil, "", false, classifySpawnError(systemdBin, err)
	}
	args := buildSystemdRunArgs(resolved, unitName, opts.MemoryMax, opts.CPUWeight, opts.Argv)
	return args, unitName, true, nil
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
	return execSystemdProbe(ctx, resolved)
}

func execSystemdProbe(ctx context.Context, resolved string) bool {
	return exec.CommandContext(ctx, resolved, "--user", "--scope", "--quiet", "--", "true").Run() == nil
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
