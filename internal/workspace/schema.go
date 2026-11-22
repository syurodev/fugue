package workspace

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
	"strconv"
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
	CrossReview    bool     `toml:"cross_review,omitempty"`
	Checks         []string `toml:"checks,omitempty"`
}

// ProfileConfig defines a named vendor session profile (vendor-runtime §7, F9.9, Q-63 A).
type ProfileConfig struct {
	Vendor string `toml:"vendor,omitempty"`
	Model  string `toml:"model,omitempty"`
	Effort string `toml:"effort,omitempty"`
}

// SessionConfig holds session-scoped settings. Teams controls whether the
// workspace's sessions are offered fugue_team_propose (F11.2a, Q-105);
// TeamsAutoApprove controls whether Fugue auto-approves a proposal that uses
// only catalog read roles and at most three agents (F11.2b, Q-102, Q-111).
// TeamsAutoApprove defaults to false: every proposal waits for the user
// unless the workspace turns it on, and an out-of-band change of either key
// waits for confirmation like every policy key (Q-64).
type SessionConfig struct {
	ScratchDir       string
	Model            string
	Effort           string
	Teams            bool
	TeamsAutoApprove bool
}

// TeamsConfig holds non-policy team execution settings valid in Tiers 1–3.
type TeamsConfig struct {
	// IdleDetachSeconds detaches idle team_manager/member sessions after this
	// many seconds (default 300; 0 disables).
	IdleDetachSeconds int
	// TaskDeadlineSeconds bounds a child task's wall-clock deadline (default 3600).
	TaskDeadlineSeconds int
	// TaskMaxTurns bounds how many vendor turns a child task may start (default 50).
	TaskMaxTurns int
	// SilenceTimeoutSeconds triggers a non-lethal stall escalation when a
	// starting/running child task emits no observation or task event (default 600; 0 disables).
	SilenceTimeoutSeconds int
	// TeamDeadlineSeconds bounds an active team's dispatch window (default 14400).
	TeamDeadlineSeconds int
	// TeamMaxAgents caps total non-lead tasks (including retries) in a team without
	// user-approved amendment (default 12).
	TeamMaxAgents int
	// ApproveEveryDiffStep raises a user approval card before each diff task is
	// enqueued for integration (§9).
	ApproveEveryDiffStep bool
}

// HostConfig holds host-wide settings valid only in Tier 2 (<FugueHome>/config.toml).
type HostConfig struct {
	WorkspacesDir          string
	OutputRetentionTotal   string
	SeenVersionsPerSession string
	SnapshotDisk           string
	// PerAccountSessions overrides the §7 per-account concurrent session cap (default 6).
	PerAccountSessions int
	// MaxSessions overrides the §7 machine-wide concurrent session cap (0 = RAM formula).
	MaxSessions int
	// HeavyCommands overrides the §7 heavy-command semaphore cap (0 = max(1, cores/4)).
	HeavyCommands int
	// HeavySlotMaxHoldSeconds bounds how long one caller may retain a
	// heavy-command semaphore slot before the host closes its stream (default 3600; 0 disables).
	HeavySlotMaxHoldSeconds int
	// PromptPacingMinSeconds and PromptPacingMaxSeconds bound the §6.1
	// per-vendor jitter delay (seconds) for origin=fugue prompts (defaults 10..15).
	PromptPacingMinSeconds int
	PromptPacingMaxSeconds int
	// MultiAccountEnabled opts into multiple accounts per vendor (F11.8).
	MultiAccountEnabled bool
	// MultiAccountTermsAccepted records user acceptance of vendor terms warnings (F11.8).
	MultiAccountTermsAccepted bool
	// AccountStrategy selects among multiple accounts ("order", "best", "consumeFirst").
	AccountStrategy string
	// ExtraAccounts lists additional account IDs per vendor (F11.8).
	ExtraAccounts map[string][]string
}

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
	Teams      TeamsConfig
	Host       HostConfig
	Provenance map[string]Tier
}

