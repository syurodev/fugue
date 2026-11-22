package app

import (
	"context"
	"log/slog"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// taskSessionRequest is what the team dispatcher asks the vendor layer for:
// one session bound to a team task, in a host-derived working directory
// (Q-119). Nothing in it comes from a client.
type taskSessionRequest struct {
	SessionID, Vendor, AccountID, Model, Effort string
	Cwd, WorkspaceID, RootName                  string
	TeamID, TaskID                              string
	// Role is the task's hierarchy role; the session token is minted with it
	// (Q-117), so the session principal is the task and nothing wider.
	Role vendorstore.TeamRole
	// ApprovalMode is the Fugue approval mode of the role's permission
	// (Q-120): plan for read, ask for write, never allow-always.
	ApprovalMode string
	// SlotReserved reports that the dispatcher already reserved a scheduler slot
	// for SessionID before recording TaskAssigned.
	SlotReserved bool
}

// taskSessions is the port the dispatcher starts and feeds task sessions
// through, and the wake delivers mail with (Q-123). The vendor layer
// implements it for the host; tests use a fake.
type taskSessions interface {
	HasCapacity(ctx context.Context) (bool, error)
	ReserveSlot(ctx context.Context, sessionID, vendorName, accountID string) (func(), error)
	StartTaskSession(ctx context.Context, req *taskSessionRequest) error
	Send(ctx context.Context, sessionID string, input vendor.Input) error
	SendAsync(ctx context.Context, sessionID string, input vendor.Input, onDone func(error)) error
	// Steer delivers a prompt into the session's running turn (§6.2: the
	// user's own text reaches the agent immediately, busy or not).
	Steer(ctx context.Context, sessionID string, input vendor.Input) error
	// ResumeTaskSession attaches a fresh process to a detached session
	// through its stored handle; the wake resumes before it delivers (Q-123).
	ResumeTaskSession(ctx context.Context, sessionID, cwd string) error
	Interrupt(ctx context.Context, sessionID string) error
	Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error
}

// vendorTaskSessions adapts the vendor service to taskSessions.
type vendorTaskSessions struct{ svc *VendorService }

func (v vendorTaskSessions) HasCapacity(ctx context.Context) (bool, error) {
	return v.svc.sessions.HasCapacity(ctx)
}

func (v vendorTaskSessions) ReserveSlot(ctx context.Context, sessionID, vendorName, accountID string) (func(), error) {
	return v.svc.sessions.ReserveSlot(ctx, sessionID, vendorName, accountID)
}

func (v vendorTaskSessions) StartTaskSession(ctx context.Context, req *taskSessionRequest) error {
	return v.svc.startTaskSession(ctx, req)
}

func (v vendorTaskSessions) Send(ctx context.Context, sessionID string, input vendor.Input) error {
	return v.svc.sessions.Send(ctx, sessionID, input)
}

func (v vendorTaskSessions) Steer(ctx context.Context, sessionID string, input vendor.Input) error {
	return v.svc.sessions.Steer(ctx, sessionID, input)
}

func (v vendorTaskSessions) SendAsync(_ context.Context, sessionID string, input vendor.Input, onDone func(error)) error {
	return v.svc.sessions.SendAsync(sessionID, input, onDone)
}

func (v vendorTaskSessions) ResumeTaskSession(ctx context.Context, sessionID, cwd string) error {
	return v.svc.resumeTaskSession(ctx, sessionID, cwd)
}

func (v vendorTaskSessions) Interrupt(ctx context.Context, sessionID string) error {
	return v.svc.sessions.Interrupt(ctx, sessionID)
}

func (v vendorTaskSessions) Close(ctx context.Context, sessionID string, reason vendor.CloseReason) error {
	return v.svc.sessions.Close(ctx, sessionID, reason)
}

// SetOnSessionResumed registers a hook invoked after a detached session
// resumes successfully (used to recover an unknown lead task).
func (s *VendorService) SetOnSessionResumed(fn func(context.Context, string) error) {
	s.onSessionResumed = fn
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
	if err := s.spawn(spec, info.Role, func() error { return s.sessions.Resume(ctx, sessionID, spec) }); err != nil {
		return err
	}
	s.notifySessionResumed(ctx, sessionID)
	return nil
}

// notifySessionResumed runs the post-resume lead recovery hook without failing a
// live resume if recording the team transition transiently errors.
func (s *VendorService) notifySessionResumed(ctx context.Context, sessionID string) {
	if s.onSessionResumed == nil {
		return
	}
	if err := s.onSessionResumed(ctx, sessionID); err != nil {
		slog.WarnContext(ctx, "recovering resumed lead task failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

func (s *VendorService) resumeProfile(ctx context.Context, info *vendorstore.Session) (model, effort string, err error) {
	if info.TeamID != "" && info.TaskID != "" {
		return s.taskResumeProfile(ctx, info.TeamID, info.TaskID)
	}
	if info.Model != "" || info.Effort != "" {
		return info.Model, info.Effort, nil
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
	_, role, failCode, err := resolveTaskProfile(ctx, s.store, teamID, taskID, task.CatalogRole)
	if err != nil {
		return "", "", err
	}
	switch failCode {
	case failSpecUnreadable:
		return "", "", errcode.New(errcode.FailedPrecondition, "the task spec is not readable")
	case failProfile:
		return "", "", errcode.New(errcode.FailedPrecondition, "task role decision not found")
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
	accountID := req.AccountID
	if accountID == "" {
		accountID = vendorDefaultAccount
	}
	var home vendor.AccountHome
	if s.accounts != nil {
		resolved, ok := s.accounts.AccountHomeFor(req.Vendor, accountID)
		if !ok {
			return errcode.New(errcode.FailedPrecondition, "selected vendor account is not enabled")
		}
		home = resolved
	}
	spec := &vendor.StartSpec{
		SessionID: req.SessionID, Cwd: req.Cwd, Home: home, HostEnv: s.hostEnv, Model: req.Model, Effort: req.Effort, Bridge: s.bridge,
	}
	info := &vendorstore.Session{
		ID: req.SessionID, Vendor: req.Vendor, AccountID: accountID, Role: string(req.Role),
		TeamID: req.TeamID, TaskID: req.TaskID, ApprovalMode: req.ApprovalMode,
		WorkspaceID: req.WorkspaceID, WorkspaceRoot: req.RootName,
	}
	start := func() error { return s.sessions.Start(ctx, info, spec) }
	if req.SlotReserved {
		start = func() error { return s.sessions.StartWithReservation(ctx, info, spec) }
	}
	if err := s.spawn(spec, info.Role, start); err != nil {
		return publicVendorError(err)
	}
	return nil
}

// HasCapacity reports whether one more session fits under the machine-wide limit,
// counting the starts in flight. The team dispatcher asks before it records
// an assignment, because a started task cannot go back to queued (F11.3a).
func (m *VendorSessions) HasCapacity(ctx context.Context) (bool, error) {
	tiers, err := m.schedulerTiers(ctx)
	if err != nil {
		return false, err
	}
	total, _, err := m.slotUsage(ctx)
	if err != nil {
		return false, err
	}
	return total < tiers.MachineWide, nil
}

// HasCapacityFor reports whether one more session for vendorName/accountID fits
// under both the machine-wide and per-account §7 tiers.
func (m *VendorSessions) HasCapacityFor(ctx context.Context, vendorName, accountID string) (bool, error) {
	tiers, err := m.schedulerTiers(ctx)
	if err != nil {
		return false, err
	}
	total, byAccount, err := m.slotUsage(ctx)
	if err != nil {
		return false, err
	}
	key := normalizeSlotKey(vendorName, accountID)
	return total < tiers.MachineWide && byAccount[key] < tiers.PerAccount, nil
}
