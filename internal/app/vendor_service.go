package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendoradapter/claude"
	"github.com/syurodev/fugue/internal/vendorstore"
	"github.com/syurodev/fugue/internal/workspace"
)

// Host maxima for vendor RPCs (Q-56 A). A caller's shorter deadline wins; a
// longer one is cut to these.
const (
	VendorSpawnLimit     = 35 * time.Second
	VendorCloseLimit     = 30 * time.Second
	VendorInterruptLimit = 12 * time.Second
	VendorUnaryLimit     = 5 * time.Second
	// VendorRequestMaxBytes bounds one vendor RPC request on the wire.
	VendorRequestMaxBytes = 1 << 20
)

// VendorClaude is the only vendor stage A serves.
const VendorClaude = "claude"

// The host-side identity of a stage-A session: the vendor's default account
// home (F9.10 adds account selection) and the solo assistant role.
const (
	vendorDefaultAccount = "default"
	vendorSessionRole    = "assistant"
)

// VendorCapability is the adapter's declared contract, as the Host API shows it.
type VendorCapability = vendor.Capability

// VendorProfile holds the client-selectable profile values (Q-53 A).
type VendorProfile struct{ Model, Effort, ResumeSessionID string }

// VendorSessionView is the safe list shape of one session.
type VendorSessionView struct {
	ID            string `json:"session_id"`
	Vendor        string `json:"vendor"`
	State         string `json:"state"`
	WaitingReason string `json:"waiting_reason,omitempty"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// VendorProbeView is one probe result without path or credential material.
type VendorProbeView struct {
	Vendor, Version, VersionWarning, Login string
	VersionTested                          bool
	Capability                             VendorCapability
}

// StartVendorSession and ResumeVendorSession name the working directory by
// workspace and root; the host resolves the path (Q-57, F5.6).
type StartVendorSession struct {
	CommandID, Vendor, WorkspaceID, RootName string
	Profile                                  VendorProfile
}

// ResumeVendorSession starts a new process on a detached session's handle.
type ResumeVendorSession struct {
	CommandID, SessionID, WorkspaceID, RootName string
	Profile                                     VendorProfile
}

// VendorInput is one Send or Steer command.
type VendorInput struct{ CommandID, SessionID, Text string }

// VendorStop is one Interrupt or Close command.
type VendorStop struct{ CommandID, SessionID string }

// VendorService is the Host API boundary of vendor sessions (F9.9c). It
// authorizes local principals, validates every client value before spawn and
// maps internal failures to typed public codes whose messages never carry
// vendor output, prompts or paths.
type VendorService struct {
	sessions   *VendorSessions
	store      *vendorstore.Store
	adapter    vendor.Adapter
	vault      *vault.Service
	workspaces *workspace.Service
	ids        ids.Generator
	hostEnv    []string
	ops        *vendorOps
}

// Probe reports each requested vendor; an empty list means Claude.
func (s *VendorService) Probe(ctx context.Context, principal string, vendors []string) ([]VendorProbeView, error) {
	if err := authorizeVendor(principal); err != nil {
		return nil, err
	}
	if len(vendors) == 0 {
		vendors = []string{VendorClaude}
	}
	for _, name := range vendors {
		if err := requireClaude(name); err != nil {
			return nil, err
		}
	}
	ctx, cancel := context.WithTimeout(ctx, VendorSpawnLimit)
	defer cancel()
	probe, err := s.adapter.Probe(ctx, vendor.AccountHome{})
	if err != nil {
		return nil, publicVendorError(err)
	}
	return []VendorProbeView{{
		Vendor: VendorClaude, Version: probe.Version, VersionTested: probe.VersionTested,
		VersionWarning: probe.VersionWarning, Login: string(probe.Login), Capability: probe.Capability,
	}}, nil
}

// Start validates the vendor, profile and workspace root, then spawns a session.
func (s *VendorService) Start(ctx context.Context, principal string, cmd *StartVendorSession) (VendorSessionView, error) {
	if err := authorizeVendor(principal); err != nil {
		return VendorSessionView{}, err
	}
	if err := requireClaude(cmd.Vendor); err != nil {
		return VendorSessionView{}, err
	}
	if err := validateVendorProfile(cmd.Profile); err != nil {
		return VendorSessionView{}, err
	}
	canonical := []string{"start", cmd.Vendor, cmd.WorkspaceID, cmd.RootName, cmd.Profile.Model, cmd.Profile.Effort, cmd.Profile.ResumeSessionID}
	return s.runForView(ctx, cmd.CommandID, canonical, VendorSpawnLimit, func(opCtx context.Context) (VendorSessionView, error) {
		spec, root, err := s.spawnSpec(opCtx, cmd.WorkspaceID, cmd.RootName, cmd.Profile)
		if err != nil {
			return VendorSessionView{}, err
		}
		if spec.SessionID, err = s.ids.NewID(); err != nil {
			return VendorSessionView{}, errcode.Wrap(errcode.Internal, "allocate a vendor session identity", err)
		}
		info := &vendorstore.Session{
			ID: spec.SessionID, Vendor: VendorClaude, AccountID: vendorDefaultAccount, Role: vendorSessionRole,
			WorkspaceID: cmd.WorkspaceID, WorkspaceRoot: root,
		}
		if err := s.sessions.Start(opCtx, info, spec); err != nil {
			return VendorSessionView{}, publicVendorError(err)
		}
		return s.view(opCtx, spec.SessionID)
	})
}

// Resume attaches a new process to a detached session's stored handle.
func (s *VendorService) Resume(ctx context.Context, principal string, cmd *ResumeVendorSession) (VendorSessionView, error) {
	if err := authorizeVendor(principal); err != nil {
		return VendorSessionView{}, err
	}
	if cmd.Profile.ResumeSessionID != "" {
		return VendorSessionView{}, errcode.New(errcode.VendorInvalidProfile, "resume uses the session's stored handle")
	}
	if err := validateVendorProfile(cmd.Profile); err != nil {
		return VendorSessionView{}, err
	}
	canonical := []string{"resume", cmd.SessionID, cmd.WorkspaceID, cmd.RootName, cmd.Profile.Model, cmd.Profile.Effort}
	return s.runForView(ctx, cmd.CommandID, canonical, VendorSpawnLimit, func(opCtx context.Context) (VendorSessionView, error) {
		workspaceID, root, err := s.boundWorkspace(opCtx, cmd)
		if err != nil {
			return VendorSessionView{}, err
		}
		spec, _, err := s.spawnSpec(opCtx, workspaceID, root, cmd.Profile)
		if err != nil {
			return VendorSessionView{}, err
		}
		spec.SessionID = cmd.SessionID
		if err := s.sessions.Resume(opCtx, cmd.SessionID, spec); err != nil {
			return VendorSessionView{}, publicVendorError(err)
		}
		return s.view(opCtx, cmd.SessionID)
	})
}

// boundWorkspace returns the workspace root the session was created in. A
// session never moves: a client that names another workspace or root is
// refused before anything spawns.
func (s *VendorService) boundWorkspace(ctx context.Context, cmd *ResumeVendorSession) (string, string, error) {
	info, err := s.store.Snapshot(ctx, cmd.SessionID)
	if err != nil {
		return "", "", publicVendorError(err)
	}
	mismatch := info.WorkspaceID == "" ||
		(cmd.WorkspaceID != "" && cmd.WorkspaceID != info.WorkspaceID) ||
		(cmd.RootName != "" && cmd.RootName != info.WorkspaceRoot)
	if mismatch {
		return "", "", errcode.New(errcode.VendorWorkspaceMismatch, "the session is bound to the workspace root it was created in")
	}
	return info.WorkspaceID, info.WorkspaceRoot, nil
}

// Send starts a turn. A /fugue: input, leading whitespace ignored, is
// rejected before vendor dispatch: stage A registers no host command (Q-54 A).
func (s *VendorService) Send(ctx context.Context, principal string, cmd VendorInput) error {
	return s.input(ctx, principal, "send", cmd, s.sessions.Send)
}

// Steer adds input to the running turn under the same /fugue: rule.
func (s *VendorService) Steer(ctx context.Context, principal string, cmd VendorInput) error {
	return s.input(ctx, principal, "steer", cmd, s.sessions.Steer)
}

func (s *VendorService) input(ctx context.Context, principal, kind string, cmd VendorInput,
	deliver func(context.Context, string, vendor.Input) error,
) error {
	if err := authorizeVendor(principal); err != nil {
		return err
	}
	if commandErr := fugueCommandError(cmd.Text); commandErr != nil {
		return commandErr
	}
	_, err := s.runForView(ctx, cmd.CommandID, []string{kind, cmd.SessionID, cmd.Text}, VendorUnaryLimit, func(opCtx context.Context) (VendorSessionView, error) {
		if err := s.requireUnlocked(); err != nil {
			return VendorSessionView{}, err
		}
		return VendorSessionView{}, publicVendorError(deliver(opCtx, cmd.SessionID, vendor.Input{Text: cmd.Text}))
	})
	return err
}

// Interrupt asks the running turn to stop; done reports that the request was
// delivered, and the turn's end is published on the session topics.
func (s *VendorService) Interrupt(ctx context.Context, principal string, cmd VendorStop) (bool, error) {
	if err := authorizeVendor(principal); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, VendorInterruptLimit)
	defer cancel()
	_, done, err := s.ops.run(ctx, cmd.CommandID, []string{"interrupt", cmd.SessionID}, VendorInterruptLimit, func(opCtx context.Context) (VendorSessionView, error) {
		return VendorSessionView{}, publicVendorError(s.sessions.Interrupt(opCtx, cmd.SessionID))
	})
	return done, err
}

// Close stops a session; done=false means the stop order is still running.
func (s *VendorService) Close(ctx context.Context, principal string, cmd VendorStop) (bool, error) {
	if err := authorizeVendor(principal); err != nil {
		return false, err
	}
	ctx, cancel := context.WithTimeout(ctx, VendorCloseLimit)
	defer cancel()
	_, done, err := s.ops.run(ctx, cmd.CommandID, []string{"close", cmd.SessionID}, VendorCloseLimit, func(opCtx context.Context) (VendorSessionView, error) {
		return VendorSessionView{}, publicVendorError(s.sessions.Close(opCtx, cmd.SessionID, vendor.CloseUserRequest))
	})
	return done, err
}

// List returns every session and the vendor-sessions sequence of the list.
func (s *VendorService) List(ctx context.Context, principal string) ([]VendorSessionView, EventSeq, error) {
	if err := authorizeVendor(principal); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, VendorUnaryLimit)
	defer cancel()
	sessions, last, err := s.store.SessionList(ctx)
	if err != nil {
		return nil, 0, publicVendorError(err)
	}
	seq, err := storeSeq(last)
	if err != nil {
		return nil, 0, err
	}
	out := make([]VendorSessionView, 0, len(sessions))
	for i := range sessions {
		out = append(out, vendorSessionView(&sessions[i]))
	}
	return out, seq, nil
}

// runForView waits at most limit for the deduplicated operation. A caller
// that stops waiting gets deadline_exceeded and may retry with the same
// command_id to attach to the still-running operation.
func (s *VendorService) runForView(ctx context.Context, commandID string, canonical []string, limit time.Duration,
	fn func(context.Context) (VendorSessionView, error),
) (VendorSessionView, error) {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	view, done, err := s.ops.run(ctx, commandID, canonical, limit, fn)
	if err != nil {
		return VendorSessionView{}, err
	}
	if !done {
		return VendorSessionView{}, errcode.New(errcode.DeadlineExceeded, "the vendor operation is still running; retry with the same command_id")
	}
	return view, nil
}

// spawnSpec resolves the working directory and returns the spec with the
// resolved root name, which the session records.
func (s *VendorService) spawnSpec(ctx context.Context, workspaceID, rootName string, profile VendorProfile) (*vendor.StartSpec, string, error) {
	if err := s.requireUnlocked(); err != nil {
		return nil, "", err
	}
	// The workspace re-validates the root on every load, so the Q-57
	// rejections (filesystem root, data directory, Fugue home) hold for the
	// resolved directory too.
	cwd, err := s.workspaces.SelectSessionCwd(ctx, workspaceID, rootName)
	if err != nil {
		return nil, "", publicWorkspaceError(err)
	}
	if rootName == "" {
		// SelectSessionCwd accepted an empty name, so there is exactly one root.
		ws, getErr := s.workspaces.GetWorkspace(ctx, workspaceID)
		if getErr != nil || len(ws.Roots) != 1 {
			return nil, "", errcode.New(errcode.WorkspaceRootInvalid, "the workspace root could not be resolved")
		}
		rootName = ws.Roots[0].Name
	}
	return &vendor.StartSpec{
		Cwd: cwd, HostEnv: s.hostEnv, Model: profile.Model, Effort: profile.Effort, ResumeSessionID: profile.ResumeSessionID,
	}, rootName, nil
}

func (s *VendorService) requireUnlocked() error {
	if s.vault.Snapshot().State != vault.StateUnlocked {
		return errcode.New(errcode.VendorVaultLocked, "the vault is locked")
	}
	return nil
}

func (s *VendorService) view(ctx context.Context, sessionID string) (VendorSessionView, error) {
	info, err := s.store.Snapshot(ctx, sessionID)
	if err != nil {
		return VendorSessionView{}, publicVendorError(err)
	}
	return vendorSessionView(&info), nil
}

func authorizeVendor(principal string) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may use vendor sessions")
	}
	return nil
}

func requireClaude(name string) error {
	if name != VendorClaude {
		return errcode.New(errcode.VendorUnsupported, "the vendor is not supported yet")
	}
	return nil
}

// validateVendorProfile reuses the vetted Claude argv allowlist, so a value
// that would not reach argv never reaches spawn either. The value is never
// echoed back.
func validateVendorProfile(profile VendorProfile) error {
	spec := &vendor.StartSpec{Model: profile.Model, Effort: profile.Effort, ResumeSessionID: profile.ResumeSessionID}
	if _, err := claude.BuildArgv(claude.DefaultBinary, spec); err != nil {
		return errcode.New(errcode.VendorInvalidProfile, "a session profile value is not allowed")
	}
	return nil
}

func vendorSessionView(info *vendorstore.Session) VendorSessionView {
	return VendorSessionView{
		ID: info.ID, Vendor: info.Vendor, State: info.State, WaitingReason: info.WaitingReason,
		CreatedAt: info.CreatedAt, UpdatedAt: info.UpdatedAt,
	}
}

// publicVendorError maps actor, store and adapter failures onto typed public
// codes with fixed messages; nothing from the vendor process passes through.
func publicVendorError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vendorstore.ErrSessionNotFound):
		return errcode.New(errcode.VendorSessionNotFound, "the vendor session is not live")
	case errors.Is(err, ErrVendorSessionCapacity):
		return errcode.New(errcode.VendorCapacity, "the host already runs the maximum number of vendor sessions")
	case errors.Is(err, vendor.ErrTurnNotRunning):
		return errcode.New(errcode.VendorTurnNotActive, "no vendor turn is running")
	}
	code := errcode.CodeOf(err)
	switch {
	case strings.HasPrefix(string(code), "vendor.") || strings.HasPrefix(string(code), "workspace."):
		return err
	case code == errcode.FailedPrecondition:
		return errcode.New(errcode.VendorSessionState, "the vendor session state does not allow the operation")
	case code.IsPublic():
		return errcode.New(code, "the vendor session operation failed")
	default:
		return errcode.New(errcode.Internal, "the vendor session operation failed")
	}
}
