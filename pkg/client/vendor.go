package client

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	vendorsv1 "github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/vendors/v1/vendorsv1connect"
	workspacev1 "github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/workspace/v1/workspacev1connect"
)

// The vendor names the wire enum maps to, the same names ListSessions
// reports.
const (
	VendorClaude = "claude"
	VendorCodex  = "codex"
	VendorAGY    = "agy"
)

// SessionProfile holds the only profile values a client may choose (Q-53 A);
// every other profile value stays a host-side default. The host validates the
// values against the vendor's argv allowlist and answers a typed rejection.
type SessionProfile struct {
	// Model is the vendor's model name; empty uses the vendor's default.
	Model string
	// Effort is the vendor's reasoning effort, for example "high"; empty
	// uses the vendor's default.
	Effort string
	// ResumeSessionID resumes a vendor session by its vendor UUID; only
	// StartSession honors it.
	ResumeSessionID string
}

// VendorProbe is what one vendor probe learned. It never carries a path or
// credential material.
type VendorProbe struct {
	Vendor         string
	Version        string
	VersionTested  bool
	VersionWarning string
	// Login is "unknown", "logged_in" or "logged_out".
	Login      string
	Capability Capability
}

// Capability is the vendor adapter's declared contract (vendor-runtime §6):
// what the vendor supports decides what the composer offers.
type Capability struct {
	PerRequestApproval  bool
	BlockAllToolCalls   bool
	Interrupt           bool
	Steer               bool
	Resume              bool
	PartialDeltas       bool
	VendorSlashCommands bool
	DirectQuota         bool
	ModelList           bool
}

// FugueCommand is one `/fugue:` command of the host's registry: it runs at
// the host and never reaches the vendor (vendor-runtime §9).
type FugueCommand struct {
	Name string
	// Available reports whether the host currently offers the command.
	Available bool
	Owner     string
}

// CommandGroup is one group of the slash menu: the vendor's own commands in
// vendor order, or the host's `/fugue:` registry.
type CommandGroup struct {
	Name   string
	Vendor []string
	Fugue  []FugueCommand
}

// Commands is the answer to ListCommands. Revision is the commands revision
// the vendor-sessions topic carries on its events; a caller re-reads when it
// advances.
type Commands struct {
	Groups   []CommandGroup
	Revision uint64
}

// WorkspaceRoot is one root of a workspace.
type WorkspaceRoot struct {
	Name, Path, Access string
	Missing            bool
}

// Workspace is one workspace and its roots. StartSession resolves the
// working directory from a root of this workspace, so the composer reads the
// roots to name one (root_name stays empty when the workspace has exactly
// one).
type Workspace struct {
	ID, Name                           string
	Roots                              []WorkspaceRoot
	MissingRoots                       []string
	PendingPolicyConfirmation          bool
	ConfigErrorKey                     string
	ConfigErrorLine, ConfigErrorColumn int64
}

// WorkspaceSummary is one workspace of the ListWorkspaces read.
type WorkspaceSummary struct {
	ID, Name                  string
	MissingRoots              []string
	PendingPolicyConfirmation bool
}

// ProbeVendors runs the vendors' own version probes and reports what each
// supports. It fails with the connect error the host answered; a locked
// vault does not block it.
func (c *Client) ProbeVendors(ctx context.Context) ([]VendorProbe, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).ProbeVendors(ctx,
		connect.NewRequest(&vendorsv1.ProbeVendorsRequest{}))
	if err != nil {
		return nil, fmt.Errorf("probe the vendors: %w", err)
	}
	out := make([]VendorProbe, 0, len(res.Msg.GetProbes()))
	for _, p := range res.Msg.GetProbes() {
		out = append(out, VendorProbe{
			Vendor: vendorString(p.GetVendor()), Version: p.GetVersion(),
			VersionTested: p.GetVersionTested(), VersionWarning: p.GetVersionWarning(),
			Login: p.GetLogin(), Capability: capabilityFromProto(p.GetCapability()),
		})
	}
	return out, nil
}

