package app

import (
	"context"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/process"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const defaultIdleDetachSeconds = 300

// Detach cleanly stops the process of an idle session while keeping its resume
// handle and moving its state from ready to detached (Q-148).
func (m *VendorSessions) Detach(ctx context.Context, sessionID string) error {
	actor := m.actor(sessionID)
	if actor == nil {
		return vendorstore.ErrSessionNotFound
	}
	if err := actor.call(ctx, actorCommand{kind: commandDetach}); err != nil {
		return err
	}
	select {
	case <-actor.done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for vendor session detach: %w", ctx.Err())
	}
}

// TouchSession resets a live session's idle-detach timer on activity such as
// MCP tool calls or queued prompts.
func (m *VendorSessions) TouchSession(sessionID string) {
	if actor := m.actor(sessionID); actor != nil {
		actor.touch()
	}
}

func (m *VendorSessions) hasPendingPacedSend(vendorName, sessionID string) bool {
	if m.pacer == nil {
		return false
	}
	return m.pacer.HasPendingForSession(vendorName, sessionID)
}

func (m *VendorSessions) vendorSupportsResume(ctx context.Context, vendorName string) bool {
	m.capMu.Lock()
	if capView, ok := m.capabilities[vendorName]; ok {
		m.capMu.Unlock()
		return capView.Resume
	}
	m.capMu.Unlock()

	ad, err := m.adapterFor(vendorName)
	if err != nil {
		return false
	}
	var capView vendor.Capability
	if direct, ok := ad.(interface{ Capability() vendor.Capability }); ok {
		capView = direct.Capability()
	} else {
		probe, probeErr := ad.Probe(ctx, vendor.AccountHome{})
		if probeErr != nil {
			return false
		}
		capView = probe.Capability
	}
	m.capMu.Lock()
	m.capabilities[vendorName] = capView
	m.capMu.Unlock()
	return capView.Resume
}

func (m *VendorSessions) idleDetachDuration(ctx context.Context, workspaceID string) (time.Duration, bool) {
	secs := defaultIdleDetachSeconds
	if m.deps.TeamsConfig != nil {
		cfg, err := m.deps.TeamsConfig(ctx, workspaceID)
		if err != nil {
			return 0, false
		}
		secs = cfg.IdleDetachSeconds
	}
	if secs <= 0 {
		return 0, false
	}
	return time.Duration(secs) * time.Second, true
}

func (a *vendorSessionActor) touch() {
	select {
	case a.touchCh <- struct{}{}:
	default:
	}
}

func (a *vendorSessionActor) stopIdleTimer() {
	if a.idleTimer != nil {
		a.idleTimer.Stop()
		a.idleTimer = nil
	}
	a.idleCh = nil
}

func (a *vendorSessionActor) resetIdleTimer(ctx context.Context) {
	a.stopIdleTimer()
	if !a.autoDetachEligible(ctx) {
		return
	}
	dur, ok := a.manager.idleDetachDuration(ctx, a.info.WorkspaceID)
	if !ok {
		return
	}
	a.idleTimer = a.manager.deps.Clock.NewTimer(dur)
	a.idleCh = a.idleTimer.C()
}

func (a *vendorSessionActor) autoDetachEligible(ctx context.Context) bool {
	if a.finished || a.detaching || a.requestedCloseReason() != "" {
		return false
	}
	if a.state.Kind != vendor.StateReady || a.turnID != "" || len(a.turns) > 0 {
		return false
	}
	if a.info.Role != string(vendorstore.RoleTeamManager) && a.info.Role != string(vendorstore.RoleMember) {
		return false
	}
	// TODO(F11.6d): allow agy auto-detach after F11.6d measures agy idle-send and session/load.
	if a.info.Vendor == VendorAGY {
		return false
	}
	if a.info.ResumeHandle == "" {
		return false
	}
	return a.manager.vendorSupportsResume(ctx, a.info.Vendor)
}

func (a *vendorSessionActor) handleIdleTimeout(ctx context.Context) {
	a.stopIdleTimer()
	if !a.canAutoDetachNow(ctx) {
		a.resetIdleTimer(ctx)
		return
	}
	if err := a.startDetach(); err != nil {
		a.resetIdleTimer(ctx)
	}
}

func (a *vendorSessionActor) canAutoDetachNow(ctx context.Context) bool {
	if !a.autoDetachEligible(ctx) {
		return false
	}
	if a.manager.hasPendingPacedSend(a.info.Vendor, a.info.ID) {
		return false
	}
	if a.manager.deps.HasPendingApproval != nil && a.manager.deps.HasPendingApproval(a.info.ID) {
		return false
	}
	return !a.isTaskAsking(ctx)
}

func (a *vendorSessionActor) isTaskAsking(ctx context.Context) bool {
	if a.info.TaskID == "" {
		return false
	}
	reader, ok := a.manager.deps.Store.(interface {
		TaskForSession(context.Context, string) (vendorstore.TaskRecord, bool, error)
	})
	if !ok {
		return false
	}
	task, found, err := reader.TaskForSession(ctx, a.info.ID)
	if err != nil {
		// Fail closed: if we cannot read task state, do not detach.
		return true
	}
	return found && task.State == vendorstore.TaskStateAsking
}

func (a *vendorSessionActor) startDetach() error {
	if a.requestedCloseReason() != "" || a.detaching || a.finished {
		return errcode.New(errcode.FailedPrecondition, "vendor session is already closing or detached")
	}
	if a.state.Kind != vendor.StateReady || a.turnID != "" || len(a.turns) > 0 {
		return errcode.New(errcode.FailedPrecondition, "vendor session is not idle")
	}
	if a.manager.hasPendingPacedSend(a.info.Vendor, a.info.ID) {
		return errcode.New(errcode.FailedPrecondition, "vendor session has a pending paced prompt")
	}
	if _, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactDetach}); err != nil {
		return errcode.New(errcode.FailedPrecondition, "vendor session state does not allow detach")
	}
	a.stopIdleTimer()
	a.detaching = true
	a.manager.spawn("detach-vendor-session-"+a.info.ID, func(stopCtx context.Context) error {
		proof, err := a.session.Close(context.WithoutCancel(stopCtx), vendor.CloseHostStop)
		select {
		case a.detachResults <- actorCloseResult{proof: proof, err: err}:
		case <-a.done:
		}
		return nil
	})
	return nil
}

func (a *vendorSessionActor) persistDetach(ctx context.Context, proof process.StopProof, closeErr error) error {
	if err := a.awaitSessionEndWork(ctx); err != nil {
		return err
	}
	status := string(proof.Status)
	if status != string(process.StopProofProved) || closeErr != nil {
		status = string(process.StopProofUnknown)
	}
	next, _, err := vendor.Transition(a.state, vendor.Fact{Kind: vendor.FactDetach})
	if err != nil {
		return fmt.Errorf("record detach vendor session state: %w", err)
	}
	writeCtx := context.WithoutCancel(ctx)
	if err := a.append(writeCtx, &vendorstore.Event{Type: vendorstore.VendorProcessExited, StopProof: status}); err != nil {
		return err
	}
	if err := a.append(writeCtx, &vendorstore.Event{Type: vendorstore.VendorSessionStateChanged, State: vendorstore.StateDetached}); err != nil {
		return err
	}
	a.state = next
	a.attached = false
	a.finished = true
	return nil
}
