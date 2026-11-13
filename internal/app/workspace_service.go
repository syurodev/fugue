package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"slices"
	"strconv"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
)

const workspaceOperationFailedMessage = "workspace operation failed"

// TopicWorkspaces carries workspace summaries and lifecycle/configuration events.
const TopicWorkspaces = "workspaces"

// Workspace lifecycle event names and the maximum config payload size.
const (
	EventWorkspaceCreated        = "created"
	EventWorkspaceConfigChanged  = "config_changed"
	EventWorkspacePendingConfirm = "pending_confirmation"
	EventWorkspaceConfirmed      = "confirmed"
	EventWorkspaceRejected       = "rejected"
	EventWorkspaceRootAdded      = "root_added"
	EventWorkspaceRootRemoved    = "root_removed"
	EventWorkspaceRootUpdated    = "root_updated"
	EventWorkspaceDeleted        = "deleted"
	WorkspaceConfigMaxBytes      = 256 * 1024
)

// WorkspaceSummary is the safe snapshot shape for the workspaces topic.
type WorkspaceSummary struct {
	ID                        string   `json:"id"`
	Name                      string   `json:"name"`
	MissingRoots              []string `json:"missing_roots"`
	ConfigRevision            int64    `json:"config_revision"`
	PendingPolicyConfirmation bool     `json:"pending_policy_confirmation"`
}

// WorkspaceConfigIssue contains only safe validation metadata, never a value.
type WorkspaceConfigIssue struct {
	Key, Message string
	Line, Column int
}

// WorkspaceRoot and WorkspaceView keep domain types behind the app boundary.
type WorkspaceRoot struct {
	Name, Path, Access string
	Missing            bool
}

// WorkspacePolicyDiff describes one reviewed policy change.
type WorkspacePolicyDiff struct{ Key, OldValue, NewValue string }

// WorkspaceView is the app boundary representation of workspace metadata.
type WorkspaceView struct {
	ID, Name, ConfigHash, CreatedAt    string
	Roots                              []WorkspaceRoot
	MissingRoots                       []string
	ConfigRevision, PolicyRevision     int64
	PendingPolicyConfirmation          bool
	PolicyDiff                         []WorkspacePolicyDiff
	ConfigErrorKey                     string
	ConfigErrorLine, ConfigErrorColumn int64
}

// EffectiveWorkspaceConfig contains merged values and their source tiers.
type EffectiveWorkspaceConfig struct {
	Values     map[string]any
	Provenance map[string]int64
}

// WorkspaceBackend is implemented by HostRuntime and accessed by the Host API
// without adding workspace calls to the backend fakes used by other services.
type WorkspaceBackend interface {
	CreateWorkspace(context.Context, string, string) (*WorkspaceView, error)
	ListWorkspaces(context.Context, string) ([]*WorkspaceView, error)
	GetWorkspace(context.Context, string, string) (*WorkspaceView, []byte, error)
	GetEffectiveWorkspaceConfig(context.Context, string, string) (EffectiveWorkspaceConfig, *WorkspaceView, error)
	ValidateWorkspaceConfig(context.Context, string, []byte) (WorkspaceConfigIssue, error)
	UpdateWorkspaceConfig(context.Context, string, string, []byte, string) (*WorkspaceView, error)
	ConfirmWorkspaceConfig(context.Context, string, string, string) (*WorkspaceView, error)
	RejectWorkspaceConfig(context.Context, string, string, string) (*WorkspaceView, error)
	AddWorkspaceRoot(context.Context, string, string, string, string, string, string) (*WorkspaceView, error)
	RemoveWorkspaceRoot(context.Context, string, string, string, string) (*WorkspaceView, error)
	UpdateWorkspaceRoot(context.Context, string, string, string, string, string, string, string) (*WorkspaceView, error)
	DeleteWorkspace(context.Context, string, string, string, string) error
}

// WorkspaceService composes workspace domain operations with the ephemeral topic.
type WorkspaceService struct {
	domain *workspace.Service
	hub    *EventHub
}

// NewWorkspaceService binds the domain service to the app event hub.
func NewWorkspaceService(domain *workspace.Service, hub *EventHub) (*WorkspaceService, error) {
	if domain == nil || hub == nil {
		return nil, errcode.New(errcode.InvalidArgument, "workspace service dependencies are required")
	}
	s := &WorkspaceService{domain: domain, hub: hub}
	if err := hub.RegisterEphemeral(TopicWorkspaces, s.snapshot, defaultEphemeralBufferCap); err != nil {
		return nil, err
	}
	return s, nil
}

