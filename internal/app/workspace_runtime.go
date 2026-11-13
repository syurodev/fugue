package app

import (
	"context"
)

// CreateWorkspace serves the CreateWorkspace Host API RPC.
func (rt *HostRuntime) CreateWorkspace(ctx context.Context, principal, name string) (*WorkspaceView, error) {
	return rt.workspaces.Create(ctx, principal, name)
}

// ListWorkspaces serves the ListWorkspaces Host API RPC.
func (rt *HostRuntime) ListWorkspaces(ctx context.Context, principal string) ([]*WorkspaceView, error) {
	return rt.workspaces.List(ctx, principal)
}

// GetWorkspace serves the GetWorkspace Host API RPC.
func (rt *HostRuntime) GetWorkspace(ctx context.Context, principal, id string) (*WorkspaceView, []byte, error) {
	return rt.workspaces.Get(ctx, principal, id)
}

// GetEffectiveWorkspaceConfig serves the GetEffectiveConfig Host API RPC.
func (rt *HostRuntime) GetEffectiveWorkspaceConfig(ctx context.Context, principal, id string) (EffectiveWorkspaceConfig, *WorkspaceView, error) {
	return rt.workspaces.Effective(ctx, principal, id)
}

// ValidateWorkspaceConfig serves the ValidateConfig Host API RPC.
func (rt *HostRuntime) ValidateWorkspaceConfig(ctx context.Context, principal string, raw []byte) (WorkspaceConfigIssue, error) {
	return rt.workspaces.Validate(ctx, principal, raw)
}

// UpdateWorkspaceConfig serves the UpdateWorkspaceConfig Host API RPC.
func (rt *HostRuntime) UpdateWorkspaceConfig(ctx context.Context, principal, id string, raw []byte, hash string) (*WorkspaceView, error) {
	return rt.workspaces.Update(ctx, principal, id, raw, hash)
}

// ConfirmWorkspaceConfig serves the ConfirmWorkspaceConfig Host API RPC.
func (rt *HostRuntime) ConfirmWorkspaceConfig(ctx context.Context, principal, id, hash string) (*WorkspaceView, error) {
	return rt.workspaces.Confirm(ctx, principal, id, hash)
}

// RejectWorkspaceConfig serves the RejectWorkspaceConfig Host API RPC.
func (rt *HostRuntime) RejectWorkspaceConfig(ctx context.Context, principal, id, hash string) (*WorkspaceView, error) {
	return rt.workspaces.Reject(ctx, principal, id, hash)
}
