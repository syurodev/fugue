package sandbox

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func seatbeltSystemReadOnlyPaths() []string {
	return []string{
		"/usr",
		"/bin",
		"/lib",
		"/System/Library",
		"/Library/Frameworks",
		"/private/var/db/dyld",
		"/etc",
		"/private/etc",
	}
}

func seatbeltNetworkMachServices() []string {
	return []string{
		"com.apple.SystemConfiguration.configd",
		"com.apple.mDNSResponder",
		"com.apple.dns.service",
		"com.apple.dns.registration",
		"com.apple.system.logger",
		"com.apple.system.notification_center",
		"com.apple.SecurityServer",
		"com.apple.trustd.agent",
	}
}

func buildSeatbeltProfile(p *Policy) string {
	return buildSeatbeltProfileForHost(p, hostHomeDir(), true)
}

func buildSeatbeltProfileWithUnixSockets(p *Policy, unixSockets bool) string {
	return buildSeatbeltProfileForHost(p, hostHomeDir(), unixSockets)
}

func buildSeatbeltProfileForHost(p *Policy, hostHome string, unixSockets bool) string {
	cp := p.withDefaults()
	var b strings.Builder
	writeSeatbeltSystemAndFilesystem(&b, &cp, hostHome)
	writeSeatbeltProcessAndPreferences(&b, &cp)
	writeSeatbeltNetworkAndIPC(&b, &cp, unixSockets)
	writeSeatbeltDenies(&b, &cp, hostHome)
	return b.String()
}

func writeSeatbeltSystemAndFilesystem(b *strings.Builder, p *Policy, hostHome string) {
	b.WriteString("(version 1)\n(deny default)\n(allow file-read*\n")
	for _, path := range seatbeltSystemReadOnlyPaths() {
		writeSBPLSubpath(b, path)
	}
	b.WriteString("  (literal \"/\")\n)\n")
	b.WriteString("(allow file-read* file-write* (require-all (literal \"/dev/null\") (vnode-type CHARACTER-DEVICE)))\n")

	b.WriteString("(allow file-read* file-write*\n")
	writeSBPLPath(b, p.WorkDir)
	for _, path := range p.ReadWritePaths {
		writeSBPLPath(b, path)
	}
	if p.SessionTempDir == "" {
		writeSBPLSubpath(b, "/tmp")
		writeSBPLSubpath(b, "/private/tmp")
	}
	if home := p.sharedHome(hostHome); home != "" {
		writeSBPLPath(b, home)
	}
	b.WriteString(")\n")

	if len(p.ReadOnlyPaths) > 0 {
		b.WriteString("(allow file-read*\n")
		for _, path := range p.ReadOnlyPaths {
			writeSBPLPath(b, path)
		}
		b.WriteString(")\n")
	}
	writeSeatbeltKeychainAndAncestors(b, p, hostHome)
}

func writeSeatbeltKeychainAndAncestors(b *strings.Builder, p *Policy, hostHome string) {
	if p.AllowKeychain {
		b.WriteString("(allow mach-lookup (global-name \"com.apple.SecurityServer\"))\n")
		if home := p.policyHomeDir(hostHome); home != "" {
			b.WriteString("(allow file-read*\n")
			writeSBPLPath(b, filepath.Join(home, "Library", "Keychains"))
			b.WriteString(")\n")
		}
	}
	for _, dir := range pathAncestors(p.grantedPaths(hostHome)) {
		b.WriteString("(allow file-read-metadata (literal \"" + escapeSBPL(dir) + "\"))\n")
		if p.ReadAncestors {
			b.WriteString("(allow file-read-data (literal \"" + escapeSBPL(dir) + "\"))\n")
		}
	}
	b.WriteString("(allow file-read-metadata (literal \"/private\"))\n")
	if p.SessionTempDir != "" {
		b.WriteString("(allow file-read-metadata (literal \"/private/tmp\"))\n")
	}
}

