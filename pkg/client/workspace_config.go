package client

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"github.com/pelletier/go-toml/v2"

	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
)

// Configuration precedence tiers matching workspace §5.1.
const (
	ConfigTierDefault   int64 = 1
	ConfigTierGlobal    int64 = 2
	ConfigTierWorkspace int64 = 3
	ConfigTierSession   int64 = 4
)

// Public workspace config error codes returned by the Host API.
const (
	WorkspaceCodeConfigChanged = "workspace.config_changed"
	WorkspaceCodeConfigInvalid = "workspace.config_invalid"
)

// Typed workspace configuration error sentinels for errors.Is.
var (
	ErrWorkspaceConfigChanged = errors.New("workspace configuration changed since it was reviewed")
	ErrWorkspaceConfigInvalid = errors.New("workspace configuration is invalid")
)

// WorkspaceConfigConflictError reports a compare-and-swap hash mismatch on
// UpdateWorkspaceConfig.
type WorkspaceConfigConflictError struct {
	Cause error
}

// Error implements error.
func (e *WorkspaceConfigConflictError) Error() string { return e.Cause.Error() }

// Unwrap returns the underlying connect error so WorkspaceCodeOf still reads
// the public code.
func (e *WorkspaceConfigConflictError) Unwrap() error { return e.Cause }

// Is matches ErrWorkspaceConfigChanged.
func (e *WorkspaceConfigConflictError) Is(target error) bool {
	return target == ErrWorkspaceConfigChanged
}

// WorkspaceConfigValidationError reports a strict decode or validation
// rejection from UpdateWorkspaceConfig, carrying the offending key and
// source location when the host reported them.
type WorkspaceConfigValidationError struct {
	Key     string
	Line    int64
	Column  int64
	Message string
	Cause   error
}

// Error implements error.
func (e *WorkspaceConfigValidationError) Error() string { return e.Cause.Error() }

// Unwrap returns the underlying connect error.
func (e *WorkspaceConfigValidationError) Unwrap() error { return e.Cause }

// Is matches ErrWorkspaceConfigInvalid.
func (e *WorkspaceConfigValidationError) Is(target error) bool {
	return target == ErrWorkspaceConfigInvalid
}

// WorkspaceConfigIssue describes one issue reported by ValidateConfig,
// including key and 1-based line:col position for editor annotations.
type WorkspaceConfigIssue struct {
	Key     string
	Line    int64
	Column  int64
	Message string
}

// EnvTagRule maps a target glob pattern to an environment tag.
type EnvTagRule struct {
	Match string
	Tag   string
}

// WorkspaceSettings holds the common non-secret workspace settings exposed in
// the structured settings forms.
type WorkspaceSettings struct {
	ApprovalMode          string
	SandboxProfile        string
	ShellAllow            []string
	EnvTags               []EnvTagRule
	SessionTeams          bool
	TeamsAutoApprove      bool
	IdleDetachSeconds     int
	TaskDeadlineSeconds   int
	TaskMaxTurns          int
	SilenceTimeoutSeconds int
	TeamDeadlineSeconds   int
	TeamMaxAgents         int
}

// WorkspaceSettingsPatch carries only the fields modified by a structured edit.
// Nil fields leave the workspace tier untouched.
type WorkspaceSettingsPatch struct {
	ApprovalMode          *string
	SandboxProfile        *string
	ShellAllow            *[]string
	EnvTags               *[]EnvTagRule
	SessionTeams          *bool
	TeamsAutoApprove      *bool
	IdleDetachSeconds     *int
	TaskDeadlineSeconds   *int
	TaskMaxTurns          *int
	SilenceTimeoutSeconds *int
	TeamDeadlineSeconds   *int
	TeamMaxAgents         *int
}

// EffectiveWorkspaceConfig holds the merged four-tier values, per-key tier
// provenance, and typed structured settings for one workspace.
type EffectiveWorkspaceConfig struct {
	Values     map[string]any
	Provenance map[string]int64
	Settings   WorkspaceSettings
}

