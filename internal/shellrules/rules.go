package shellrules

import (
	"path/filepath"
	"slices"
	"strings"
)

// Inspect evaluates static shell rules for a command line according to interaction §3.3.
func Inspect(command string, opts *Options) Verdict {
	if opts == nil {
		opts = &Options{}
	}
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Verdict{Ask: false, Risk: RiskRead}
	}

	cmds, ok := Split(command)
	if !ok {
		return Verdict{Ask: true, Risk: RiskExternal, Unparsable: true, MatchedRule: RuleUnparsable}
	}
	if len(cmds) == 0 {
		return Verdict{Ask: false, Risk: RiskRead}
	}

	if matchesDownloadPipe(cmds) {
		return Verdict{Ask: true, Risk: RiskExternal, MatchedRule: RuleDownloadPipe, Commands: cmds}
	}

	for i := range cmds {
		if matched, risk, rule := matchDefaultRule(&cmds[i], opts.WorkspaceRoots); matched {
			return Verdict{Ask: true, Risk: risk, MatchedRule: rule, Commands: cmds}
		}
	}

	if requiresTagConfirmation(opts) {
		return Verdict{Ask: true, Risk: RiskExternal, MatchedRule: RuleEnvTag, Commands: cmds}
	}

	for i := range cmds {
		if matchesAnyPrefix(&cmds[i], opts.ShellAsk) {
			return Verdict{Ask: true, Risk: RiskWrite, MatchedRule: RuleWorkspaceAsk, Commands: cmds}
		}
	}

	return Verdict{Ask: false, Risk: RiskWrite, Commands: cmds}
}

// IsAllowed reports whether command is allowed by workspace shell_allow policy without
// violating hard rule I-03 or matching any default risk rule (interaction §3.3).
// Every simple command in a compound command must be covered by shell_allow.
func IsAllowed(command string, opts *Options) bool {
	if opts == nil {
		opts = &Options{}
	}
	if strings.TrimSpace(command) == "" || requiresTagConfirmation(opts) {
		return false
	}

	cmds, ok := Split(command)
	if !ok || len(cmds) == 0 || matchesDownloadPipe(cmds) {
		return false
	}

	for i := range cmds {
		cmd := &cmds[i]
		if matched, _, _ := matchDefaultRule(cmd, opts.WorkspaceRoots); matched || matchesAnyPrefix(cmd, opts.ShellAsk) {
			return false
		}
	}

	return len(opts.ShellAllow) > 0 && allCommandsMatchAllow(cmds, opts.ShellAllow)
}

func requiresTagConfirmation(opts *Options) bool {
	return opts.Target != "" && (opts.TagPolicy == nil || opts.TagPolicy.RequiresConfirmation(opts.EnvTag))
}

func matchDefaultRule(cmd *Command, roots []string) (bool, Risk, string) {
	if matchesRemotePush(cmd) {
		return true, RiskExternal, RuleRemotePush
	}
	if matchesDeployInfra(cmd) {
		return true, RiskExternal, RuleDeployInfra
	}
	if matchesDestructiveOutside(cmd, roots) || redirectsOutside(cmd, roots) {
		return true, RiskDestructive, RuleDestructiveOutside
	}
	return false, "", ""
}

func allCommandsMatchAllow(cmds []Command, allowPrefixes []string) bool {
	for i := range cmds {
		if !matchesAnyPrefix(&cmds[i], allowPrefixes) {
			return false
		}
	}
	return true
}

func matchesAnyPrefix(cmd *Command, prefixes []string) bool {
	raw := strings.TrimSpace(cmd.Raw)
	norm := strings.TrimSpace(cmd.Name + " " + strings.Join(cmd.Args, " "))
	for _, p := range prefixes {
		trimmed := strings.TrimSpace(p)
		if trimmed != "" && (raw == trimmed || strings.HasPrefix(raw, trimmed+" ") ||
			norm == trimmed || strings.HasPrefix(norm, trimmed+" ")) {
			return true
		}
	}
	return false
}

func matchesDownloadPipe(cmds []Command) bool {
	for i := range len(cmds) - 1 {
		if cmds[i].PipedTo && (cmds[i].Base == "curl" || cmds[i].Base == "wget") &&
			isShellInterpreter(cmds[i+1].Base) {
			return true
		}
	}
	return false
}

func isShellInterpreter(b string) bool {
	return b == "sh" || b == "bash" || b == "zsh" || b == "dash"
}

