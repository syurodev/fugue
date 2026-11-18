package app

import (
	"context"

	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// taskSessionRequest is what the team dispatcher asks the vendor layer for:
// one session bound to a team task, in a host-derived working directory
// (Q-119). Nothing in it comes from a client.
type taskSessionRequest struct {
	SessionID, Vendor, Model, Effort string
	Cwd, WorkspaceID, RootName       string
	TeamID, TaskID                   string
	// Role is the task's hierarchy role; the session token is minted with it
	// (Q-117), so the session principal is the task and nothing wider.
	Role vendorstore.TeamRole
	// ApprovalMode is the Fugue approval mode of the role's permission
	// (Q-120): plan for read, ask for write, never allow-always.
	ApprovalMode string
}

// taskSessions is the port the dispatcher starts and feeds task sessions
// through. The vendor layer implements it for the host; tests use a fake.
type taskSessions interface {
	HasCapacity(ctx context.Context) (bool, error)
	StartTaskSession(ctx context.Context, req *taskSessionRequest) error
	Send(ctx context.Context, sessionID string, input vendor.Input) error
	Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error
}

// vendorTaskSessions adapts the vendor service to taskSessions.
type vendorTaskSessions struct{ svc *VendorService }

func (v vendorTaskSessions) HasCapacity(ctx context.Context) (bool, error) {
	return v.svc.sessions.HasCapacity(ctx)
}

func (v vendorTaskSessions) StartTaskSession(ctx context.Context, req *taskSessionRequest) error {
	return v.svc.startTaskSession(ctx, req)
}

func (v vendorTaskSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	return v.svc.sessions.Send(ctx, sessionID, input)
}

func (v vendorTaskSessions) Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error {
	return v.svc.sessions.Close(ctx, sessionID, reason)
}

// startTaskSession spawns the session of a team task. It is a host-internal
// call, not part of the Host API: only the dispatcher reaches it, after it
// recorded the assignment, so no client can start a team session.
func (s *VendorService) startTaskSession(ctx context.Context, req *taskSessionRequest) error {
	if _, err := s.adapterFor(req.Vendor); err != nil {
		return err
	}
	if err := validateVendorProfileFor(req.Vendor, VendorProfile{Model: req.Model, Effort: req.Effort}); err != nil {
		return err
	}
	if err := s.requireUnlocked(); err != nil {
		return err
	}
	spec := &vendor.StartSpec{
		SessionID: req.SessionID, Cwd: req.Cwd, HostEnv: s.hostEnv, Model: req.Model, Effort: req.Effort, Bridge: s.bridge,
	}
	info := &vendorstore.Session{
		ID: req.SessionID, Vendor: req.Vendor, AccountID: vendorDefaultAccount, Role: string(req.Role),
		TeamID: req.TeamID, TaskID: req.TaskID, ApprovalMode: req.ApprovalMode,
		WorkspaceID: req.WorkspaceID, WorkspaceRoot: req.RootName,
	}
	if err := s.spawn(spec, info.Role, func() error { return s.sessions.Start(ctx, info, spec) }); err != nil {
		return publicVendorError(err)
	}
	return nil
}

// activeSessions counts the sessions that hold a process slot.
func (m *VendorSessions) activeSessions(ctx context.Context) (int, error) {
	sessions, err := m.deps.Store.ListSessions(ctx)
	if err != nil {
		return 0, safeActorError(err)
	}
	active := 0
	for i := range sessions {
		if sessions[i].State != vendorstore.StateClosed && sessions[i].State != vendorstore.StateDetached {
			active++
		}
	}
	return active, nil
}

// HasCapacity reports whether one more session fits under the host limit,
// counting the starts in flight. The team dispatcher asks before it records
// an assignment, because a started task cannot go back to queued (F11.3a).
func (m *VendorSessions) HasCapacity(ctx context.Context) (bool, error) {
	active, err := m.activeSessions(ctx)
	if err != nil {
		return false, err
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return active+m.startReservations < maxVendorSessionsPerHost, nil
}