// GetEffectiveConfig reads the merged effective configuration and per-key tier
// provenance for workspaceID.
func (c *Client) GetEffectiveConfig(ctx context.Context, workspaceID string) (EffectiveWorkspaceConfig, error) {
	res, err := workspaceClient(c).GetEffectiveConfig(ctx,
		connect.NewRequest(&workspacev1.GetEffectiveConfigRequest{Id: workspaceID}))
	if err != nil {
		return EffectiveWorkspaceConfig{}, fmt.Errorf("read effective workspace configuration: %w", err)
	}
	values := res.Msg.GetValues().AsMap()
	prov := maps.Clone(res.Msg.GetProvenance())
	if prov == nil {
		prov = map[string]int64{}
	}
	return EffectiveWorkspaceConfig{
		Values:     values,
		Provenance: prov,
		Settings:   extractWorkspaceSettings(values),
	}, nil
}

// ValidateConfig validates candidate rawTOML against workspaceID's Tier 3
// schema without persisting it.
func (c *Client) ValidateConfig(ctx context.Context, workspaceID string, rawTOML []byte) ([]WorkspaceConfigIssue, error) {
	res, err := workspaceClient(c).ValidateConfig(ctx,
		connect.NewRequest(&workspacev1.ValidateConfigRequest{Id: workspaceID, RawToml: rawTOML}))
	if err != nil {
		return nil, fmt.Errorf("validate workspace configuration: %w", err)
	}
	raw := res.Msg.GetIssues()
	issues := make([]WorkspaceConfigIssue, 0, len(raw))
	for _, it := range raw {
		issues = append(issues, WorkspaceConfigIssue{
			Key:     it.GetKey(),
			Line:    it.GetLine(),
			Column:  it.GetColumn(),
			Message: it.GetMessage(),
		})
	}
	return issues, nil
}

// UpdateWorkspaceConfig atomically writes rawTOML to the workspace tier when
// expectedHash still matches the host's current config hash.
func (c *Client) UpdateWorkspaceConfig(ctx context.Context, workspaceID string, rawTOML []byte, expectedHash string) (Workspace, error) {
	res, err := workspaceClient(c).UpdateWorkspaceConfig(ctx,
		connect.NewRequest(&workspacev1.UpdateWorkspaceConfigRequest{
			Id:           workspaceID,
			RawToml:      rawTOML,
			ExpectedHash: expectedHash,
		}))
	if err != nil {
		return Workspace{}, classifyWorkspaceUpdateError(err)
	}
	return workspaceFromProto(res.Msg.GetWorkspace()), nil
}

func classifyWorkspaceUpdateError(err error) error {
	code, _ := WorkspaceCodeOf(err)
	switch code {
	case WorkspaceCodeConfigChanged:
		return fmt.Errorf("update workspace configuration: %w", &WorkspaceConfigConflictError{Cause: err})
	case WorkspaceCodeConfigInvalid:
		msg := ""
		var cerr *connect.Error
		if errors.As(err, &cerr) {
			msg = cerr.Message()
		}
		key, line, col := parseWorkspaceInvalidMessage(msg)
		return fmt.Errorf("update workspace configuration: %w", &WorkspaceConfigValidationError{
			Key:     key,
			Line:    line,
			Column:  col,
			Message: msg,
			Cause:   err,
		})
	default:
		return fmt.Errorf("update workspace configuration: %w", err)
	}
}

func parseWorkspaceInvalidMessage(msg string) (key string, line, col int64) {
	if _, after, ok := strings.Cut(msg, " at key "); ok {
		key, _, _ = strings.Cut(after, " (")
		key = strings.TrimSpace(key)
	}
	line, col = parseLineColumnSuffix(msg)
	return key, line, col
}

