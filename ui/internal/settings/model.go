package settings

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/pkg/client"
)

// Field identifiers matching EffectiveWorkspaceConfig.Provenance keys.
const (
	FieldApprovalMode          = "policy.approval_mode"
	FieldSandboxProfile        = "policy.sandbox_profile"
	FieldShellAllow            = "policy.shell_allow"
	FieldEnvTags               = "policy.env_tags"
	FieldSessionTeams          = "session.teams"
	FieldTeamsAutoApprove      = "session.teams_auto_approve"
	FieldIdleDetachSeconds     = "teams.idle_detach_seconds"
	FieldTaskDeadlineSeconds   = "teams.task_deadline_seconds"
	FieldTaskMaxTurns          = "teams.task_max_turns"
	FieldSilenceTimeoutSeconds = "teams.silence_timeout_seconds"
	FieldTeamDeadlineSeconds   = "teams.team_deadline_seconds"
	FieldTeamMaxAgents         = "teams.team_max_agents"
)

// Validation bounds matching internal/workspace/toml.go.
const (
	MaxIdleDetachSeconds     = 86400
	MaxTeamDeadlineSeconds   = 7 * 24 * 3600
	MaxTaskTurns             = 1000
	MaxSilenceTimeoutSeconds = 86400
	MaxTeamAgents            = 64
)

var (
	errInvalidEnvTagRule = errors.New("env tag rules must use match=tag format with non-empty match and tag")
	errInvalidIntRange   = errors.New("integer setting is out of allowed range")
)

// FieldChange summarizes one setting that changed on the host underneath
// unsaved user edits during a CAS conflict.
type FieldChange struct {
	ID       string
	Label    string
	OldValue string
	NewValue string
}

// Model holds both app preferences and the currently selected workspace's
// structured settings, effective provenance, and CAS conflict tracking.
type Model struct {
	Prefs PrefsState

	WorkspaceID               string
	WorkspaceName             string
	Loaded                    bool
	Loading                   bool
	Saving                    bool
	PendingPolicyConfirmation bool
	PolicyConfirmArmed        bool
	PolicyDecisionBusy        bool

	ConfigHash           string
	RawTOML              []byte
	RawDraft             string
	RawRevision          uint64
	RawConflict          bool
	ConflictHash         string
	ShowConflictDiff     bool
	Validating           bool
	ValidationIssues     []client.WorkspaceConfigIssue
	ValidationErr        string
	PendingPolicyDiff    []client.PolicyDiffEntry
	PendingRawPolicyDiff []client.PolicyDiffEntry
	ConfigErrorKey       string
	ConfigErrorLine      int64
	ConfigErrorColumn    int64
	Baseline             client.WorkspaceSettings
	Draft                client.WorkspaceSettings
	Provenance           map[string]int64

	ChangedUnderneath []FieldChange
	LoadErr           string
	SaveErr           string
}

// NewModel creates an initialized settings model with default preferences.
func NewModel() *Model {
	return &Model{
		Prefs:      PrefsState{Current: DefaultPreferences()},
		Provenance: map[string]int64{},
	}
}

// IsPolicyField reports whether id is a policy-protected key requiring an
// explicit user confirmation step before saving.
func IsPolicyField(id string) bool {
	switch id {
	case FieldApprovalMode, FieldSandboxProfile, FieldShellAllow, FieldEnvTags,
		FieldSessionTeams, FieldTeamsAutoApprove:
		return true
	default:
		return false
	}
}

// TierLabel returns a short human label for a configuration provenance tier.
func TierLabel(tier int64) string {
	switch tier {
	case client.ConfigTierDefault:
		return "default"
	case client.ConfigTierGlobal:
		return "global"
	case client.ConfigTierWorkspace:
		return "workspace"
	case client.ConfigTierSession:
		return "session"
	default:
		return "default"
	}
}

