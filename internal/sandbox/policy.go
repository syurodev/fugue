package sandbox

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// NetworkMode controls network isolation inside a sandboxed process.
type NetworkMode string

const (
	// NetworkNone disables network access inside the sandbox.
	NetworkNone NetworkMode = "none"
	// NetworkHost permits outbound IP networking on the host stack.
	NetworkHost NetworkMode = "host"
)

var (
	// ErrUnavailable reports that the sandbox backend binary is missing or unusable.
	ErrUnavailable = errcode.New(errcode.Unavailable, "sandbox unavailable")
	// ErrUnsupportedPlatform reports that no sandbox backend exists for the host OS.
	ErrUnsupportedPlatform = errcode.New(errcode.Unavailable, "sandbox unsupported on this platform")
	// ErrInvalidPolicy reports that a sandbox policy or command failed validation.
	ErrInvalidPolicy = errcode.New(errcode.InvalidArgument, "invalid sandbox policy")
)

func defaultSensitiveDenyPaths() []string {
	return []string{
		"~/.ssh",
		"~/.gnupg",
		"~/.aws",
		"~/.config/gcloud",
	}
}

// Policy defines the filesystem, network and environment boundaries for a
// sandboxed command execution (tool-system §6).
type Policy struct {
	// SessionTempDir is a private, short macOS session scratch directory.
	// Only this directory may host the session's Unix listeners.
	SessionTempDir string
	WorkDir        string
	ReadOnlyPaths  []string
	ReadWritePaths []string
	DenyReadPaths  []string
	// DenyWritePaths are never writable, even inside WorkDir, a ReadWritePaths
	// entry or a shared home. Each entry must be a clean absolute path that
	// exists and is not a symlink, and no writable grant may sit inside one.
	DenyWritePaths         []string
	AllowUnixSockets       []string
	ListenLoopbackPorts    []int
	AllowLoopbackListeners bool
	ReadPreferenceDomains  []string
	ReadAncestors          bool
	AllowKeychain          bool
	Env                    map[string]string
	Network                NetworkMode
	PrivateHome            *bool
	// DangerousAllowSensitive disables built-in sensitive path denials in tests.
	DangerousAllowSensitive bool
}

// Cmd describes an executable and its arguments to start inside a sandbox.
type Cmd struct {
	Path string
	Args []string
	Dir  string
}

// Validate checks that the policy satisfies cross-backend invariants.
func (p *Policy) Validate() error {
	if p == nil || p.WorkDir == "" || !filepath.IsAbs(p.WorkDir) || !validNetworkMode(p.Network) {
		return ErrInvalidPolicy
	}
	if !validSessionTempDir(p.SessionTempDir) {
		return ErrInvalidPolicy
	}
	if p.AllowLoopbackListeners && p.Network != NetworkHost {
		return ErrInvalidPolicy
	}
	if !validSocketAndPortGrants(p.AllowUnixSockets, p.ListenLoopbackPorts) {
		return ErrInvalidPolicy
	}
	if err := p.validateDenyWritePaths(); err != nil {
		return err
	}
	if p.SessionTempDir != "" {
		if err := validateBindPath(p.SessionTempDir, p.resolvedEffectiveDenyRead(hostHomeDir())); err != nil {
			return err
		}
	}
	return p.validateBindPaths()
}

func validSessionTempDir(dir string) bool {
	if dir == "" {
		return true
	}
	return filepath.IsAbs(dir) &&
		filepath.Clean(dir) == dir &&
		filepath.Dir(dir) == "/private/tmp" &&
		strings.HasPrefix(filepath.Base(dir), "dk-")
}

func validSocketAndPortGrants(sockets []string, ports []int) bool {
	for _, sock := range sockets {
		if !filepath.IsAbs(sock) {
			return false
		}
	}
	for _, port := range ports {
		if port < 1 || port > 65535 {
			return false
		}
	}
	return true
}

// ValidateCmd validates both the policy and the target command.
func (p *Policy) ValidateCmd(c Cmd) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if c.Path == "" {
		return ErrInvalidPolicy
	}
	return nil
}

// EffectiveDenyRead returns the deduplicated list of normalized paths denied
// for reading, combining built-in sensitive paths with Policy.DenyReadPaths.
func (p *Policy) EffectiveDenyRead() []string {
	return p.effectiveDenyRead(hostHomeDir())
}