func writeSeatbeltProcessAndPreferences(b *strings.Builder, p *Policy) {
	b.WriteString("(allow process-exec)\n(allow process-fork)\n")
	b.WriteString(`(allow signal (target same-sandbox))
(allow process-info* (target same-sandbox))
(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo"))
(allow pseudo-tty)
(allow file-read* file-write* file-ioctl (literal "/dev/ptmx"))
(allow file-read* file-write* file-ioctl (require-all (regex #"^/dev/ttys[0-9]+$") (extension "com.apple.sandbox.pty")))
`)
	b.WriteString("(allow sysctl-read)\n")

	if len(p.ReadPreferenceDomains) > 0 {
		b.WriteString("(allow mach-lookup (global-name \"com.apple.cfprefsd.daemon\") (global-name \"com.apple.cfprefsd.agent\") (local-name \"com.apple.cfprefsd.agent\"))\n")
		b.WriteString("(allow ipc-posix-shm-read* (ipc-posix-name-prefix \"apple.cfprefs.\"))\n")
		b.WriteString("(allow user-preference-read\n")
		for _, domain := range p.ReadPreferenceDomains {
			b.WriteString("  (preference-domain \"" + escapeSBPL(domain) + "\")\n")
		}
		b.WriteString(")\n")
	}
}

func writeSeatbeltNetworkAndIPC(b *strings.Builder, p *Policy, unixSockets bool) {
	switch p.Network {
	case NetworkNone:
		b.WriteString("(deny network-outbound)\n(deny network-inbound)\n")
	case NetworkHost:
		b.WriteString("(allow network-outbound (remote ip \"*:*\"))\n")
		b.WriteString("(allow network-outbound (literal \"/private/var/run/mDNSResponder\"))\n")
		b.WriteString("(allow file-read-metadata (literal \"/private/var\") (literal \"/private/var/run\") (literal \"/var\") (literal \"/var/run\"))\n")
		b.WriteString("(allow file-read* (literal \"/private/var/run/resolv.conf\") (literal \"/var/run/resolv.conf\"))\n")
		b.WriteString("(allow file-read* (subpath \"/private/var/db/timezone\") (subpath \"/var/db/timezone\"))\n")
		for _, service := range seatbeltNetworkMachServices() {
			b.WriteString("(allow mach-lookup (global-name \"" + service + "\"))\n")
		}
		b.WriteString("(deny file-read* (subpath \"/var/root\"))\n")
		b.WriteString("(deny file-read* (subpath \"/private/var/root\"))\n")
		b.WriteString("(deny file-read* (subpath \"/var/db/Keychains\"))\n")
		b.WriteString("(deny file-read* (subpath \"/private/var/db/Keychains\"))\n")
	}

	for _, sock := range p.AllowUnixSockets {
		b.WriteString("(allow network-outbound (literal \"" + escapeSBPL(resolveDenyPath(sock)) + "\"))\n")
	}
	if p.AllowLoopbackListeners {
		b.WriteString("(allow network-bind (local ip \"localhost:*\"))\n")
		b.WriteString("(allow network-inbound (local ip \"localhost:*\"))\n")
	}
	for _, port := range p.ListenLoopbackPorts {
		addr := "localhost:" + strconv.Itoa(port)
		b.WriteString("(allow network-bind (local ip \"" + addr + "\"))\n")
		b.WriteString("(allow network-inbound (local ip \"" + addr + "\"))\n")
	}

	b.WriteString("(deny file-read* file-write* (regex #\"^/private/tmp/dk-\"))\n")
	if p.SessionTempDir != "" {
		b.WriteString("(allow file-read* file-write* (subpath \"" + escapeSBPL(p.SessionTempDir) + "\"))\n")
		if unixSockets {
			writeSessionUnixSockets(b, p.SessionTempDir)
		}
	}
}

func writeSeatbeltDenies(b *strings.Builder, p *Policy, hostHome string) {
	if len(p.DenyWritePaths) > 0 {
		b.WriteString("(deny file-write*\n")
		for _, path := range p.DenyWritePaths {
			writeSBPLPath(b, path)
		}
		b.WriteString(")\n")
	}

	denyPaths := p.effectiveDenyRead(hostHome)
	if !p.AllowKeychain {
		denyPaths = append(denyPaths, p.keychainDirs(hostHome)...)
	}
	if len(denyPaths) > 0 {
		b.WriteString("(deny file-read* file-write*\n")
		for _, path := range denyPaths {
			writeSBPLPath(b, path)
		}
		b.WriteString(")\n")
	}

	protected := make([]string, 0, (len(denyPaths)+len(p.DenyWritePaths))*2)
	for _, path := range append(append([]string(nil), denyPaths...), p.DenyWritePaths...) {
		protected = append(protected, normalizePath(path), resolveThroughParents(path))
	}
	for _, dir := range pathAncestors(protected) {
		b.WriteString("(deny file-write-unlink (require-all (vnode-type DIRECTORY) (literal \"" + escapeSBPL(dir) + "\")))\n")
	}
}