func parseLineColumnSuffix(msg string) (int64, int64) {
	_, after, ok := strings.Cut(msg, "(line ")
	if !ok {
		return 0, 0
	}
	lineStr, rest, hasCol := strings.Cut(after, ", column ")
	if !hasCol {
		return 0, 0
	}
	colStr, _, hasEnd := strings.Cut(rest, ")")
	if !hasEnd {
		return 0, 0
	}
	line, lineErr := strconv.ParseInt(strings.TrimSpace(lineStr), 10, 64)
	col, colErr := strconv.ParseInt(strings.TrimSpace(colStr), 10, 64)
	if lineErr != nil || colErr != nil {
		return 0, 0
	}
	return line, col
}

// PatchWorkspaceTOML applies only the non-nil fields of patch onto rawTOML
// while preserving unrelated Tier 3 tables and keys. Re-marshalling drops
// comments and key order (Q-208).
//
// TODO(F10.24): replace with the host-side PatchWorkspaceConfig RPC.
func PatchWorkspaceTOML(rawTOML []byte, patch *WorkspaceSettingsPatch) ([]byte, error) {
	doc := map[string]any{}
	if len(strings.TrimSpace(string(rawTOML))) > 0 {
		if err := toml.Unmarshal(rawTOML, &doc); err != nil {
			return nil, fmt.Errorf("decode workspace TOML: %w", err)
		}
	}
	if patch != nil {
		applyPolicyPatch(doc, patch)
		applySessionPatch(doc, patch)
		applyTeamsPatch(doc, patch)
	}
	out, err := toml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("encode workspace TOML: %w", err)
	}
	return out, nil
}

func applyPolicyPatch(doc map[string]any, patch *WorkspaceSettingsPatch) {
	if patch.ApprovalMode == nil && patch.SandboxProfile == nil && patch.ShellAllow == nil && patch.EnvTags == nil {
		return
	}
	pol := ensureTOMLTable(doc, "policy")
	if patch.ApprovalMode != nil {
		pol["approval_mode"] = *patch.ApprovalMode
	}
	if patch.SandboxProfile != nil {
		pol["sandbox_profile"] = *patch.SandboxProfile
	}
	if patch.ShellAllow != nil {
		pol["shell_allow"] = slices.Clone(*patch.ShellAllow)
		delete(pol, "shell_allow_add")
	}
	if patch.EnvTags != nil {
		rules := make([]map[string]string, 0, len(*patch.EnvTags))
		for _, r := range *patch.EnvTags {
			rules = append(rules, map[string]string{"match": r.Match, "tag": r.Tag})
		}
		pol["env_tags"] = rules
	}
}

func applySessionPatch(doc map[string]any, patch *WorkspaceSettingsPatch) {
	if patch.SessionTeams == nil && patch.TeamsAutoApprove == nil {
		return
	}
	sess := ensureTOMLTable(doc, "session")
	if patch.SessionTeams != nil {
		sess["teams"] = *patch.SessionTeams
	}
	if patch.TeamsAutoApprove != nil {
		sess["teams_auto_approve"] = *patch.TeamsAutoApprove
	}
}

func applyTeamsPatch(doc map[string]any, patch *WorkspaceSettingsPatch) {
	entries := []struct {
		key string
		val *int
	}{
		{"idle_detach_seconds", patch.IdleDetachSeconds},
		{"task_deadline_seconds", patch.TaskDeadlineSeconds},
		{"task_max_turns", patch.TaskMaxTurns},
		{"silence_timeout_seconds", patch.SilenceTimeoutSeconds},
		{"team_deadline_seconds", patch.TeamDeadlineSeconds},
		{"team_max_agents", patch.TeamMaxAgents},
	}
	var teams map[string]any
	for _, e := range entries {
		if e.val == nil {
			continue
		}
		if teams == nil {
			teams = ensureTOMLTable(doc, "teams")
		}
		teams[e.key] = *e.val
	}
}