// FieldLabel returns the display label for a structured workspace setting ID.
func FieldLabel(id string) string {
	labels := map[string]string{
		FieldApprovalMode:          "Approval mode",
		FieldSandboxProfile:        "Sandbox profile",
		FieldShellAllow:            "Shell allow prefixes",
		FieldEnvTags:               "Environment tags",
		FieldSessionTeams:          "Offer team proposals",
		FieldTeamsAutoApprove:      "Auto-approve read-only teams",
		FieldIdleDetachSeconds:     "Idle detach timeout",
		FieldTaskDeadlineSeconds:   "Task deadline",
		FieldTaskMaxTurns:          "Task max turns",
		FieldSilenceTimeoutSeconds: "Silence timeout",
		FieldTeamDeadlineSeconds:   "Team deadline",
		FieldTeamMaxAgents:         "Team max agents",
	}
	if label, ok := labels[id]; ok {
		return label
	}
	return id
}

// TierFor returns the provenance tier for id, defaulting to ConfigTierDefault.
func (m *Model) TierFor(id string) int64 {
	if tier, ok := m.Provenance[id]; ok && tier > 0 {
		return tier
	}
	return client.ConfigTierDefault
}

// SelectWorkspace switches the target workspace for settings loading.
func (m *Model) SelectWorkspace(id, name string) {
	if m.WorkspaceID == id {
		if name != "" {
			m.WorkspaceName = name
		}
		return
	}
	m.WorkspaceID = id
	m.WorkspaceName = name
	m.Loaded = false
	m.Loading = false
	m.Saving = false
	m.PolicyConfirmArmed = false
	m.PolicyDecisionBusy = false
	m.PendingPolicyConfirmation = false
	m.ConfigHash = ""
	m.RawTOML = nil
	m.RawDraft = ""
	m.RawRevision++
	m.RawConflict = false
	m.ConflictHash = ""
	m.ShowConflictDiff = false
	m.Validating = false
	m.ValidationIssues = nil
	m.ValidationErr = ""
	m.PendingPolicyDiff = nil
	m.PendingRawPolicyDiff = nil
	m.ConfigErrorKey = ""
	m.ConfigErrorLine = 0
	m.ConfigErrorColumn = 0
	m.Baseline = client.WorkspaceSettings{}
	m.Draft = client.WorkspaceSettings{}
	m.Provenance = map[string]int64{}
	m.ChangedUnderneath = nil
	m.LoadErr = ""
	m.SaveErr = ""
}

// ApplyWorkspaceLoaded applies a loaded workspace and effective config,
// replacing Draft when the load is fresh rather than a CAS conflict reload.
func (m *Model) ApplyWorkspaceLoaded(ws *client.Workspace, eff *client.EffectiveWorkspaceConfig) {
	if ws == nil || eff == nil || (m.WorkspaceID != "" && ws.ID != m.WorkspaceID) {
		return
	}
	m.WorkspaceID = ws.ID
	m.WorkspaceName = ws.Name
	m.Loaded = true
	m.Loading = false
	m.LoadErr = ""
	m.ConfigHash = ws.ConfigHash
	m.RawTOML = slices.Clone(ws.RawTOML)
	m.RawDraft = string(ws.RawTOML)
	m.RawRevision++
	m.RawConflict = false
	m.ConflictHash = ""
	m.ShowConflictDiff = false
	m.Validating = false
	m.ValidationIssues = nil
	m.ValidationErr = ""
	m.PendingPolicyConfirmation = ws.PendingPolicyConfirmation
	m.PendingPolicyDiff = slices.Clone(ws.PolicyDiff)
	m.PendingRawPolicyDiff = nil
	m.ConfigErrorKey = ws.ConfigErrorKey
	m.ConfigErrorLine = ws.ConfigErrorLine
	m.ConfigErrorColumn = ws.ConfigErrorColumn
	m.PolicyDecisionBusy = false
	m.Baseline = cloneWorkspaceSettings(&eff.Settings)
	m.Draft = cloneWorkspaceSettings(&eff.Settings)
	m.Provenance = maps.Clone(eff.Provenance)
	if m.Provenance == nil {
		m.Provenance = map[string]int64{}
	}
	m.ChangedUnderneath = nil
	m.PolicyConfirmArmed = false
}

