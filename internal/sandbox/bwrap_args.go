package sandbox

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const defaultResolvConfPath = "/etc/resolv.conf"

func bwrapSystemReadOnlyPaths() []string {
	return []string{"/usr", "/bin", "/lib", "/lib64", "/etc"}
}

type bwrapPathKind int

const (
	bwrapPathMissing bwrapPathKind = iota
	bwrapPathFile
	bwrapPathDir

	bwrapFlagBind   = "--bind"
	bwrapFlagROBind = "--ro-bind"
	bwrapFlagTmpfs  = "--tmpfs"
)

type bwrapEnv struct {
	hostHome       string
	resolvConfPath string
	stat           func(string) bwrapPathKind
	evalSymlinks   func(string) (string, error)
}

func defaultBwrapEnv() bwrapEnv {
	return bwrapEnv{
		hostHome:       hostHomeDir(),
		resolvConfPath: defaultResolvConfPath,
		stat: func(path string) bwrapPathKind {
			info, err := os.Stat(path)
			if err != nil {
				return bwrapPathMissing
			}
			if info.IsDir() {
				return bwrapPathDir
			}
			return bwrapPathFile
		},
		evalSymlinks: filepath.EvalSymlinks,
	}
}

func (e bwrapEnv) exists(path string) bool {
	return e.stat(path) != bwrapPathMissing
}

func buildBwrapArgs(p *Policy, c Cmd) []string {
	return buildBwrapArgsWith(p, c, defaultBwrapEnv())
}

func buildBwrapArgsWith(p *Policy, c Cmd, env bwrapEnv) []string {
	cp := p.withDefaults()

	args := []string{
		"--unshare-user",
		"--unshare-pid",
		"--unshare-ipc",
		"--die-with-parent",
	}
	if cp.Network == NetworkNone {
		args = append(args, "--unshare-net")
	}

	args = append(args, "--dev", "/dev", "--proc", "/proc")
	for _, path := range bwrapSystemReadOnlyPaths() {
		if env.exists(path) {
			args = append(args, "--ro-bind", path, path)
		}
	}
	appendResolvConf(&args, &cp, env)
	args = append(args, "--tmpfs", "/tmp")

	binds := collectBwrapBinds(&cp, env)
	appendNestedBinds(&args, binds)

	denyPaths := cp.effectiveDenyRead(env.hostHome)
	for _, path := range denyPaths {
		appendDenyReadMount(&args, path, env)
	}

	home := cp.policyHomeDir(env.hostHome)
	if cp.EffectivePrivateHome() && home != "" {
		appendPrivateHomeBinds(&args, &cp, home, denyPaths, env)
	}

	for _, sock := range cp.AllowUnixSockets {
		if env.exists(sock) {
			args = append(args, "--bind", sock, sock)
		}
	}

	args = appendEnvArgs(args, withHomeEnv(&cp, env.hostHome, cp.Env))
	args = append(args, "--chdir", cp.EffectiveChdir(c), "--", c.Path)
	args = append(args, c.Args...)
	return args
}

func collectBwrapBinds(p *Policy, env bwrapEnv) []bwrapBind {
	binds := []bwrapBind{{flag: bwrapFlagBind, path: p.WorkDir}}
	if shared := p.sharedHome(env.hostHome); shared != "" && env.exists(shared) {
		binds = append(binds, bwrapBind{flag: bwrapFlagBind, path: shared})
	}
	for _, path := range p.ReadWritePaths {
		if env.exists(path) {
			binds = append(binds, bwrapBind{flag: bwrapFlagBind, path: path})
		}
	}
	for _, path := range p.ReadOnlyPaths {
		if env.exists(path) {
			binds = append(binds, bwrapBind{flag: bwrapFlagROBind, path: path})
		}
	}
	for _, path := range p.DenyWritePaths {
		if env.exists(path) {
			binds = append(binds, bwrapBind{flag: bwrapFlagROBind, path: path})
		}
	}
	home := p.policyHomeDir(env.hostHome)
	privateHome := p.EffectivePrivateHome() && home != ""
	fixed := append([]string{"/tmp", "/dev", "/proc"}, bwrapSystemReadOnlyPaths()...)
	if target, err := env.evalSymlinks(env.resolvConfPath); err == nil {
		fixed = append(fixed, target)
	}
	return pinDenyWriteAncestors(binds, p.DenyWritePaths, fixed, env, func(dir string) bool {
		return privateHome && pathIsContainedIn(dir, home)
	})
}