func ensureTOMLTable(doc map[string]any, section string) map[string]any {
	if existing, ok := doc[section].(map[string]any); ok {
		return existing
	}
	tbl := map[string]any{}
	doc[section] = tbl
	return tbl
}

func extractWorkspaceSettings(values map[string]any) WorkspaceSettings {
	pol := valAt[map[string]any](values, "Policy")
	sess := valAt[map[string]any](values, "Session")
	teams := valAt[map[string]any](values, "Teams")
	return WorkspaceSettings{
		ApprovalMode:          valAt[string](pol, "ApprovalMode"),
		SandboxProfile:        valAt[string](pol, "SandboxProfile"),
		ShellAllow:            strSliceAt(pol, "ShellAllow"),
		EnvTags:               envTagSliceAt(pol, "EnvTags"),
		SessionTeams:          valAt[bool](sess, "Teams"),
		TeamsAutoApprove:      valAt[bool](sess, "TeamsAutoApprove"),
		IdleDetachSeconds:     intAt(teams, "IdleDetachSeconds"),
		TaskDeadlineSeconds:   intAt(teams, "TaskDeadlineSeconds"),
		TaskMaxTurns:          intAt(teams, "TaskMaxTurns"),
		SilenceTimeoutSeconds: intAt(teams, "SilenceTimeoutSeconds"),
		TeamDeadlineSeconds:   intAt(teams, "TeamDeadlineSeconds"),
		TeamMaxAgents:         intAt(teams, "TeamMaxAgents"),
	}
}

func valAt[T any](m map[string]any, key string) T {
	var zero T
	if m == nil {
		return zero
	}
	v, ok := m[key].(T)
	if !ok {
		return zero
	}
	return v
}

func intAt(m map[string]any, key string) int {
	if f, ok := m[key].(float64); ok {
		return int(f)
	}
	if n, ok := m[key].(int64); ok {
		return int(n)
	}
	return valAt[int](m, key)
}

func strSliceAt(m map[string]any, key string) []string {
	raw := valAt[[]any](m, key)
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, it := range raw {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func envTagSliceAt(m map[string]any, key string) []EnvTagRule {
	raw := valAt[[]any](m, key)
	if len(raw) == 0 {
		return nil
	}
	out := make([]EnvTagRule, 0, len(raw))
	for _, it := range raw {
		entry, ok := it.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, EnvTagRule{
			Match: valAt[string](entry, "Match"),
			Tag:   valAt[string](entry, "Tag"),
		})
	}
	return out
}

func workspaceFromProto(w *workspacev1.Workspace) Workspace {
	if w == nil {
		return Workspace{}
	}
	var rawTOML []byte
	if len(w.GetRawToml()) > 0 {
		rawTOML = slices.Clone(w.GetRawToml())
	}
	out := Workspace{
		ID: w.GetId(), Name: w.GetName(), RawTOML: rawTOML,
		ConfigHash: w.GetConfigHash(), ConfigRevision: w.GetConfigRevision(),
		PolicyRevision:            w.GetPolicyRevision(),
		MissingRoots:              w.GetMissingRoots(),
		PendingPolicyConfirmation: w.GetPendingPolicyConfirmation(),
		ConfigErrorKey:            w.GetConfigErrorKey(), ConfigErrorLine: w.GetConfigErrorLine(),
		ConfigErrorColumn: w.GetConfigErrorColumn(),
	}
	for _, r := range w.GetRoots() {
		out.Roots = append(out.Roots, WorkspaceRoot{Name: r.GetName(), Path: r.GetPath(), Access: r.GetAccess(), Missing: r.GetMissing()})
	}
	for _, d := range w.GetPolicyDiff() {
		out.PolicyDiff = append(out.PolicyDiff, PolicyDiffEntry{Key: d.GetKey(), OldValue: d.GetOldValue(), NewValue: d.GetNewValue()})
	}
	return out
}