func matchesRemotePush(cmd *Command) bool {
	nonFlags := nonFlagArgs(cmd.Args)
	switch cmd.Base {
	case "git":
		gitArgs := nonFlagArgs(skipGitGlobalFlags(cmd.Args))
		return len(gitArgs) > 0 && gitArgs[0] == "push"
	case "gh":
		return len(nonFlags) >= 2 && nonFlags[0] == "pr" && nonFlags[1] == "merge"
	case "npm", "pnpm", "yarn":
		return len(nonFlags) > 0 && nonFlags[0] == "publish"
	case "docker", "podman":
		return len(nonFlags) > 0 && nonFlags[0] == "push"
	default:
		return false
	}
}

func matchesDeployInfra(cmd *Command) bool {
	nonFlags := nonFlagArgs(cmd.Args)
	if len(nonFlags) == 0 {
		return false
	}
	sub := nonFlags[0]
	switch cmd.Base {
	case "kubectl":
		switch sub {
		case "apply", "delete", "create", "patch", "replace":
			return true
		}
	case "terraform":
		switch sub {
		case "apply", "destroy":
			return true
		}
	case "helm":
		switch sub {
		case "upgrade", "install", "uninstall", "delete":
			return true
		}
	}
	return false
}

func matchesDestructiveOutside(cmd *Command, roots []string) bool {
	switch cmd.Base {
	case "rm":
		isRec := isRecursiveRm(cmd.Args)
		for _, a := range nonFlagArgs(cmd.Args) {
			if (isRec && strings.Contains(a, "$")) || isOutsideWorkspace(a, roots) {
				return true
			}
		}
	case "mv", "cp":
		dest := destinationArg(cmd.Args)
		if dest != "" && isOutsideWorkspace(dest, roots) {
			return true
		}
	}
	return false
}

// redirectsOutside reports output redirections that write outside the workspace.
func redirectsOutside(cmd *Command, roots []string) bool {
	return slices.ContainsFunc(cmd.Redirects, func(t string) bool {
		return t != "/dev/null" && t != "/dev/stdout" && t != "/dev/stderr" && isOutsideWorkspace(t, roots)
	})
}

func isRecursiveRm(args []string) bool {
	for _, a := range args {
		if a == "-r" || a == "-R" || a == "--recursive" ||
			(strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && (strings.ContainsRune(a, 'r') || strings.ContainsRune(a, 'R'))) {
			return true
		}
	}
	return false
}

func destinationArg(args []string) string {
	for i, a := range args {
		if a == "-t" && i+1 < len(args) {
			return args[i+1]
		}
		if strings.HasPrefix(a, "--target-directory=") {
			return strings.TrimPrefix(a, "--target-directory=")
		}
	}
	nonFlags := nonFlagArgs(args)
	if len(nonFlags) >= 2 {
		return nonFlags[len(nonFlags)-1]
	}
	return ""
}

func isOutsideWorkspace(p string, roots []string) bool {
	trimmed := strings.TrimSpace(p)
	if trimmed == "" {
		return false
	}
	if isHomeOrParent(trimmed) {
		return true
	}
	clean := filepath.Clean(trimmed)
	if !filepath.IsAbs(clean) && !strings.HasPrefix(clean, "/") && !strings.HasPrefix(clean, "\\") {
		return false
	}
	return !isWithinAnyRoot(clean, roots)
}

func isHomeOrParent(p string) bool {
	if strings.HasPrefix(p, "~") { // ~, ~/x, ~user/x all expand to a home directory
		return true
	}
	clean := filepath.Clean(p)
	return clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator))
}

func isWithinAnyRoot(clean string, roots []string) bool {
	if len(roots) == 0 {
		return false
	}
	for _, r := range roots {
		rClean := filepath.Clean(r)
		if rClean != "" && (clean == rClean || strings.HasPrefix(clean, rClean+string(filepath.Separator))) {
			return true
		}
	}
	return false
}

func skipGitGlobalFlags(args []string) []string {
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			return args[i+1:]
		}
		if a == "-C" || a == "-c" || a == "--git-dir" || a == "--work-tree" || a == "--namespace" {
			i += 2
			continue
		}
		if strings.HasPrefix(a, "-") {
			i++
			continue
		}
		break
	}
	if i < len(args) {
		return args[i:]
	}
	return nil
}

func nonFlagArgs(args []string) []string {
	var res []string
	afterDashDash := false
	for _, a := range args {
		if a == "--" {
			afterDashDash = true
			continue
		}
		if afterDashDash || !strings.HasPrefix(a, "-") {
			res = append(res, a)
		}
	}
	return res
}