// PolicyDiffEntry describes a single policy key change awaiting user confirmation (Q-64).
type PolicyDiffEntry struct{ Key, OldValue, NewValue string }

// DefaultConfig returns the Tier 1 embedded default configuration (workspace §5.2).
func DefaultConfig(fugueHome, workspaceID string) EffectiveConfig {
	wsDir, scratchDir, prov := filepath.Join(fugueHome, "workspaces"), "", make(map[string]Tier, 24)
	if workspaceID != "" {
		scratchDir = filepath.Join(wsDir, workspaceID, "scratch")
	}
	for _, k := range defaultProvenanceKeys() {
		prov[k] = TierDefault
	}
	return EffectiveConfig{
		Roots: []Root{}, Env: Env{Keep: []string{}, OverrideAllow: []string{}},
		Policy: PolicyConfig{ApprovalMode: "auto", SandboxProfile: "workspace-write", ShellAsk: []string{}, ShellAllow: []string{}, EnvTags: []EnvTagRule{}},
		Redact: RedactConfig{AllowKeys: []string{}, AllowPaths: []string{}}, Roles: []RoleConfig{}, Profiles: map[string]ProfileConfig{},
		Session: SessionConfig{ScratchDir: scratchDir, Teams: true}, Provenance: prov,
		Teams: TeamsConfig{
			IdleDetachSeconds: 300, TaskDeadlineSeconds: 3600, TaskMaxTurns: 50,
			SilenceTimeoutSeconds: 600, TeamDeadlineSeconds: 14400, TeamMaxAgents: 12,
		},
		Host: HostConfig{
			WorkspacesDir: wsDir, OutputRetentionTotal: "64 MiB", SeenVersionsPerSession: "8 MiB", SnapshotDisk: "1 GiB",
			PerAccountSessions: 6, HeavySlotMaxHoldSeconds: 3600, PromptPacingMinSeconds: 10, PromptPacingMaxSeconds: 15,
			AccountStrategy: "order",
		},
	}
}

