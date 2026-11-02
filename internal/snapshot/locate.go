package snapshot

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// MinGitVersion is the oldest git release the snapshot engine accepts.
const MinGitVersion = "2.26"

const locateProbeTimeout = 5 * time.Second

// Availability describes whether a usable git binary is on the machine.
type Availability struct {
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// OK reports whether a usable git was found.
func (a Availability) OK() bool {
	return a.Path != "" && a.Reason == ""
}

// LocateOptions customises how LocateWith searches for git. Zero fields fall
// back to the host OS defaults.
type LocateOptions struct {
	GOOS           string
	ShimPath       string
	ToolchainPaths []string
	SearchDirs     []string
	LookPath       func(string) (string, error)
}

func (o *LocateOptions) withDefaults() LocateOptions {
	var cfg LocateOptions
	if o != nil {
		cfg = *o
	}
	if cfg.GOOS == "" {
		cfg.GOOS = runtime.GOOS
	}
	if cfg.ShimPath == "" {
		cfg.ShimPath = "/usr/bin/git"
	}
	if cfg.ToolchainPaths == nil {
		cfg.ToolchainPaths = []string{
			"/Library/Developer/CommandLineTools/usr/bin/git",
			"/Applications/Xcode.app/Contents/Developer/usr/bin/git",
		}
	}
	if cfg.SearchDirs == nil {
		cfg.SearchDirs = []string{
			"/opt/homebrew/bin",
			"/usr/local/bin",
			"/opt/local/bin",
			"/usr/bin",
			"/bin",
		}
	}
	if cfg.LookPath == nil {
		cfg.LookPath = exec.LookPath
	}
	return cfg
}

// Locate finds a usable git binary on the machine using the default host search
// rules.
func Locate(ctx context.Context) Availability {
	return LocateWith(ctx, nil)
}

// LocateWith finds a usable git binary using opts.
func LocateWith(ctx context.Context, opts *LocateOptions) Availability {
	cfg := opts.withDefaults()
	var lastProblem string
	for _, candidate := range candidatePaths(&cfg) {
		if cfg.GOOS == goosDarwin && samePath(candidate, cfg.ShimPath) && !hasAppleCommandLineTools(cfg.ToolchainPaths) {
			lastProblem = "macOS Command Line Tools are not installed (run `xcode-select --install`)"
			continue
		}
		version, err := probeGit(ctx, candidate)
		if err != nil {
			continue
		}
		if compareGitVersions(version, MinGitVersion) < 0 {
			lastProblem = fmt.Sprintf("git %s at %s is older than the required %s", version, candidate, MinGitVersion)
			continue
		}
		return Availability{Path: candidate, Version: version}
	}
	if lastProblem != "" {
		return Availability{Reason: lastProblem}
	}
	return Availability{Reason: "git was not found on PATH"}
}

func candidatePaths(cfg *LocateOptions) []string {
	seen := make(map[string]bool)
	var out []string
	add := func(path string) {
		if path == "" {
			return
		}
		clean := filepath.Clean(path)
		if !seen[clean] {
			seen[clean] = true
			out = append(out, clean)
		}
	}
	if found, err := cfg.LookPath("git"); err == nil {
		add(found)
	}
	name := "git"
	if cfg.GOOS == "windows" {
		name = "git.exe"
	}
	for _, dir := range cfg.SearchDirs {
		if dir != "" {
			add(filepath.Join(dir, name))
		}
	}
	if cfg.GOOS == goosDarwin {
		add(cfg.ShimPath)
	}
	return out
}

func hasAppleCommandLineTools(toolchainPaths []string) bool {
	for _, path := range toolchainPaths {
		info, err := os.Stat(path)
		if err == nil && !info.IsDir() {
			return true
		}
	}
	return false
}

func samePath(a, b string) bool {
	if filepath.Clean(a) == filepath.Clean(b) {
		return true
	}
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

func probeGit(ctx context.Context, path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", fmt.Errorf("%w: stat %s: not a file", ErrGitProbe, path)
	}
	probeCtx, cancel := context.WithTimeout(ctx, locateProbeTimeout)
	defer cancel()

	var stdout bytes.Buffer
	var runErr error
	for range 10 {
		stdout.Reset()
		cmd := exec.CommandContext(probeCtx, "git")
		cmd.Path = path
		cmd.Args = []string{path, "--version"}
		cmd.Stdout = &stdout
		cmd.Stderr = &stdout
		runErr = cmd.Run()
		if runErr == nil || !strings.Contains(runErr.Error(), "text file busy") {
			break
		}
		runtime.Gosched()
	}
	if runErr != nil {
		return "", fmt.Errorf("run %s --version: %w", path, runErr)
	}
	ver := extractGitVersion(stdout.String())
	if ver == "" {
		return "", fmt.Errorf("%w: unrecognised output %q", ErrGitProbe, strings.TrimSpace(stdout.String()))
	}
	return ver, nil
}

// extractGitVersion parses the numeric version out of `git version X.Y.Z...`.
func extractGitVersion(banner string) string {
	const prefix = "git version "
	idx := strings.Index(banner, prefix)
	if idx < 0 {
		return ""
	}
	rest := banner[idx+len(prefix):]
	var end int
	for end < len(rest) {
		ch := rest[end]
		if (ch >= '0' && ch <= '9') || ch == '.' {
			end++
			continue
		}
		break
	}
	ver := strings.Trim(rest[:end], ".")
	if !strings.Contains(ver, ".") {
		return ""
	}
	return ver
}

// compareGitVersions compares dotted numeric version prefixes (such as
// "2.39.5 (Apple Git-154)" vs "2.26").
func compareGitVersions(a, b string) int {
	pa := parseVersionNumbers(a)
	pb := parseVersionNumbers(b)
	n := max(len(pa), len(pb))
	for i := range n {
		var va, vb int
		if i < len(pa) {
			va = pa[i]
		}
		if i < len(pb) {
			vb = pb[i]
		}
		if va < vb {
			return -1
		}
		if va > vb {
			return 1
		}
	}
	return 0
}

func parseVersionNumbers(raw string) []int {
	if idx := strings.IndexByte(raw, ' '); idx >= 0 {
		raw = raw[:idx]
	}
	parts := strings.Split(raw, ".")
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		var digits strings.Builder
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				break
			}
			digits.WriteRune(ch)
		}
		if digits.Len() == 0 {
			break
		}
		n, err := strconv.Atoi(digits.String())
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