func (p *Policy) effectiveDenyRead(hostHome string) []string {
	if p == nil {
		return nil
	}
	defaults := defaultSensitiveDenyPaths()
	seen := make(map[string]struct{})
	out := make([]string, 0, len(defaults)*2+len(p.DenyReadPaths))

	add := func(home, path string) {
		normalized := normalizeDenyPath(home, path)
		if normalized == "" {
			return
		}
		if _, ok := seen[normalized]; ok {
			return
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}

	policyHome := p.policyHomeDir(hostHome)
	if !p.DangerousAllowSensitive {
		for _, path := range defaults {
			add(hostHome, path)
		}
		if policyHome != "" && normalizePath(policyHome) != normalizePath(hostHome) {
			for _, path := range defaults {
				add(policyHome, path)
			}
		}
	}
	for _, path := range p.DenyReadPaths {
		add(policyHome, path)
	}
	return out
}

func (p *Policy) policyHomeDir(hostHome string) string {
	if p != nil && p.Env != nil && p.Env["HOME"] != "" {
		return p.Env["HOME"]
	}
	return hostHome
}

// EffectiveChdir returns c.Dir when absolute and contained in WorkDir, else WorkDir.
func (p *Policy) EffectiveChdir(c Cmd) string {
	if p == nil {
		return ""
	}
	if c.Dir != "" && filepath.IsAbs(c.Dir) && pathIsContainedIn(c.Dir, p.WorkDir) {
		return c.Dir
	}
	return p.WorkDir
}

// EffectivePrivateHome reports whether the backend should hide the host home.
func (p *Policy) EffectivePrivateHome() bool {
	return p == nil || p.PrivateHome == nil || *p.PrivateHome
}

func (p *Policy) sharedHome(hostHome string) string {
	if p.EffectivePrivateHome() {
		return ""
	}
	return p.policyHomeDir(hostHome)
}

func (p *Policy) withDefaults() Policy {
	if p == nil {
		return Policy{Network: NetworkNone}
	}
	cp := *p
	if cp.Network == "" {
		cp.Network = NetworkNone
	}
	return cp
}

func validNetworkMode(mode NetworkMode) bool {
	return mode == "" || mode == NetworkNone || mode == NetworkHost
}

func (p *Policy) validateBindPaths() error {
	hostHome := hostHomeDir()
	denyPaths := p.resolvedEffectiveDenyRead(hostHome)
	if err := validateBindPath(p.WorkDir, denyPaths); err != nil {
		return err
	}
	grants := make([]string, 0, len(p.ReadWritePaths)+len(p.ReadOnlyPaths))
	grants = append(grants, p.ReadWritePaths...)
	grants = append(grants, p.ReadOnlyPaths...)
	for _, path := range grants {
		if strings.TrimSpace(path) == "" || !pathExists(path) {
			continue
		}
		if err := validateBindPath(path, denyPaths); err != nil {
			return err
		}
	}
	builtin := *p
	builtin.DenyReadPaths = nil
	builtinDeny := builtin.resolvedEffectiveDenyRead(hostHome)
	for _, path := range p.AllowUnixSockets {
		if strings.TrimSpace(path) == "" || !pathExists(path) {
			continue
		}
		if err := validateBindPath(path, builtinDeny); err != nil {
			return err
		}
	}
	return nil
}

func (p *Policy) validateDenyWritePaths() error {
	grants := make([]string, 0, 1+len(p.ReadWritePaths))
	grants = append(grants, p.WorkDir)
	grants = append(grants, p.ReadWritePaths...)
	for _, path := range p.DenyWritePaths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return ErrInvalidPolicy
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidPolicy
		}
		if anyGrantContainedIn(grants, path) {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func anyGrantContainedIn(grants []string, denyWritePath string) bool {
	for _, grant := range grants {
		if grant != "" && pathIsContainedIn(normalizePath(grant), denyWritePath) {
			return true
		}
	}
	return false
}

func validateBindPath(path string, denyPaths []string) error {
	resolved, err := resolveExistingPath(path)
	if err != nil {
		return ErrInvalidPolicy
	}
	for _, denyPath := range denyPaths {
		if pathIsContainedIn(resolved, denyPath) {
			return ErrInvalidPolicy
		}
	}
	return nil
}

func (p *Policy) resolvedEffectiveDenyRead(hostHome string) []string {
	denyPaths := p.effectiveDenyRead(hostHome)
	out := make([]string, 0, len(denyPaths))
	seen := make(map[string]struct{})
	for _, path := range denyPaths {
		normalized := resolveDenyPath(path)
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

func normalizeDenyPath(home, path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	return normalizePath(expandHome(home, path))
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if !filepath.IsAbs(path) {
		if abs, err := filepath.Abs(path); err == nil {
			path = abs
		}
	}
	return filepath.Clean(path)
}

func resolveDenyPath(path string) string {
	path = normalizePath(path)
	if path == "" {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(resolved)
	}
	return path
}

func resolveExistingPath(path string) (string, error) {
	path = normalizePath(path)
	if path == "" {
		return "", ErrInvalidPolicy
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", ErrInvalidPolicy
	}
	return filepath.Clean(resolved), nil
}

func expandHome(home, path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	if home == "" {
		home = hostHomeDir()
		if home == "" {
			return path
		}
	}
	if path == "~" {
		return home
	}
	if strings.HasPrefix(path, "~/") {
		return filepath.Join(home, path[2:])
	}
	return path
}

func withHomeEnv(p *Policy, hostHome string, env map[string]string) map[string]string {
	out := make(map[string]string, len(env)+2)
	for key, value := range env {
		out[key] = value
	}
	if home := p.policyHomeDir(hostHome); home != "" {
		out["HOME"] = home
	}
	if out["TMPDIR"] == "" {
		out["TMPDIR"] = "/tmp"
	}
	return out
}

func hostHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func pathIsContainedIn(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}
