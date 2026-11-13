package workspace

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/shellrules"
)

// Tier identifies one of the four configuration precedence layers (WS-05).
type Tier int

// Configuration precedence tiers from lowest (1) to highest (4).
const (
	TierDefault   Tier = 1
	TierGlobal    Tier = 2
	TierWorkspace Tier = 3
	TierSession   Tier = 4

	keyPolicyApprovalMode = "policy.approval_mode"
)

// EnvTagRule maps a target glob pattern to an environment tag (interaction §3.4).
type EnvTagRule struct {
	Match string `toml:"match"`
	Tag   string `toml:"tag"`
}

// PolicyConfig holds the merged interaction and sandbox policy settings (F6.3b).
type PolicyConfig struct {
	ApprovalMode   string       `toml:"approval_mode,omitempty"`
	SandboxProfile string       `toml:"sandbox_profile,omitempty"`
	ShellAsk       []string     `toml:"shell_ask,omitempty"`
	ShellAllow     []string     `toml:"shell_allow,omitempty"`
	EnvTags        []EnvTagRule `toml:"env_tags,omitempty"`
}

// Env is validated and versioned since F5.6b but has no consumer in stage A
// (Q-71). F6.4 (env snapshot) and F6.6 (shell tool) must read it from the
// merged configuration; see docs/roadmap.md Q-71 before adding another env
// setting.
type Env struct {
	Keep             []string `toml:"keep,omitempty"`
	KeepAdd          []string `toml:"keep_add,omitempty"`
	OverrideAllow    []string `toml:"override_allow,omitempty"`
	OverrideAllowAdd []string `toml:"override_allow_add,omitempty"`
}

// RedactConfig holds content-safety redaction allowlists.
type RedactConfig struct {
	AllowKeys  []string `toml:"allow_keys,omitempty"`
	AllowPaths []string `toml:"allow_paths,omitempty"`
}

// RoleConfig defines a team role entry in the role catalog (orchestration §5.2, F11.1).
type RoleConfig struct {
	Name           string   `toml:"name"`
	Description    string   `toml:"description,omitempty"`
	Vendor         string   `toml:"vendor"`
	Model          string   `toml:"model,omitempty"`
	Effort         string   `toml:"effort,omitempty"`
	Permission     string   `toml:"permission"`
	IsLeadEligible bool     `toml:"is_lead_eligible,omitempty"`
	Checks         []string `toml:"checks,omitempty"`
}

// ProfileConfig defines a named vendor session profile (vendor-runtime §7, F9.9, Q-63 A).
type ProfileConfig struct {
	Vendor string `toml:"vendor,omitempty"`
	Model  string `toml:"model,omitempty"`
	Effort string `toml:"effort,omitempty"`
}

// SessionConfig holds session-scoped settings.
type SessionConfig struct{ ScratchDir, Model, Effort string }

// HostConfig holds host-wide settings valid only in Tier 2 (<FugueHome>/config.toml).
type HostConfig struct{ WorkspacesDir, OutputRetentionTotal, SeenVersionsPerSession, SnapshotDisk string }

// SessionOverride holds Tier 4 per-session overrides (Q-53 A: model and effort only).
type SessionOverride struct {
	Model  string `toml:"model,omitempty"`
	Effort string `toml:"effort,omitempty"`
}

// EffectiveConfig is the merged four-tier configuration with per-key provenance.
type EffectiveConfig struct {
	Name       string
	Roots      []Root
	Policy     PolicyConfig
	Env        Env
	Redact     RedactConfig
	Roles      []RoleConfig
	Profiles   map[string]ProfileConfig
	Session    SessionConfig
	Host       HostConfig
	Provenance map[string]Tier
}

// PolicyDiffEntry describes a single policy key change awaiting user confirmation (Q-64).
type PolicyDiffEntry struct{ Key, OldValue, NewValue string }

// DefaultConfig returns the Tier 1 embedded default configuration (workspace §5.2).
func DefaultConfig(fugueHome, workspaceID string) EffectiveConfig {
	wsDir, scratchDir, prov := filepath.Join(fugueHome, "workspaces"), "", make(map[string]Tier, 16)
	if workspaceID != "" {
		scratchDir = filepath.Join(wsDir, workspaceID, "scratch")
	}
	for _, k := range []string{keyPolicyApprovalMode, "policy.sandbox_profile", "policy.shell_ask", "policy.shell_allow", "policy.env_tags", "env.keep", "env.override_allow", "redact.allow_keys", "redact.allow_paths", "roles", "profiles", "session.scratch_dir", "host.workspaces_dir", "host.output_retention_total", "host.seen_versions_per_session", "host.snapshot_disk"} {
		prov[k] = TierDefault
	}
	return EffectiveConfig{
		Roots: []Root{}, Env: Env{Keep: []string{}, OverrideAllow: []string{}},
		Policy: PolicyConfig{ApprovalMode: "auto", SandboxProfile: "workspace-write", ShellAsk: []string{}, ShellAllow: []string{}, EnvTags: []EnvTagRule{}},
		Redact: RedactConfig{AllowKeys: []string{}, AllowPaths: []string{}}, Roles: []RoleConfig{}, Profiles: map[string]ProfileConfig{},
		Session: SessionConfig{ScratchDir: scratchDir}, Provenance: prov,
		Host: HostConfig{WorkspacesDir: wsDir, OutputRetentionTotal: "64 MiB", SeenVersionsPerSession: "8 MiB", SnapshotDisk: "1 GiB"},
	}
}