// ApplyCASConflictReload updates the baseline, hash, raw TOML, and provenance
// after a CAS conflict while preserving the user's unsaved Draft and RawDraft
// edits and recording which fields changed underneath.
func (m *Model) ApplyCASConflictReload(ws *client.Workspace, eff *client.EffectiveWorkspaceConfig, conflictErr error) {
	m.Saving = false
	m.PolicyConfirmArmed = false
	m.PendingRawPolicyDiff = nil
	if conflictErr != nil {
		m.SaveErr = conflictErr.Error()
	}
	if ws == nil || eff == nil {
		return
	}
	m.ChangedUnderneath = diffSettings(&m.Baseline, &eff.Settings)
	m.ConfigHash = ws.ConfigHash
	m.RawTOML = slices.Clone(ws.RawTOML)
	m.RawConflict = true
	m.ConflictHash = ws.ConfigHash
	m.PendingPolicyConfirmation = ws.PendingPolicyConfirmation
	m.PendingPolicyDiff = slices.Clone(ws.PolicyDiff)
	m.ConfigErrorKey = ws.ConfigErrorKey
	m.ConfigErrorLine = ws.ConfigErrorLine
	m.ConfigErrorColumn = ws.ConfigErrorColumn
	m.Baseline = cloneWorkspaceSettings(&eff.Settings)
	m.Provenance = maps.Clone(eff.Provenance)
	if m.Provenance == nil {
		m.Provenance = map[string]int64{}
	}
}

// DiscardDraft resets Draft to Baseline and clears any conflict diff or error.
func (m *Model) DiscardDraft() {
	m.Draft = cloneWorkspaceSettings(&m.Baseline)
	m.ChangedUnderneath = nil
	m.PolicyConfirmArmed = false
	m.SaveErr = ""
}

// DirtyFields returns the ordered IDs of structured settings modified in Draft.
func (m *Model) DirtyFields() []string {
	diffs := diffSettings(&m.Baseline, &m.Draft)
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.ID)
	}
	return out
}

// DirtyPolicyFields returns the dirty field IDs that are policy keys.
func (m *Model) DirtyPolicyFields() []string {
	var out []string
	for _, id := range m.DirtyFields() {
		if IsPolicyField(id) {
			out = append(out, id)
		}
	}
	return out
}

// BuildPatch builds a WorkspaceSettingsPatch containing only modified fields.
func (m *Model) BuildPatch() (*client.WorkspaceSettingsPatch, bool) {
	dirty := m.DirtyFields()
	if len(dirty) == 0 {
		return nil, false
	}
	patch := &client.WorkspaceSettingsPatch{}
	for _, id := range dirty {
		applyFieldToPatch(patch, id, &m.Draft)
	}
	return patch, true
}

func applyFieldToPatch(patch *client.WorkspaceSettingsPatch, id string, draft *client.WorkspaceSettings) {
	switch id {
	case FieldApprovalMode:
		v := draft.ApprovalMode
		patch.ApprovalMode = &v
	case FieldSandboxProfile:
		v := draft.SandboxProfile
		patch.SandboxProfile = &v
	case FieldShellAllow:
		v := slices.Clone(draft.ShellAllow)
		patch.ShellAllow = &v
	case FieldEnvTags:
		v := slices.Clone(draft.EnvTags)
		patch.EnvTags = &v
	case FieldSessionTeams:
		v := draft.SessionTeams
		patch.SessionTeams = &v
	case FieldTeamsAutoApprove:
		v := draft.TeamsAutoApprove
		patch.TeamsAutoApprove = &v
	default:
		applyTeamsFieldToPatch(patch, id, draft)
	}
}

func applyTeamsFieldToPatch(patch *client.WorkspaceSettingsPatch, id string, draft *client.WorkspaceSettings) {
	switch id {
	case FieldIdleDetachSeconds:
		v := draft.IdleDetachSeconds
		patch.IdleDetachSeconds = &v
	case FieldTaskDeadlineSeconds:
		v := draft.TaskDeadlineSeconds
		patch.TaskDeadlineSeconds = &v
	case FieldTaskMaxTurns:
		v := draft.TaskMaxTurns
		patch.TaskMaxTurns = &v
	case FieldSilenceTimeoutSeconds:
		v := draft.SilenceTimeoutSeconds
		patch.SilenceTimeoutSeconds = &v
	case FieldTeamDeadlineSeconds:
		v := draft.TeamDeadlineSeconds
		patch.TeamDeadlineSeconds = &v
	case FieldTeamMaxAgents:
		v := draft.TeamMaxAgents
		patch.TeamMaxAgents = &v
	}
}