// Create creates a workspace for a local principal and publishes its summary.
func (s *WorkspaceService) Create(ctx context.Context, principal, name string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.CreateWorkspace(ctx, workspace.CreateWorkspaceRequest{Name: name})
	if err == nil {
		s.publish(EventWorkspaceCreated, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// List returns workspace metadata for a local principal.
func (s *WorkspaceService) List(ctx context.Context, principal string) ([]*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	items, err := s.domain.ListWorkspaces(ctx)
	if err != nil {
		return nil, publicWorkspaceError(err)
	}
	out := make([]*WorkspaceView, 0, len(items))
	for _, item := range items {
		out = append(out, workspaceView(item))
	}
	return out, nil
}

// Get returns workspace metadata and its bounded raw TOML for a local principal.
func (s *WorkspaceService) Get(ctx context.Context, principal, id string) (*WorkspaceView, []byte, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, nil, err
	}
	ws, err := s.domain.GetWorkspace(ctx, id)
	if err != nil {
		return nil, nil, publicWorkspaceError(err)
	}
	if ws.PendingPolicyConfirmation {
		s.publish(EventWorkspacePendingConfirm, ws)
	}
	raw, err := os.ReadFile(s.domain.Paths().ConfigPath(id))
	if err != nil {
		return nil, nil, errcode.New(errcode.WorkspaceConfigInvalid, "workspace configuration could not be read")
	}
	if len(raw) > WorkspaceConfigMaxBytes {
		return nil, nil, errcode.New(errcode.WorkspaceConfigTooLarge, "workspace configuration exceeds 256 KiB")
	}
	return workspaceView(ws), raw, nil
}

// Effective returns the merged config and provenance for a local principal.
func (s *WorkspaceService) Effective(ctx context.Context, principal, id string) (EffectiveWorkspaceConfig, *WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return EffectiveWorkspaceConfig{}, nil, err
	}
	eff, ws, err := s.domain.GetEffectiveConfig(ctx, id, nil)
	if err == nil && ws.PendingPolicyConfirmation {
		s.publish(EventWorkspacePendingConfirm, ws)
	}
	if err != nil {
		return EffectiveWorkspaceConfig{}, nil, publicWorkspaceError(err)
	}
	data, err := json.Marshal(eff)
	if err != nil {
		return EffectiveWorkspaceConfig{}, nil, errcode.Wrap(errcode.Internal, "encode effective workspace configuration", err)
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return EffectiveWorkspaceConfig{}, nil, errcode.Wrap(errcode.Internal, "decode effective workspace configuration", err)
	}
	delete(values, "Provenance")
	provenance := make(map[string]int64, len(eff.Provenance))
	for key, tier := range eff.Provenance {
		provenance[key] = int64(tier)
	}
	return EffectiveWorkspaceConfig{Values: values, Provenance: provenance}, workspaceView(ws), nil
}

// Validate checks TOML without writing it and returns safe source metadata.
func (s *WorkspaceService) Validate(ctx context.Context, principal string, raw []byte) (WorkspaceConfigIssue, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return WorkspaceConfigIssue{}, err
	}
	if len(raw) > WorkspaceConfigMaxBytes {
		return WorkspaceConfigIssue{}, errcode.New(errcode.WorkspaceConfigTooLarge, "workspace configuration exceeds 256 KiB")
	}
	err := workspace.ValidateConfig(workspace.TierWorkspace, raw)
	if err == nil {
		return WorkspaceConfigIssue{}, nil
	}
	var domainErr *workspace.Error
	if errors.As(err, &domainErr) && domainErr.Code == workspace.ErrConfigInvalid {
		issue := &WorkspaceConfigIssue{Key: domainErr.Key, Message: "invalid configuration"}
		issue.Line, issue.Column, _ = domainErr.Position()
		return *issue, nil
	}
	return WorkspaceConfigIssue{}, publicWorkspaceError(err)
}