// TagPolicy reports whether an environment tag requires confirmation (interaction §3.4).
type TagPolicy interface {
	RequiresConfirmation(name string) bool
}

// ResolveEnvTag returns the environment tag for target using rules in declaration order.
func ResolveEnvTag(rules []EnvTagRule, target string) string {
	for _, r := range rules {
		if ok, err := path.Match(r.Match, target); target != "" && err == nil && ok {
			return r.Tag
		}
	}
	return ""
}

// IsShellAllowed reports whether command on target is permitted by policy.shell_allow
// without violating hard rule I-03 or matching default risk rules (interaction §3.3).
// Every simple command in a compound command must be covered by shell_allow.
func IsShellAllowed(cfg *EffectiveConfig, tags TagPolicy, command, target string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}
	tag := ""
	if target != "" {
		tag = ResolveEnvTag(cfg.Policy.EnvTags, target)
	}
	roots := make([]string, 0, len(cfg.Roots))
	for _, r := range cfg.Roots {
		roots = append(roots, r.Path)
	}
	return shellrules.IsAllowed(command, &shellrules.Options{
		WorkspaceRoots: roots,
		ShellAsk:       cfg.Policy.ShellAsk,
		ShellAllow:     cfg.Policy.ShellAllow,
		Target:         target,
		EnvTag:         tag,
		TagPolicy:      tags,
	})
}

func mergeConfigs(base *EffectiveConfig, t2 *tier2RawConfig, t3 *tier3RawConfig, t4 *SessionOverride, homeDir string) EffectiveConfig {
	out := *base
	out.Provenance, out.Profiles = maps.Clone(base.Provenance), maps.Clone(base.Profiles)
	if t2 != nil {
		applyCommonTier(&out, TierGlobal, nil, nil, nil, t2.Roles, t2.Profiles, t2.Session, homeDir)
		if t2.Host != nil {
			setPathScalar(&out.Host.WorkspacesDir, out.Provenance, "host.workspaces_dir", TierGlobal, t2.Host.WorkspacesDir, homeDir)
			setScalar(&out.Host.OutputRetentionTotal, out.Provenance, "host.output_retention_total", TierGlobal, t2.Host.OutputRetentionTotal)
			setScalar(&out.Host.SeenVersionsPerSession, out.Provenance, "host.seen_versions_per_session", TierGlobal, t2.Host.SeenVersionsPerSession)
			setScalar(&out.Host.SnapshotDisk, out.Provenance, "host.snapshot_disk", TierGlobal, t2.Host.SnapshotDisk)
		}
	}
	if t3 != nil {
		setScalar(&out.Name, out.Provenance, "name", TierWorkspace, t3.Name)
		if t3.Roots != nil {
			out.Roots, out.Provenance["roots"] = slices.Clone(*t3.Roots), TierWorkspace
		}
		applyCommonTier(&out, TierWorkspace, t3.Policy, t3.Env, t3.Redact, t3.Roles, t3.Profiles, t3.Session, homeDir)
	}
	if t4 != nil {
		setNonEmpty(&out.Session.Model, out.Provenance, "session.model", TierSession, t4.Model)
		setNonEmpty(&out.Session.Effort, out.Provenance, "session.effort", TierSession, t4.Effort)
	}
	return out
}

