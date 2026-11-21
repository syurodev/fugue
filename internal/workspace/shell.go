package workspace

import (
	"strings"

	"github.com/syurodev/fugue/internal/shellrules"
)

// ShellEval summarizes static shell rule evaluation for a Fugue-run command (VR-13).
type ShellEval struct {
	Ask         bool
	Allowed     bool
	Risk        string
	Unparsable  bool
	MatchedRule string
	Target      string
	EnvTag      string
}

// EvaluateShell inspects command against workspace roots, shell_ask, shell_allow,
// default risk rules, and env_tags so callers can feed interaction.Decide (VR-13).
func EvaluateShell(cfg *EffectiveConfig, tags TagPolicy, command string) ShellEval {
	if cfg == nil {
		cfg = &EffectiveConfig{}
	}
	target, envTag := resolveCommandEnvTarget(cfg.Policy.EnvTags, command)
	opts := shellOptionsFor(cfg, tags, target, envTag)
	v := shellrules.Inspect(command, opts)
	allowed := shellrules.IsAllowed(command, opts)
	return ShellEval{
		Ask:         v.Ask,
		Allowed:     allowed,
		Risk:        string(v.Risk),
		Unparsable:  v.Unparsable,
		MatchedRule: v.MatchedRule,
		Target:      target,
		EnvTag:      envTag,
	}
}

func resolveCommandEnvTarget(rules []EnvTagRule, command string) (string, string) {
	if len(rules) == 0 {
		return "", ""
	}
	cmds, ok := shellrules.Split(command)
	if !ok {
		return "", ""
	}
	for i := range cmds {
		for _, tok := range commandTargetCandidates(&cmds[i]) {
			if tag := ResolveEnvTag(rules, tok); tag != "" {
				return tok, tag
			}
		}
	}
	return "", ""
}

func commandTargetCandidates(cmd *shellrules.Command) []string {
	var out []string
	out = append(out, cmd.Name)
	out = append(out, cmd.Args...)
	out = append(out, cmd.Redirects...)
	for _, env := range cmd.Env {
		if _, val, ok := strings.Cut(env, "="); ok {
			out = append(out, val)
		}
	}
	return out
}

func shellOptionsFor(cfg *EffectiveConfig, tags TagPolicy, target, envTag string) *shellrules.Options {
	roots := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		roots = append(roots, r.Path)
	}
	return &shellrules.Options{
		WorkspaceRoots: roots,
		ShellAsk:       cfg.Policy.ShellAsk,
		ShellAllow:     cfg.Policy.ShellAllow,
		Target:         target,
		EnvTag:         envTag,
		TagPolicy:      tags,
	}
}
