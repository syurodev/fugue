package client

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

// RootAccessRW and RootAccessRO are the access values the host's schema
// accepts for a workspace root.
const (
	RootAccessRW = "rw"
	RootAccessRO = "ro"
)

// The workspace mutation methods. Each fails with the connect error the host
// answered, whose typed detail names the public workspace code (host-api §4);
// WorkspaceCodeOf reads it. A mutation that changes a workspace publishes the
// change on the `workspaces` topic, so callers refresh from the topic and
// from a fresh workspace read, never from the mutation's own answer. Every
// change but Create is a compare-and-swap on the config hash: an expected
// hash that no longer matches is refused with workspace.config_changed, and
// the caller re-reads the workspace before retrying.

// CreateWorkspace creates a workspace by name. The host makes the directory,
// its fugue.toml and its WORKSPACE.md, and validates the name's own rules.
func (c *Client) CreateWorkspace(ctx context.Context, name string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).CreateWorkspace(ctx,
		connect.NewRequest(&workspacev1.CreateWorkspaceRequest{Name: name}))
	if err != nil {
		return fmt.Errorf("create the workspace: %w", err)
	}
	return nil
}

// AddRoot adds a root to the workspace. The path is typed by the user and
// validated by the host: a path that does not exist, is nested in another
// root, sits in the Fugue home or the data directory, or duplicates a name
// comes back as workspace.root_invalid (or the code that names the exact
// rule), which the caller shows inline.
func (c *Client) AddRoot(ctx context.Context, workspaceID, name, path, access, expectedHash string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).AddRoot(ctx,
		connect.NewRequest(&workspacev1.AddRootRequest{
			Id: workspaceID, Name: name, Path: path, Access: access, ExpectedHash: expectedHash,
		}))
	if err != nil {
		return fmt.Errorf("add the root: %w", err)
	}
	return nil
}

// RemoveRoot removes one root by name. Sessions, history and grants the root
// carries are the host's concern (workspace §4.2); the answer only refuses a
// stale hash or an unknown name.
func (c *Client) RemoveRoot(ctx context.Context, workspaceID, name, expectedHash string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).RemoveRoot(ctx,
		connect.NewRequest(&workspacev1.RemoveRootRequest{Id: workspaceID, Name: name, ExpectedHash: expectedHash}))
	if err != nil {
		return fmt.Errorf("remove the root: %w", err)
	}
	return nil
}

// UpdateRoot changes one root's name, path or access in one compare-and-swap.
// A path change is a remove followed by an add at the host (workspace §4.3),
// so grants and seen versions the old path carried do not survive it.
func (c *Client) UpdateRoot(ctx context.Context, workspaceID, name, newName, path, access, expectedHash string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).UpdateRoot(ctx,
		connect.NewRequest(&workspacev1.UpdateRootRequest{
			Id: workspaceID, Name: name, NewName: newName, Path: path, Access: access, ExpectedHash: expectedHash,
		}))
	if err != nil {
		return fmt.Errorf("update the root: %w", err)
	}
	return nil
}

// DeleteWorkspace removes the workspace's Fugue-owned state. ConfirmName must
// repeat the workspace's name exactly; a stale hash or any other name is
// refused, so a deletion never happens on a stale view or a mistyped answer.
func (c *Client) DeleteWorkspace(ctx context.Context, workspaceID, expectedHash, confirmName string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).DeleteWorkspace(ctx,
		connect.NewRequest(&workspacev1.DeleteWorkspaceRequest{
			Id: workspaceID, ExpectedHash: expectedHash, ConfirmName: confirmName,
		}))
	if err != nil {
		return fmt.Errorf("delete the workspace: %w", err)
	}
	return nil
}

// ConfirmWorkspaceConfig applies the pending out-of-band policy change (Q-64)
// whose on-disk hash still matches expectedHash, and bumps the policy
// revision. A file that changed again after the card was drawn is refused
// with workspace.config_changed, so a confirmation never applies a policy the
// user has not seen.
func (c *Client) ConfirmWorkspaceConfig(ctx context.Context, workspaceID, expectedHash string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).ConfirmWorkspaceConfig(ctx,
		connect.NewRequest(&workspacev1.ConfirmWorkspaceConfigRequest{Id: workspaceID, ExpectedHash: expectedHash}))
	if err != nil {
		return fmt.Errorf("confirm the workspace configuration: %w", err)
	}
	return nil
}

// RejectWorkspaceConfig restores the last accepted configuration (Q-64) whose
// on-disk hash still matches expectedHash, discarding the pending change.
func (c *Client) RejectWorkspaceConfig(ctx context.Context, workspaceID, expectedHash string) error {
	_, err := workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base).RejectWorkspaceConfig(ctx,
		connect.NewRequest(&workspacev1.RejectWorkspaceConfigRequest{Id: workspaceID, ExpectedHash: expectedHash}))
	if err != nil {
		return fmt.Errorf("reject the workspace configuration: %w", err)
	}
	return nil
}

// WorkspaceCodeOf returns the public code a failed workspace call was
// refused with, for example workspace.root_invalid, and whether the failure
// carries one. The code rides the connect error detail (host-api §4); a
// hidden or uncoded failure carries no detail and reports none.
func WorkspaceCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

func errorDetailOf(err error) (*hostv1.ErrorDetail, bool) {
	var cerr *connect.Error
	if !errors.As(err, &cerr) {
		return nil, false
	}
	for _, d := range cerr.Details() {
		msg, derr := d.Value()
		if derr != nil {
			continue
		}
		detail, ok := msg.(*hostv1.ErrorDetail)
		if !ok {
			continue
		}
		return detail, true
	}
	return nil, false
}
