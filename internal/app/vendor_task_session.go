package app

import (
	"context"
	"encoding/json"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
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
// through, and the wake delivers mail with (Q-123). The vendor layer
// implements it for the host; tests use a fake.
type taskSessions interface {
	HasCapacity(ctx context.Context) (bool, error)
	StartTaskSession(ctx context.Context, req *taskSessionRequest) error
	Send(ctx context.Context, sessionID string, input vendor.Input) error
	// ResumeTaskSession attaches a fresh process to a detached session
	// through its stored handle; the wake resumes before it delivers (Q-123).
	ResumeTaskSession(ctx context.Context, sessionID, cwd string) error
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

func (v vendorTaskSessions) ResumeTaskSession(ctx context.Context, sessionID, cwd string) error {
	return v.svc.resumeTaskSession(ctx, sessionID, cwd)
}

func (v vendorTaskSessions) Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error {
	return v.svc.sessions.Close(ctx, sessionID, reason)
}

// resumeTaskSession attaches a fresh process to a detached session through
// its stored handle (Q-123). Task sessions restore the decided role profile
// and task override from the sealed spec (OR-E13); non-task sessions (such as
// the proposing lead) restore their last recorded model if any.
func (s *VendorService) resumeTaskSession(ctx context.Context, sessionID, cwd string) error {
	info, err := s.store.Snapshot(ctx, sessionID)
	if err != nil {
		return safeActorError(err)
	}
	if info.State != vendorstore.StateDetached {
		return errcode.New(errcode.FailedPrecondition, "vendor session is not resumable")
	}
	model, effort, err := s.resumeProfile(ctx, &info)
	if err != nil {
		return err
	}
	spec := &vendor.StartSpec{
		SessionID: sessionID, Cwd: cwd, HostEnv: s.hostEnv, Model: model, Effort: effort, Bridge: s.bridge,
	}
	return s.spawn(spec, info.Role, func() error { return s.sessions.Resume(ctx, sessionID, spec) })
}

func (s *VendorService) resumeProfile(ctx context.Context, info *vendorstore.Session) (model, effort string, err error) {
	if info.TeamID != "" && info.TaskID != "" {
		return s.taskResumeProfile(ctx, info.TeamID, info.TaskID)
	}
	items, err := s.store.Transcript(ctx, info.ID, 0, 100)
	if err != nil {
		return "", "", safeActorError(err)
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Metadata.Model != "" {
			return items[i].Metadata.Model, "", nil
		}
	}
	return "", "", nil
}

func (s *VendorService) taskResumeProfile(ctx context.Context, teamID, taskID string) (model, effort string, err error) {
	_, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return "", "", boundaryError("read the task team", err)
	}
	var task *vendorstore.TaskRecord
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		return "", "", errcode.New(errcode.FailedPrecondition, "team task not found")
	}
	raw, err := s.store.OpenTaskSpec(ctx, teamID, taskID)
	if err != nil {
		return "", "", boundaryError("open the task spec", err)
	}
	if !json.Valid(raw) {
		return "", "", errcode.New(errcode.FailedPrecondition, "the task spec is not readable")
	}
	var taskSpec teams.ProtectedTaskContent
	if decodeErr := json.Unmarshal(raw, &taskSpec); decodeErr != nil {
		return "", "", errcode.Wrap(errcode.Internal, "decode the task spec", decodeErr)
	}
	role, found, err := decidedRoleFor(ctx, s.store, teamID, task.CatalogRole)
	if err != nil {
		return "", "", err
	}
	if !found {
		return "", "", errcode.New(errcode.FailedPrecondition, "task role decision not found")
	}
	if taskSpec.Profile != nil {
		overlayProfile(&role, taskSpec.Profile)
	}
	return role.Model, role.Effort, nil
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