// Update applies TOML using a compare-and-swap hash and publishes a change.
func (s *WorkspaceService) Update(ctx context.Context, principal, id string, raw []byte, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	if len(raw) > WorkspaceConfigMaxBytes {
		return nil, errcode.New(errcode.WorkspaceConfigTooLarge, "workspace configuration exceeds 256 KiB")
	}
	ws, err := s.domain.UpdateWorkspaceConfig(ctx, id, raw, expectedHash)
	if err == nil {
		s.publish(EventWorkspaceConfigChanged, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// Confirm applies a reviewed policy change only if its hash still matches.
func (s *WorkspaceService) Confirm(ctx context.Context, principal, id, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.ConfirmWorkspaceConfig(ctx, id, expectedHash)
	if err == nil {
		s.publish(EventWorkspaceConfirmed, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// Reject restores the accepted TOML only if its reviewed hash still matches.
func (s *WorkspaceService) Reject(ctx context.Context, principal, id, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.RejectWorkspaceConfig(ctx, id, expectedHash)
	if err == nil {
		s.publish(EventWorkspaceRejected, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// AddRoot adds a root and publishes the effective metadata change.
func (s *WorkspaceService) AddRoot(ctx context.Context, principal, id, name, path, access, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.AddRoot(ctx, id, expectedHash, workspace.RootSpec{Name: name, Path: path, Access: workspace.RootAccess(access)})
	if err == nil {
		s.publish(EventWorkspaceRootAdded, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// RemoveRoot removes a root and publishes the effective metadata change.
func (s *WorkspaceService) RemoveRoot(ctx context.Context, principal, id, name, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.RemoveRoot(ctx, id, expectedHash, name)
	if err == nil {
		s.publish(EventWorkspaceRootRemoved, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// UpdateRoot changes a root and publishes the effective metadata change.
func (s *WorkspaceService) UpdateRoot(ctx context.Context, principal, id, name, newName, path, access, expectedHash string) (*WorkspaceView, error) {
	if err := authorizeWorkspace(principal); err != nil {
		return nil, err
	}
	ws, err := s.domain.UpdateRoot(ctx, id, expectedHash, name, workspace.RootSpec{Name: newName, Path: path, Access: workspace.RootAccess(access)})
	if err == nil {
		s.publish(EventWorkspaceRootUpdated, ws)
	}
	return workspaceView(ws), publicWorkspaceError(err)
}

// Delete removes only Fugue-owned workspace state and announces a committed row deletion.
func (s *WorkspaceService) Delete(ctx context.Context, principal, id, expectedHash, confirmName string) error {
	if err := authorizeWorkspace(principal); err != nil {
		return err
	}
	ws, err := s.domain.DeleteWorkspace(ctx, id, expectedHash, confirmName)
	if ws != nil {
		s.publish(EventWorkspaceDeleted, ws)
	}
	return publicWorkspaceError(err)
}

func authorizeWorkspace(principal string) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may access workspaces")
	}
	return nil
}

func (s *WorkspaceService) snapshot(ctx context.Context) ([]byte, error) {
	items, err := s.domain.ListWorkspaces(ctx)
	if err != nil {
		return nil, publicWorkspaceError(err)
	}
	summaries := make([]WorkspaceSummary, 0, len(items))
	for _, ws := range items {
		summaries = append(summaries, workspaceSummary(ws))
	}
	data, err := json.Marshal(summaries)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "marshal workspace summaries", err)
	}
	return data, nil
}

func (s *WorkspaceService) publish(event string, ws *workspace.Workspace) {
	data, err := json.Marshal(workspaceSummary(ws))
	if err != nil {
		return
	}
	if _, err := s.hub.Publish(TopicWorkspaces, Event{Type: event, Key: ws.ID, Payload: data}); err != nil {
		// The hub is registered with this service; a publish error is an internal invariant failure.
		slog.Warn("workspace event publish failed", "event", event, "workspace_id", ws.ID, "err", err.Error())
	}
}

func workspaceSummary(ws *workspace.Workspace) WorkspaceSummary {
	return WorkspaceSummary{ID: ws.ID, Name: ws.Name, MissingRoots: ws.MissingRoots, ConfigRevision: ws.ConfigRevision, PendingPolicyConfirmation: ws.PendingPolicyConfirmation}
}

func workspaceView(ws *workspace.Workspace) *WorkspaceView {
	if ws == nil {
		return nil
	}
	out := &WorkspaceView{ID: ws.ID, Name: ws.Name, ConfigHash: ws.ConfigHash, CreatedAt: ws.CreatedAt, MissingRoots: append([]string(nil), ws.MissingRoots...), ConfigRevision: ws.ConfigRevision, PolicyRevision: ws.PolicyRevision, PendingPolicyConfirmation: ws.PendingPolicyConfirmation}
	for _, root := range ws.Roots {
		out.Roots = append(out.Roots, WorkspaceRoot{Name: root.Name, Path: root.Path, Access: string(root.Access), Missing: slices.Contains(ws.MissingRoots, root.Name)})
	}
	for _, diff := range ws.PolicyDiff {
		out.PolicyDiff = append(out.PolicyDiff, WorkspacePolicyDiff{Key: diff.Key, OldValue: diff.OldValue, NewValue: diff.NewValue})
	}
	var configErr *workspace.Error
	if errors.As(ws.ConfigError, &configErr) {
		out.ConfigErrorKey, out.ConfigErrorLine, out.ConfigErrorColumn = configErr.Key, int64(configErr.Line), int64(configErr.Column)
	}
	return out
}

func publicWorkspaceError(err error) error {
	if err == nil {
		return nil
	}
	var domainErr *workspace.Error
	if !errors.As(err, &domainErr) {
		return errcode.Wrap(errcode.Internal, "workspace operation failed", err)
	}
	code, message := workspaceErrorCode(domainErr.Code)
	if domainErr.Code == workspace.ErrConfigInvalid {
		message = "workspace configuration is invalid"
		if domainErr.Key != "" {
			message += " at key " + domainErr.Key
		}
		if domainErr.Line > 0 && domainErr.Column > 0 {
			message += " (line " + strconv.Itoa(domainErr.Line) + ", column " + strconv.Itoa(domainErr.Column) + ")"
		}
	}
	return errcode.New(code, message)
}

func workspaceErrorCode(code workspace.Code) (errcode.Code, string) {
	switch code {
	case workspace.ErrNotFound:
		return errcode.WorkspaceNotFound, "workspace was not found"
	case workspace.ErrAlreadyExists:
		return errcode.WorkspaceAlreadyExists, "workspace already exists"
	case workspace.ErrConfigInvalid:
		return errcode.WorkspaceConfigInvalid, "workspace configuration is invalid"
	case workspace.ErrConfigChanged:
		return errcode.WorkspaceConfigChanged, "workspace configuration changed since it was reviewed"
	case workspace.ErrConfigUnconfirmed:
		return errcode.WorkspaceConfigUnconfirmed, "workspace configuration requires confirmation"
	case workspace.ErrRootMissing:
		return errcode.WorkspaceRootMissing, "workspace root is missing"
	case workspace.ErrRootInvalid:
		return errcode.WorkspaceRootInvalid, "workspace root is invalid"
	case workspace.ErrPathDenied:
		return errcode.WorkspacePathDenied, "workspace path is not allowed"
	case workspace.ErrOwnershipInvalid:
		return errcode.WorkspaceOwnershipInvalid, "workspace ownership or permissions are invalid"
	case workspace.ErrIDInvalid, workspace.ErrConfirmationRequired, workspace.ErrDeleteFailed:
		return additionalWorkspaceErrorCode(code)
	default:
		return errcode.Internal, workspaceOperationFailedMessage
	}
}

func additionalWorkspaceErrorCode(code workspace.Code) (errcode.Code, string) {
	switch code {
	case workspace.ErrIDInvalid:
		return errcode.WorkspaceNotFound, "workspace was not found"
	case workspace.ErrConfirmationRequired:
		return errcode.WorkspaceConfigUnconfirmed, "workspace deletion requires matching confirmation"
	case workspace.ErrDeleteFailed:
		return errcode.WorkspaceOwnershipInvalid, "workspace state could not be safely removed"
	case workspace.ErrNotFound, workspace.ErrAlreadyExists, workspace.ErrConfigInvalid,
		workspace.ErrConfigChanged, workspace.ErrConfigUnconfirmed, workspace.ErrRootInvalid,
		workspace.ErrRootMissing, workspace.ErrPathDenied, workspace.ErrOwnershipInvalid:
		return errcode.Internal, workspaceOperationFailedMessage
	default:
		return errcode.Internal, workspaceOperationFailedMessage
	}
}