func defaultProvenanceKeys() []string {
	return []string{
		keyPolicyApprovalMode, "policy.sandbox_profile", "policy.shell_ask", "policy.shell_allow", "policy.env_tags",
		"env.keep", "env.override_allow", "redact.allow_keys", "redact.allow_paths", "roles", "profiles",
		"session.scratch_dir", "session.teams", "session.teams_auto_approve",
		"teams.idle_detach_seconds", "teams.task_deadline_seconds", "teams.task_max_turns",
		"teams.silence_timeout_seconds", "teams.team_deadline_seconds", "teams.team_max_agents",
		"teams.approve_every_diff_step",
		"host.workspaces_dir", "host.output_retention_total", "host.seen_versions_per_session", "host.snapshot_disk",
		"host.per_account_sessions", "host.max_sessions", "host.heavy_commands", "host.heavy_slot_max_hold_seconds",
		"host.prompt_pacing_min_seconds", "host.prompt_pacing_max_seconds",
		"host.multi_account_enabled", "host.multi_account_terms_accepted", "host.account_strategy", "host.extra_accounts",
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
	return shellrules.IsAllowed(command, shellOptionsFor(cfg, tags, target, tag))
}

func mergeConfigs(base *EffectiveConfig, t2 *tier2RawConfig, t3 *tier3RawConfig, t4 *SessionOverride, homeDir string) EffectiveConfig {
	out := *base
	out.Provenance, out.Profiles = maps.Clone(base.Provenance), maps.Clone(base.Profiles)
	if t2 != nil {
		applyCommonTier(&out, TierGlobal, nil, nil, nil, t2.Roles, t2.Profiles, t2.Session, t2.Teams, homeDir)
		applyHostTier(&out, t2.Host, homeDir)
	}
	if t3 != nil {
		setScalar(&out.Name, out.Provenance, "name", TierWorkspace, t3.Name)
		if t3.Roots != nil {
			out.Roots, out.Provenance["roots"] = slices.Clone(*t3.Roots), TierWorkspace
		}
		applyCommonTier(&out, TierWorkspace, t3.Policy, t3.Env, t3.Redact, t3.Roles, t3.Profiles, t3.Session, t3.Teams, homeDir)
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
	sess *rawSession, teamsCfg *rawTeams, homeDir string,
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
		dst.Roles, dst.Provenance["roles"] = mergeRoles(dst.Roles, *roles), tier
	}
	applyProfiles(dst, tier, profiles)
	applySessionTier(dst, tier, sess, homeDir)
	applyTeamsTier(dst, tier, teamsCfg)
}

func applyHostTier(dst *EffectiveConfig, host *rawHost, homeDir string) {
	if host == nil {
		return
	}
	setPathScalar(&dst.Host.WorkspacesDir, dst.Provenance, "host.workspaces_dir", TierGlobal, host.WorkspacesDir, homeDir)
	setScalar(&dst.Host.OutputRetentionTotal, dst.Provenance, "host.output_retention_total", TierGlobal, host.OutputRetentionTotal)
	setScalar(&dst.Host.SeenVersionsPerSession, dst.Provenance, "host.seen_versions_per_session", TierGlobal, host.SeenVersionsPerSession)
	setScalar(&dst.Host.SnapshotDisk, dst.Provenance, "host.snapshot_disk", TierGlobal, host.SnapshotDisk)
	setIntScalar(&dst.Host.PerAccountSessions, dst.Provenance, "host.per_account_sessions", TierGlobal, host.PerAccountSessions)
	setIntScalar(&dst.Host.MaxSessions, dst.Provenance, "host.max_sessions", TierGlobal, host.MaxSessions)
	setIntScalar(&dst.Host.HeavyCommands, dst.Provenance, "host.heavy_commands", TierGlobal, host.HeavyCommands)
	setIntScalar(&dst.Host.HeavySlotMaxHoldSeconds, dst.Provenance, "host.heavy_slot_max_hold_seconds", TierGlobal, host.HeavySlotMaxHoldSeconds)
	setIntScalar(&dst.Host.PromptPacingMinSeconds, dst.Provenance, "host.prompt_pacing_min_seconds", TierGlobal, host.PromptPacingMinSeconds)
	setIntScalar(&dst.Host.PromptPacingMaxSeconds, dst.Provenance, "host.prompt_pacing_max_seconds", TierGlobal, host.PromptPacingMaxSeconds)
	if host.MultiAccountEnabled != nil {
		dst.Host.MultiAccountEnabled = *host.MultiAccountEnabled
		dst.Provenance["host.multi_account_enabled"] = TierGlobal
	}
	if host.MultiAccountTermsAccepted != nil {
		dst.Host.MultiAccountTermsAccepted = *host.MultiAccountTermsAccepted
		dst.Provenance["host.multi_account_terms_accepted"] = TierGlobal
	}
	setScalar(&dst.Host.AccountStrategy, dst.Provenance, "host.account_strategy", TierGlobal, host.AccountStrategy)
	if len(host.ExtraAccounts) > 0 {
		copied := make(map[string][]string, len(host.ExtraAccounts))
		for k, v := range host.ExtraAccounts {
			copied[k] = slices.Clone(v)
		}
		dst.Host.ExtraAccounts = copied
		dst.Provenance["host.extra_accounts"] = TierGlobal
	}
}

func applyTeamsTier(dst *EffectiveConfig, tier Tier, teamsCfg *rawTeams) {
	if teamsCfg == nil {
		return
	}
	setIntScalar(&dst.Teams.IdleDetachSeconds, dst.Provenance, "teams.idle_detach_seconds", tier, teamsCfg.IdleDetachSeconds)
	setIntScalar(&dst.Teams.TaskDeadlineSeconds, dst.Provenance, "teams.task_deadline_seconds", tier, teamsCfg.TaskDeadlineSeconds)
	setIntScalar(&dst.Teams.TaskMaxTurns, dst.Provenance, "teams.task_max_turns", tier, teamsCfg.TaskMaxTurns)
	setIntScalar(&dst.Teams.SilenceTimeoutSeconds, dst.Provenance, "teams.silence_timeout_seconds", tier, teamsCfg.SilenceTimeoutSeconds)
	setIntScalar(&dst.Teams.TeamDeadlineSeconds, dst.Provenance, "teams.team_deadline_seconds", tier, teamsCfg.TeamDeadlineSeconds)
	setIntScalar(&dst.Teams.TeamMaxAgents, dst.Provenance, "teams.team_max_agents", tier, teamsCfg.TeamMaxAgents)
	if teamsCfg.ApproveEveryDiffStep != nil {
		dst.Teams.ApproveEveryDiffStep = *teamsCfg.ApproveEveryDiffStep
		dst.Provenance["teams.approve_every_diff_step"] = tier
	}
}

func applySessionTier(dst *EffectiveConfig, tier Tier, sess *rawSession, homeDir string) {
	if sess != nil {
		setPathScalar(&dst.Session.ScratchDir, dst.Provenance, "session.scratch_dir", tier, sess.ScratchDir, homeDir)
		if sess.Teams != nil {
			dst.Session.Teams, dst.Provenance["session.teams"] = *sess.Teams, tier
		}
		if sess.TeamsAutoApprove != nil {
			dst.Session.TeamsAutoApprove, dst.Provenance["session.teams_auto_approve"] = *sess.TeamsAutoApprove, tier
		}
	}
}

func setIntScalar(dst *int, prov map[string]Tier, key string, tier Tier, src *int) {
	if src != nil {
		*dst, prov[key] = *src, tier
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
	add("roles", formatRolesForDiff(accepted.Roles), formatRolesForDiff(candidate.Roles))
	add("session.teams", strconv.FormatBool(accepted.Session.Teams), strconv.FormatBool(candidate.Session.Teams))
	add("session.teams_auto_approve", strconv.FormatBool(accepted.Session.TeamsAutoApprove), strconv.FormatBool(candidate.Session.TeamsAutoApprove))
	return diffs
}

func mergeRoles(base, overlay []RoleConfig) []RoleConfig {
	out := make([]RoleConfig, 0, len(base)+len(overlay))
	index := make(map[string]int, len(base)+len(overlay))
	for i := range base {
		r := base[i]
		r.Checks = slices.Clone(r.Checks)
		index[r.Name] = len(out)
		out = append(out, r)
	}
	for i := range overlay {
		r := overlay[i]
		r.Checks = slices.Clone(r.Checks)
		if idx, exists := index[r.Name]; exists {
			out[idx] = r
			continue
		}
		index[r.Name] = len(out)
		out = append(out, r)
	}
	return out
}

func formatRolesForDiff(roles []RoleConfig) string {
	if len(roles) == 0 {
		return "[]"
	}
	parts := make([]string, len(roles))
	for i := range roles {
		r := &roles[i]
		// Checks are free-form strings, so each one is quoted: a joined
		// rendering must never make two distinct catalogs compare equal (a
		// single check "task lint, task test" is not ["task lint", "task
		// test"]), or an out-of-band check edit would skip confirmation.
		checks := make([]string, len(r.Checks))
		for j, check := range r.Checks {
			checks[j] = strconv.Quote(check)
		}
		parts[i] = fmt.Sprintf(
			"{name:%s vendor:%s model:%s effort:%s permission:%s lead:%t checks:[%s] desc:%q}",
			r.Name, r.Vendor, r.Model, r.Effort, r.Permission, r.IsLeadEligible,
			strings.Join(checks, ", "), r.Description,
		)
	}
	return "[" + strings.Join(parts, "; ") + "]"
}