func appendPrivateHomeBinds(args *[]string, p *Policy, home string, denyPaths []string, env bwrapEnv) {
	*args = append(*args, bwrapFlagTmpfs, home)
	var homeBinds []bwrapBind
	if env.exists(p.WorkDir) && pathIsContainedIn(p.WorkDir, home) && !pathIsDenied(p.WorkDir, denyPaths) {
		homeBinds = append(homeBinds, bwrapBind{flag: bwrapFlagBind, path: p.WorkDir})
	}
	homeBinds = appendHomeAllowlist(homeBinds, home, denyPaths, p.ReadWritePaths, bwrapFlagBind, env)
	homeBinds = appendHomeAllowlist(homeBinds, home, denyPaths, p.ReadOnlyPaths, bwrapFlagROBind, env)
	homeBinds = appendHomeAllowlist(homeBinds, home, denyPaths, p.DenyWritePaths, bwrapFlagROBind, env)
	homeBinds = pinDenyWriteAncestors(homeBinds, p.DenyWritePaths, nil, env, func(dir string) bool {
		return !pathIsUnder(dir, home)
	})
	appendNestedBinds(args, homeBinds)
}

func appendResolvConf(args *[]string, p *Policy, env bwrapEnv) {
	if p.Network == NetworkNone {
		return
	}
	target, err := env.evalSymlinks(env.resolvConfPath)
	if err != nil {
		return
	}
	for _, root := range bwrapSystemReadOnlyPaths() {
		if target == root || pathIsUnder(target, root) {
			return
		}
	}
	*args = append(*args, bwrapFlagROBind, target, target)
}

func appendDenyReadMount(args *[]string, path string, env bwrapEnv) {
	switch env.stat(path) {
	case bwrapPathMissing:
		return
	case bwrapPathDir:
		*args = append(*args, bwrapFlagTmpfs, path)
	case bwrapPathFile:
		*args = append(*args, bwrapFlagBind, "/dev/null", path)
	}
}

func appendHomeAllowlist(binds []bwrapBind, home string, denyPaths, paths []string, flag string, env bwrapEnv) []bwrapBind {
	for _, path := range paths {
		if !env.exists(path) || !pathIsUnder(path, home) || pathIsDenied(path, denyPaths) {
			continue
		}
		binds = append(binds, bwrapBind{flag: flag, path: path})
	}
	return binds
}

type bwrapBind struct {
	flag string
	path string
}

func pinDenyWriteAncestors(binds []bwrapBind, denyWrite, fixed []string, env bwrapEnv, skip func(dir string) bool) []bwrapBind {
	mounted := make(map[string]bool, len(binds))
	for _, b := range binds {
		mounted[b.path] = true
	}
	for _, path := range denyWrite {
		if !env.exists(path) {
			continue
		}
		for dir := filepath.Dir(path); dir != "/" && dir != "."; dir = filepath.Dir(dir) {
			if mounted[dir] || skip(dir) || holdsFixedMount(dir, fixed) {
				continue
			}
			binds = append(binds, ancestorPinBind(dir, binds))
			mounted[dir] = true
		}
	}
	return binds
}

func ancestorPinBind(dir string, binds []bwrapBind) bwrapBind {
	pin := bwrapBind{flag: bwrapFlagTmpfs, path: dir}
	depth := -1
	for _, b := range binds {
		if b.flag != bwrapFlagTmpfs && pathIsUnder(dir, b.path) && pathDepth(b.path) > depth {
			pin.flag, depth = b.flag, pathDepth(b.path)
		}
	}
	return pin
}

func holdsFixedMount(dir string, fixed []string) bool {
	for _, mount := range fixed {
		if pathIsContainedIn(mount, dir) {
			return true
		}
	}
	return false
}

func appendNestedBinds(args *[]string, binds []bwrapBind) {
	sort.SliceStable(binds, func(i, j int) bool {
		return pathDepth(binds[i].path) < pathDepth(binds[j].path)
	})
	for _, b := range binds {
		if b.flag == "--tmpfs" {
			*args = append(*args, b.flag, b.path)
			continue
		}
		*args = append(*args, b.flag, b.path, b.path)
	}
}

func pathDepth(path string) int {
	clean := filepath.Clean(path)
	if clean == string(filepath.Separator) {
		return 0
	}
	return strings.Count(clean, string(filepath.Separator))
}

func appendEnvArgs(args []string, env map[string]string) []string {
	args = append(args, "--clearenv")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		args = append(args, "--setenv", key, env[key])
	}
	return args
}

func pathIsUnder(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pathIsDenied(path string, denyPaths []string) bool {
	for _, denyPath := range denyPaths {
		if path == denyPath || pathIsUnder(path, denyPath) {
			return true
		}
	}
	return false
}