func (p *Policy) keychainDirs(hostHome string) []string {
	var out []string
	for _, home := range []string{p.policyHomeDir(hostHome), hostHome} {
		if home == "" {
			continue
		}
		dir := filepath.Join(home, "Library", "Keychains")
		if !slices.Contains(out, dir) {
			out = append(out, dir)
		}
	}
	return out
}

func (p *Policy) grantedPaths(hostHome string) []string {
	out := make([]string, 0, len(p.ReadWritePaths)+len(p.ReadOnlyPaths)+len(p.AllowUnixSockets)+2)
	add := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		out = append(out, normalizePath(path))
		if resolved := resolveDenyPath(path); resolved != "" {
			out = append(out, resolved)
		}
	}
	add(p.WorkDir)
	add(p.sharedHome(hostHome))
	for _, path := range p.ReadWritePaths {
		add(path)
	}
	for _, path := range p.ReadOnlyPaths {
		add(path)
	}
	for _, path := range p.AllowUnixSockets {
		add(path)
	}
	return out
}

func resolveThroughParents(path string) string {
	path = normalizePath(path)
	if path == "" {
		return ""
	}
	rest := ""
	for dir := path; ; dir = filepath.Dir(dir) {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, rest)
		}
		if dir == filepath.Dir(dir) {
			return path
		}
		rest = filepath.Join(filepath.Base(dir), rest)
	}
}

func pathAncestors(paths []string) []string {
	seen := map[string]struct{}{}
	for _, path := range paths {
		for dir := filepath.Dir(path); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			seen[dir] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for dir := range seen {
		out = append(out, dir)
	}
	sort.Strings(out)
	return out
}

func writeSBPLPath(b *strings.Builder, path string) {
	writeSBPLSubpath(b, path)
	if resolved := resolveDenyPath(path); resolved != "" && resolved != normalizePath(path) {
		writeSBPLSubpath(b, resolved)
	}
}

func writeSBPLSubpath(b *strings.Builder, path string) {
	b.WriteString("  (subpath \"")
	b.WriteString(escapeSBPL(path))
	b.WriteString("\")\n")
}

func escapeSBPL(path string) string {
	path = strings.ReplaceAll(path, "\\", "\\\\")
	return strings.ReplaceAll(path, "\"", "\\\"")
}

func writeSessionUnixSockets(b *strings.Builder, dir string) {
	filter := `(subpath "` + escapeSBPL(dir) + `")`
	b.WriteString("(allow network-bind network-inbound (local unix-socket " + filter + "))\n")
	b.WriteString("(allow network-outbound (remote unix-socket " + filter + "))\n")
}

type profileValidationRunner interface {
	Run(ctx context.Context, p *Policy, profile string) error
}

type unixProfileGate struct {
	once    sync.Once
	checked atomic.Bool
	enabled atomic.Bool
	runner  profileValidationRunner
}

func (g *unixProfileGate) profile(ctx context.Context, p *Policy) string {
	if p.SessionTempDir == "" {
		return buildSeatbeltProfileWithUnixSockets(p, false)
	}
	g.once.Do(func() {
		var err error
		for range 2 {
			probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err = g.runner.Run(probeCtx, p, buildSeatbeltProfileWithUnixSockets(p, true))
			timedOut := errors.Is(err, context.DeadlineExceeded) || errors.Is(probeCtx.Err(), context.DeadlineExceeded)
			cancel()
			if !timedOut {
				break
			}
		}
		g.enabled.Store(err == nil)
		g.checked.Store(true)
	})
	return buildSeatbeltProfileWithUnixSockets(p, g.enabled.Load())
}

func (g *unixProfileGate) status() string {
	if !g.checked.Load() {
		return "Session Unix sockets have not been checked yet."
	}
	if !g.enabled.Load() {
		return "Session Unix sockets are unavailable on this system. Sessions can still run."
	}
	return "Session Unix sockets are available."
}

func seatbeltProcessEnv(p *Policy, hostHome string) []string {
	if p == nil || (p.Env == nil && !p.EffectivePrivateHome()) {
		return nil
	}
	env := withHomeEnv(p, hostHome, p.Env)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}