func applyCommonTier(
	dst *EffectiveConfig, tier Tier, pol *rawPolicy, env *rawEnv,
	red *rawRedact, roles *[]RoleConfig, profiles map[string]ProfileConfig,
	sess *rawSession, homeDir string,
) {
	if pol != nil {
		setScalar(&dst.Policy.ApprovalMode, dst.Provenance, keyPolicyApprovalMode, tier, pol.ApprovalMode)
		setScalar(&dst.Policy.SandboxProfile, dst.Provenance, "policy.sandbox_profile", tier, pol.SandboxProfile)
		mergeStringList(&dst.Policy.ShellAsk, dst.Provenance, "policy.shell_ask", tier, pol.ShellAsk, pol.ShellAskAdd)
		mergeStringList(&dst.Policy.ShellAllow, dst.Provenance, "policy.shell_allow", tier, pol.ShellAllow, pol.ShellAllowAdd)
		if pol.EnvTags != nil {
			dst.Policy.EnvTags, dst.Provenance["policy.env_tags"] = slices.Clone(*pol.EnvTags), tier
		}
	}
	if env != nil {
		mergeStringList(&dst.Env.Keep, dst.Provenance, "env.keep", tier, env.Keep, env.KeepAdd)
		mergeStringList(&dst.Env.OverrideAllow, dst.Provenance, "env.override_allow", tier, env.OverrideAllow, env.OverrideAllowAdd)
	}
	if red != nil {
		mergeStringList(&dst.Redact.AllowKeys, dst.Provenance, "redact.allow_keys", tier, red.AllowKeys, red.AllowKeysAdd)
		mergeStringList(&dst.Redact.AllowPaths, dst.Provenance, "redact.allow_paths", tier, red.AllowPaths, red.AllowPathsAdd)
	}
	if roles != nil {
		dst.Roles, dst.Provenance["roles"] = slices.Clone(*roles), tier
	}
	applyProfiles(dst, tier, profiles)
	if sess != nil {
		setPathScalar(&dst.Session.ScratchDir, dst.Provenance, "session.scratch_dir", tier, sess.ScratchDir, homeDir)
	}
}

func applyProfiles(dst *EffectiveConfig, tier Tier, profiles map[string]ProfileConfig) {
	for name, in := range profiles {
		cur, prefix := dst.Profiles[name], "profiles."+name
		dst.Provenance["profiles"], dst.Provenance[prefix] = tier, tier
		setNonEmpty(&cur.Vendor, dst.Provenance, prefix+".vendor", tier, in.Vendor)
		setNonEmpty(&cur.Model, dst.Provenance, prefix+".model", tier, in.Model)
		setNonEmpty(&cur.Effort, dst.Provenance, prefix+".effort", tier, in.Effort)
		dst.Profiles[name] = cur
	}
}

func setPathScalar(dst *string, prov map[string]Tier, key string, tier Tier, src *string, homeDir string) {
	if src != nil {
		*dst, prov[key] = expandHome(*src, homeDir), tier
	}
}

func setScalar(dst *string, prov map[string]Tier, key string, tier Tier, src *string) {
	if src != nil {
		*dst, prov[key] = *src, tier
	}
}

func setNonEmpty(dst *string, prov map[string]Tier, key string, tier Tier, val string) {
	if val != "" {
		*dst, prov[key] = val, tier
	}
}

func mergeStringList(dst *[]string, prov map[string]Tier, key string, tier Tier, replace, add *[]string) {
	if replace != nil {
		*dst, prov[key] = slices.Clone(*replace), tier
	} else if add != nil {
		*dst, prov[key] = append(slices.Clone(*dst), *add...), tier
	}
}

func diffPolicy(accepted, candidate *EffectiveConfig) []PolicyDiffEntry {
	var diffs []PolicyDiffEntry
	add := func(key, oldVal, newVal string) {
		if oldVal != newVal {
			diffs = append(diffs, PolicyDiffEntry{Key: key, OldValue: oldVal, NewValue: newVal})
		}
	}
	fmtStrs := func(s []string) string { return "[" + strings.Join(s, ", ") + "]" }
	add(keyPolicyApprovalMode, accepted.Policy.ApprovalMode, candidate.Policy.ApprovalMode)
	add("policy.sandbox_profile", accepted.Policy.SandboxProfile, candidate.Policy.SandboxProfile)
	add("policy.shell_ask", fmtStrs(accepted.Policy.ShellAsk), fmtStrs(candidate.Policy.ShellAsk))
	add("policy.shell_allow", fmtStrs(accepted.Policy.ShellAllow), fmtStrs(candidate.Policy.ShellAllow))
	add("policy.env_tags", fmt.Sprint(accepted.Policy.EnvTags), fmt.Sprint(candidate.Policy.EnvTags))
	add("env.keep", fmtStrs(accepted.Env.Keep), fmtStrs(candidate.Env.Keep))
	add("env.override_allow", fmtStrs(accepted.Env.OverrideAllow), fmtStrs(candidate.Env.OverrideAllow))
	add("redact.allow_keys", fmtStrs(accepted.Redact.AllowKeys), fmtStrs(candidate.Redact.AllowKeys))
	add("redact.allow_paths", fmtStrs(accepted.Redact.AllowPaths), fmtStrs(candidate.Redact.AllowPaths))
	add("roots", fmt.Sprint(accepted.Roots), fmt.Sprint(candidate.Roots))
	return diffs
}