// StartSession starts one vendor session in a workspace root (vendor-runtime
// §6). CommandID deduplicates the operation: a retry with the same id and the
// same command attaches to the original one instead of starting a session
// twice. The host resolves the working directory from the workspace root and
// answers typed rejections (workspace mismatch, invalid profile, locked
// vault) as connect errors.
func (c *Client) StartSession(ctx context.Context, commandID, vendor, workspaceID, rootName string, profile SessionProfile) (Session, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).StartSession(ctx,
		connect.NewRequest(&vendorsv1.StartSessionRequest{
			CommandId: commandID, Vendor: vendorProto(vendor),
			WorkspaceId: workspaceID, RootName: rootName, Profile: profile.proto(),
		}))
	if err != nil {
		return Session{}, fmt.Errorf("start the vendor session: %w", err)
	}
	return sessionFromProto(res.Msg.GetSession()), nil
}

// ResumeSession attaches a new process to a detached session. The session
// stays bound to the workspace root it was created in: a caller naming
// another workspace or root is refused, so a composer that does not know the
// binding leaves workspaceID and rootName empty and the host uses the stored
// ones.
func (c *Client) ResumeSession(ctx context.Context, commandID, sessionID, workspaceID, rootName string, profile SessionProfile) (Session, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).ResumeSession(ctx,
		connect.NewRequest(&vendorsv1.ResumeSessionRequest{
			CommandId: commandID, SessionId: sessionID,
			WorkspaceId: workspaceID, RootName: rootName, Profile: profile.proto(),
		}))
	if err != nil {
		return Session{}, fmt.Errorf("resume the vendor session: %w", err)
	}
	return sessionFromProto(res.Msg.GetSession()), nil
}

// Send delivers one user message or slash command to a session that is ready
// (vendor-runtime §9): a command is the whole prompt, and `/fugue:` input is
// rejected before vendor dispatch. CommandID deduplicates the operation the
// same way StartSession does.
func (c *Client) Send(ctx context.Context, commandID, sessionID, text string) error {
	_, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).Send(ctx,
		connect.NewRequest(&vendorsv1.SendRequest{CommandId: commandID, SessionId: sessionID, Text: text}))
	if err != nil {
		return fmt.Errorf("send to the vendor session: %w", err)
	}
	return nil
}

// Steer adds input to the session's running turn. A vendor without the steer
// capability answers a typed rejection.
func (c *Client) Steer(ctx context.Context, commandID, sessionID, text string) error {
	_, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).Steer(ctx,
		connect.NewRequest(&vendorsv1.SteerRequest{CommandId: commandID, SessionId: sessionID, Text: text}))
	if err != nil {
		return fmt.Errorf("steer the vendor session: %w", err)
	}
	return nil
}

// Interrupt asks the session's running turn to stop and reports whether the
// request was delivered inside the caller's deadline. A false Done means the
// stop operation keeps running on the host; the turn's end is published on
// the session's topics either way.
func (c *Client) Interrupt(ctx context.Context, commandID, sessionID string) (bool, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).Interrupt(ctx,
		connect.NewRequest(&vendorsv1.InterruptRequest{CommandId: commandID, SessionId: sessionID}))
	if err != nil {
		return false, fmt.Errorf("interrupt the vendor session: %w", err)
	}
	return res.Msg.GetStatus() == vendorsv1.OperationStatus_OPERATION_STATUS_DONE, nil
}

// ListCommands reads a session's slash commands: the vendor's own group and
// the host's `/fugue:` group (vendor-runtime §9). It re-reads whenever the
// session's commands revision advances on the vendor-sessions topic.
func (c *Client) ListCommands(ctx context.Context, sessionID string) (Commands, error) {
	res, err := vendorsv1connect.NewVendorServiceClient(c.hc, c.base).ListCommands(ctx,
		connect.NewRequest(&vendorsv1.ListCommandsRequest{SessionId: sessionID}))
	if err != nil {
		return Commands{}, fmt.Errorf("list the vendor commands: %w", err)
	}
	out := Commands{Revision: res.Msg.GetCommandsRevision()}
	for _, g := range res.Msg.GetGroups() {
		group := CommandGroup{Name: g.GetName()}
		for _, vc := range g.GetVendorCommands() {
			group.Vendor = append(group.Vendor, vc.GetName())
		}
		for _, fc := range g.GetFugueCommands() {
			group.Fugue = append(group.Fugue, FugueCommand{Name: fc.GetName(), Available: fc.GetAvailable(), Owner: fc.GetOwner()})
		}
		out.Groups = append(out.Groups, group)
	}
	return out, nil
}

