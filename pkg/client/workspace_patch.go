package client

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
)

var (
	errPatchInputRequired = errors.New("patch workspace configuration: input is required")
	errUnsupportedPatchOp = errors.New("unsupported config patch op")
)

// ConfigPatchOp identifies a structured workspace config patch operation.
type ConfigPatchOp uint8

// Supported structured workspace config patch operations.
const (
	ConfigPatchSet ConfigPatchOp = iota + 1
	ConfigPatchDelete
)

// ConfigPatch describes one field-level update or deletion for PatchWorkspaceConfig.
type ConfigPatch struct {
	DottedKey string
	Op        ConfigPatchOp
	Value     any
}

// PatchWorkspaceConfigInput describes one structured PatchWorkspaceConfig RPC call.
type PatchWorkspaceConfigInput struct {
	CommandID    string
	WorkspaceID  string
	ExpectedHash string
	Patch        *WorkspaceSettingsPatch
	Patches      []ConfigPatch
}

// PatchWorkspaceConfig applies structured field-level updates on the host while
// preserving comments and key order, using CAS on ExpectedHash.
func (c *Client) PatchWorkspaceConfig(ctx context.Context, in *PatchWorkspaceConfigInput) (Workspace, error) {
	if in == nil {
		return Workspace{}, errPatchInputRequired
	}
	pbPatches, err := buildProtoConfigPatches(in.Patch, in.Patches)
	if err != nil {
		return Workspace{}, fmt.Errorf("patch workspace configuration: %w", err)
	}
	res, err := workspaceClient(c).PatchWorkspaceConfig(ctx,
		connect.NewRequest(&workspacev1.PatchWorkspaceConfigRequest{
			CommandId:    in.CommandID,
			WorkspaceId:  in.WorkspaceID,
			ExpectedHash: in.ExpectedHash,
			Patches:      pbPatches,
		}))
	if err != nil {
		return Workspace{}, classifyWorkspaceUpdateError(err)
	}
	return workspaceFromProto(res.Msg.GetWorkspace()), nil
}

func buildProtoConfigPatches(patch *WorkspaceSettingsPatch, extra []ConfigPatch) ([]*workspacev1.ConfigPatch, error) {
	var out []*workspacev1.ConfigPatch
	if patch != nil {
		out = appendSettingsPatches(out, patch)
	}
	for _, p := range extra {
		pb, err := toProtoConfigPatch(p)
		if err != nil {
			return nil, err
		}
		out = append(out, pb)
	}
	return out, nil
}

func appendSettingsPatches(out []*workspacev1.ConfigPatch, patch *WorkspaceSettingsPatch) []*workspacev1.ConfigPatch {
	if patch.ApprovalMode != nil {
		out = append(out, setStringPatch("policy.approval_mode", *patch.ApprovalMode))
	}
	if patch.SandboxProfile != nil {
		out = append(out, setStringPatch("policy.sandbox_profile", *patch.SandboxProfile))
	}
	if patch.ShellAllow != nil {
		vals := make([]*structpb.Value, 0, len(*patch.ShellAllow))
		for _, s := range *patch.ShellAllow {
			vals = append(vals, structpb.NewStringValue(s))
		}
		out = append(out, &workspacev1.ConfigPatch{
			DottedKey: "policy.shell_allow",
			Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET,
			Value:     structpb.NewListValue(&structpb.ListValue{Values: vals}),
		}, &workspacev1.ConfigPatch{
			DottedKey: "policy.shell_allow_add",
			Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_DELETE,
		})
	}
	if patch.EnvTags != nil {
		vals := make([]*structpb.Value, 0, len(*patch.EnvTags))
		for _, r := range *patch.EnvTags {
			vals = append(vals, structpb.NewStructValue(&structpb.Struct{Fields: map[string]*structpb.Value{
				"match": structpb.NewStringValue(r.Match),
				"tag":   structpb.NewStringValue(r.Tag),
			}}))
		}
		out = append(out, &workspacev1.ConfigPatch{
			DottedKey: "policy.env_tags",
			Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET,
			Value:     structpb.NewListValue(&structpb.ListValue{Values: vals}),
		})
	}
	if patch.SessionTeams != nil {
		out = append(out, setBoolPatch("session.teams", *patch.SessionTeams))
	}
	if patch.TeamsAutoApprove != nil {
		out = append(out, setBoolPatch("session.teams_auto_approve", *patch.TeamsAutoApprove))
	}
	return appendTeamsSettingsPatches(out, patch)
}

func appendTeamsSettingsPatches(out []*workspacev1.ConfigPatch, patch *WorkspaceSettingsPatch) []*workspacev1.ConfigPatch {
	entries := []struct {
		key string
		val *int
	}{
		{"teams.idle_detach_seconds", patch.IdleDetachSeconds},
		{"teams.task_deadline_seconds", patch.TaskDeadlineSeconds},
		{"teams.task_max_turns", patch.TaskMaxTurns},
		{"teams.silence_timeout_seconds", patch.SilenceTimeoutSeconds},
		{"teams.team_deadline_seconds", patch.TeamDeadlineSeconds},
		{"teams.team_max_agents", patch.TeamMaxAgents},
	}
	for _, e := range entries {
		if e.val != nil {
			out = append(out, &workspacev1.ConfigPatch{
				DottedKey: e.key,
				Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET,
				Value:     structpb.NewNumberValue(float64(*e.val)),
			})
		}
	}
	return out
}

func setStringPatch(key, val string) *workspacev1.ConfigPatch {
	return &workspacev1.ConfigPatch{
		DottedKey: key,
		Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET,
		Value:     structpb.NewStringValue(val),
	}
}

func setBoolPatch(key string, val bool) *workspacev1.ConfigPatch {
	return &workspacev1.ConfigPatch{
		DottedKey: key,
		Op:        workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET,
		Value:     structpb.NewBoolValue(val),
	}
}

func toProtoConfigPatch(p ConfigPatch) (*workspacev1.ConfigPatch, error) {
	pb := &workspacev1.ConfigPatch{DottedKey: p.DottedKey}
	switch p.Op {
	case ConfigPatchSet:
		pb.Op = workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_SET
		val, err := structpb.NewValue(p.Value)
		if err != nil {
			return nil, fmt.Errorf("encode value for %s: %w", p.DottedKey, err)
		}
		pb.Value = val
	case ConfigPatchDelete:
		pb.Op = workspacev1.ConfigPatchOp_CONFIG_PATCH_OP_DELETE
	default:
		return nil, fmt.Errorf("%w for %s", errUnsupportedPatchOp, p.DottedKey)
	}
	return pb, nil
}