// FormatShellAllow formats shell_allow entries as a comma-separated string.
func FormatShellAllow(items []string) string {
	return strings.Join(items, ", ")
}

// ParseShellAllow parses comma- or newline-separated shell allow prefixes.
func ParseShellAllow(raw string) []string {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// FormatEnvTags formats env_tags rules as comma-separated match=tag pairs.
func FormatEnvTags(rules []client.EnvTagRule) string {
	parts := make([]string, 0, len(rules))
	for _, r := range rules {
		parts = append(parts, r.Match+"="+r.Tag)
	}
	return strings.Join(parts, ", ")
}

// ParseEnvTags parses comma- or newline-separated match=tag entries.
func ParseEnvTags(raw string) ([]client.EnvTagRule, error) {
	parts := strings.FieldsFunc(raw, func(r rune) bool { return r == ',' || r == '\n' })
	out := make([]client.EnvTagRule, 0, len(parts))
	for _, p := range parts {
		trimmed := strings.TrimSpace(p)
		if trimmed == "" {
			continue
		}
		match, tag, ok := strings.Cut(trimmed, "=")
		match, tag = strings.TrimSpace(match), strings.TrimSpace(tag)
		if !ok || match == "" || tag == "" {
			return nil, fmt.Errorf("%w: %q", errInvalidEnvTagRule, trimmed)
		}
		out = append(out, client.EnvTagRule{Match: match, Tag: tag})
	}
	return out, nil
}

// ParseBoundedInt parses an integer field and verifies it falls within [minVal, maxVal].
func ParseBoundedInt(raw string, minVal, maxVal int) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("parse integer %q: %w", raw, err)
	}
	if n < minVal || n > maxVal {
		return 0, fmt.Errorf("%w: must be between %d and %d", errInvalidIntRange, minVal, maxVal)
	}
	return n, nil
}

func cloneWorkspaceSettings(s *client.WorkspaceSettings) client.WorkspaceSettings {
	out := *s
	out.ShellAllow = slices.Clone(s.ShellAllow)
	out.EnvTags = slices.Clone(s.EnvTags)
	return out
}

func diffSettings(a, b *client.WorkspaceSettings) []FieldChange {
	var out []FieldChange
	add := func(id, oldVal, newVal string) {
		if oldVal != newVal {
			out = append(out, FieldChange{ID: id, Label: FieldLabel(id), OldValue: oldVal, NewValue: newVal})
		}
	}
	add(FieldApprovalMode, a.ApprovalMode, b.ApprovalMode)
	add(FieldSandboxProfile, a.SandboxProfile, b.SandboxProfile)
	if !slices.Equal(a.ShellAllow, b.ShellAllow) {
		add(FieldShellAllow, FormatShellAllow(a.ShellAllow), FormatShellAllow(b.ShellAllow))
	}
	if !slices.Equal(a.EnvTags, b.EnvTags) {
		add(FieldEnvTags, FormatEnvTags(a.EnvTags), FormatEnvTags(b.EnvTags))
	}
	add(FieldSessionTeams, strconv.FormatBool(a.SessionTeams), strconv.FormatBool(b.SessionTeams))
	add(FieldTeamsAutoApprove, strconv.FormatBool(a.TeamsAutoApprove), strconv.FormatBool(b.TeamsAutoApprove))
	add(FieldIdleDetachSeconds, strconv.Itoa(a.IdleDetachSeconds), strconv.Itoa(b.IdleDetachSeconds))
	add(FieldTaskDeadlineSeconds, strconv.Itoa(a.TaskDeadlineSeconds), strconv.Itoa(b.TaskDeadlineSeconds))
	add(FieldTaskMaxTurns, strconv.Itoa(a.TaskMaxTurns), strconv.Itoa(b.TaskMaxTurns))
	add(FieldSilenceTimeoutSeconds, strconv.Itoa(a.SilenceTimeoutSeconds), strconv.Itoa(b.SilenceTimeoutSeconds))
	add(FieldTeamDeadlineSeconds, strconv.Itoa(a.TeamDeadlineSeconds), strconv.Itoa(b.TeamDeadlineSeconds))
	add(FieldTeamMaxAgents, strconv.Itoa(a.TeamMaxAgents), strconv.Itoa(b.TeamMaxAgents))
	return out
}