// ListWorkspaces reads the workspace list, which the composer's start flow
// chooses a session's workspace from. Workspace management itself is F10.14.
func (c *Client) ListWorkspaces(ctx context.Context) ([]WorkspaceSummary, error) {
	res, err := workspaceClient(c).ListWorkspaces(ctx, connect.NewRequest(&workspacev1.ListWorkspacesRequest{}))
	if err != nil {
		return nil, fmt.Errorf("list the workspaces: %w", err)
	}
	out := make([]WorkspaceSummary, 0, len(res.Msg.GetWorkspaces()))
	for _, w := range res.Msg.GetWorkspaces() {
		out = append(out, WorkspaceSummary{
			ID: w.GetId(), Name: w.GetName(), MissingRoots: w.GetMissingRoots(),
			PendingPolicyConfirmation: w.GetPendingPolicyConfirmation(),
		})
	}
	return out, nil
}

// Workspace reads one workspace and its roots, which StartSession resolves
// the working directory from.
func (c *Client) Workspace(ctx context.Context, id string) (Workspace, error) {
	res, err := workspaceClient(c).GetWorkspace(ctx, connect.NewRequest(&workspacev1.GetWorkspaceRequest{Id: id}))
	if err != nil {
		return Workspace{}, fmt.Errorf("read the workspace: %w", err)
	}
	w := res.Msg.GetWorkspace()
	out := Workspace{
		ID: w.GetId(), Name: w.GetName(), MissingRoots: w.GetMissingRoots(),
		PendingPolicyConfirmation: w.GetPendingPolicyConfirmation(),
		ConfigErrorKey:            w.GetConfigErrorKey(), ConfigErrorLine: w.GetConfigErrorLine(),
		ConfigErrorColumn: w.GetConfigErrorColumn(),
	}
	for _, r := range w.GetRoots() {
		out.Roots = append(out.Roots, WorkspaceRoot{Name: r.GetName(), Path: r.GetPath(), Access: r.GetAccess(), Missing: r.GetMissing()})
	}
	return out, nil
}

// workspaceClient builds the workspace service client on the client's
// dedicated connection.
func workspaceClient(c *Client) workspacev1connect.WorkspaceServiceClient {
	return workspacev1connect.NewWorkspaceServiceClient(c.hc, c.base)
}

// sessionFromProto maps the wire session onto the public shape.
func sessionFromProto(s *vendorsv1.VendorSession) Session {
	return Session{
		ID: s.GetSessionId(), Vendor: vendorString(s.GetVendor()), State: s.GetState(),
		WaitingReason: s.GetWaitingReason(), CreatedAt: s.GetCreatedAt(), UpdatedAt: s.GetUpdatedAt(),
	}
}

// capabilityFromProto maps the wire capability onto the public shape.
func capabilityFromProto(cap *vendorsv1.Capability) Capability {
	return Capability{
		PerRequestApproval: cap.GetPerRequestApproval(), BlockAllToolCalls: cap.GetBlockAllToolCalls(),
		Interrupt: cap.GetInterrupt(), Steer: cap.GetSteer(), Resume: cap.GetResume(),
		PartialDeltas: cap.GetPartialDeltas(), VendorSlashCommands: cap.GetVendorSlashCommands(),
		DirectQuota: cap.GetDirectQuota(), ModelList: cap.GetModelList(),
	}
}

// profileProto maps the public profile onto the wire shape.
func (p SessionProfile) proto() *vendorsv1.SessionProfile {
	return &vendorsv1.SessionProfile{Model: p.Model, Effort: p.Effort, ResumeSessionId: p.ResumeSessionID}
}

// vendorProto maps a vendor name onto the wire enum; an unknown name keeps
// the unspecified vendor, which the host rejects with a typed answer.
func vendorProto(name string) vendorsv1.Vendor {
	switch name {
	case VendorClaude:
		return vendorsv1.Vendor_VENDOR_CLAUDE
	case VendorCodex:
		return vendorsv1.Vendor_VENDOR_CODEX
	case VendorAGY:
		return vendorsv1.Vendor_VENDOR_AGY
	default:
		return vendorsv1.Vendor_VENDOR_UNSPECIFIED
	}
}
