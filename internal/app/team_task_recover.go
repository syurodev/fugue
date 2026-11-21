package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/syurodev/fugue/internal/gitops"
	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	recoveryDecisionResume   = "resume"
	recoveryDecisionKeepDiff = "keep_diff"
	recoveryDecisionCancel   = "cancel"
)

type recoveryDecisionPayload struct {
	Reason    string `json:"reason,omitempty"`
	CommitSHA string `json:"commit_sha,omitempty"`
}

// Recover applies a delegator's §12 recovery decision (resume, keep_diff, or
// cancel) to a direct child task in TaskStateUnknown (Q-151).
func (s *teamTaskService) Recover(ctx context.Context, sessionID, taskID, decision, reason string) (string, error) {
	if decision != recoveryDecisionResume && decision != recoveryDecisionKeepDiff && decision != recoveryDecisionCancel {
		return "", errcode.New(errcode.InvalidArgument, "decision must be resume, keep_diff, or cancel")
	}
	cutReason, _ := truncateUTF8(strings.TrimSpace(reason), maxCancelReasonBytes)
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return "", err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return "", err
	}
	if target.State != vendorstore.TaskStateUnknown {
		return "", errcode.New(errcode.FailedPrecondition, "task is not in unknown state")
	}
	switch decision {
	case recoveryDecisionCancel:
		return s.recoverCancel(ctx, sessionID, c, target, cutReason)
	case recoveryDecisionKeepDiff:
		return s.recoverKeepDiff(ctx, sessionID, c, target, cutReason)
	default:
		return s.recoverResume(ctx, sessionID, c, target, cutReason)
	}
}

func (s *teamTaskService) recoverCancel(ctx context.Context, sessionID string, c *callerTeamContext, target *vendorstore.TaskRecord, reason string) (string, error) {
	payload, err := json.Marshal(recoveryDecisionPayload{Reason: reason})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode recovery cancel payload", err)
	}
	if target.HierarchyRole == vendorstore.RoleTeamManager {
		return s.recoverCancelManager(ctx, sessionID, c, target, payload)
	}
	if err := s.appendRecoveryEvent(ctx, sessionID, target.TaskID, recoveryDecisionCancel, "", payload, "", "", nil); err != nil {
		return "", err
	}
	s.invalidateTaskQuestionCard(ctx, c.team.TeamID, target.TaskID)
	descSessions := s.cancelSubtreeDescendants(ctx, sessionID, target.TaskID, payload)
	sessions := make([]string, 0, 1+len(descSessions))
	if target.SessionID != "" {
		sessions = append(sessions, target.SessionID)
	}
	sessions = append(sessions, descSessions...)
	s.closeCancelledSessions(ctx, sessions)
	return "task " + target.TaskID + " recovery cancelled", nil
}

// recoverCancelManager cancels an unknown team manager without cancelling its
// running work (§12, Q-159 A): the manager's non-terminal children are
// re-parented to the lead in the same event projection, and only the
// manager's own session closes. The caller is the lead through the takeover
// grant; the store rejects a non-lead adopter. Mail already queued for the
// manager is re-addressed to the lead in the same transaction, except the
// rows the lead itself sent, so a queued member digest reaches the lead.
func (s *teamTaskService) recoverCancelManager(ctx context.Context, sessionID string, c *callerTeamContext, target *vendorstore.TaskRecord, payload []byte) (string, error) {
	keep := s.mailTheLeadSent(ctx, &c.team, target.TaskID, c.caller.TaskID)
	if err := s.appendRecoveryEvent(ctx, sessionID, target.TaskID, recoveryDecisionCancel, "", payload, c.caller.TaskID, target.TaskID, keep); err != nil {
		return "", err
	}
	s.invalidateTaskQuestionCard(ctx, c.team.TeamID, target.TaskID)
	if target.SessionID != "" {
		s.closeCancelledSessions(ctx, []string{target.SessionID})
	}
	return "task " + target.TaskID + " recovery cancelled; its running children were re-parented to the lead", nil
}

// mailTheLeadSent returns the manager's pending mailbox rows the lead itself
// sent (§12, F11.9): a row whose opened payload names the adopting lead is
// the lead's own message, so it stays behind. The vault is unlocked on the
// delegator's tool path; a row that cannot be opened or attributed is not
// kept, so it moves with the manager's other mail instead of stranding.
func (s *teamTaskService) mailTheLeadSent(ctx context.Context, rec *vendorstore.TeamRecord, managerTaskID, leadTaskID string) []string {
	rows, err := s.store.PendingMailboxRows(ctx, rec.TeamID, managerTaskID)
	if err != nil {
		slog.WarnContext(ctx, "reading the manager's pending mail before the takeover failed", "team_id", rec.TeamID, "task_id", managerTaskID, "error_code", string(errcode.CodeOf(err)))
		return nil
	}
	var keep []string
	for i := range rows {
		raw, openErr := s.store.OpenMailboxPayload(ctx, rec.TeamID, rows[i].MessageID)
		if openErr != nil {
			continue
		}
		var item mailboxDigestItem
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		if item.TaskID == leadTaskID {
			keep = append(keep, rows[i].MessageID)
		}
	}
	return keep
}

func (s *teamTaskService) recoverKeepDiff(ctx context.Context, sessionID string, c *callerTeamContext, target *vendorstore.TaskRecord, reason string) (string, error) {
	if s.git == nil {
		return "", errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	wt, err := s.taskWorktreeDir(ctx, &c.team, target.TaskID)
	if err != nil {
		return "", err
	}
	res, err := s.git.CommitWorktree(ctx, wt, "fugue: recover "+target.TaskID)
	if err != nil {
		return "", boundaryError("commit recovered worktree", err)
	}
	sha := res.SHA
	if sha == "" {
		if sha, err = s.git.WorktreeHead(ctx, wt); err != nil {
			return "", boundaryError("read recovered worktree head", err)
		}
	}
	payload, err := json.Marshal(recoveryDecisionPayload{Reason: reason, CommitSHA: sha})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode keep_diff payload", err)
	}
	if err := s.appendRecoveryEvent(ctx, sessionID, target.TaskID, recoveryDecisionKeepDiff, "", payload, "", "", nil); err != nil {
		return "", err
	}
	s.invalidateTaskQuestionCard(ctx, c.team.TeamID, target.TaskID)
	return "task " + target.TaskID + " diff kept and moved to review", nil
}

func (s *teamTaskService) recoverResume(ctx context.Context, sessionID string, c *callerTeamContext, target *vendorstore.TaskRecord, reason string) (string, error) {
	if s.sessions == nil {
		return "", errcode.New(errcode.FailedPrecondition, "vendor sessions are not configured")
	}
	wt, err := s.taskWorktreeDir(ctx, &c.team, target.TaskID)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(recoveryDecisionPayload{Reason: reason})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode recovery resume payload", err)
	}
	if s.canResumeSession(ctx, target.SessionID) {
		err = s.resumeDetachedTask(ctx, sessionID, c.team.TeamID, target, wt, payload, reason)
	} else {
		err = s.respawnRecoveredTask(ctx, sessionID, &c.team, target, c.tasks, wt, payload, reason)
	}
	if err != nil {
		return "", err
	}
	s.invalidateTaskQuestionCard(ctx, c.team.TeamID, target.TaskID)
	return "task " + target.TaskID + " resumed", nil
}

func (s *teamTaskService) canResumeSession(ctx context.Context, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	info, err := s.store.Snapshot(ctx, sessionID)
	return err == nil && info.State == vendorstore.StateDetached && info.ResumeHandle != ""
}

func (s *teamTaskService) resumeDetachedTask(ctx context.Context, callerSessionID, teamID string, target *vendorstore.TaskRecord, wt string, payload []byte, reason string) error {
	room, err := s.sessions.HasCapacity(ctx)
	if err != nil {
		return boundaryError("check session capacity", err)
	}
	if !room {
		return errcode.New(errcode.VendorCapacity, "no session capacity available to resume task")
	}
	if err := s.appendRecoveryEvent(ctx, callerSessionID, target.TaskID, recoveryDecisionResume, "", payload, "", "", nil); err != nil {
		return err
	}
	if err := s.sessions.ResumeTaskSession(ctx, target.SessionID, wt); err != nil {
		s.failRecoveredTask(ctx, teamID, target.TaskID, vendorstore.TaskStateStarting, target.SessionID, failSessionStart)
		return errcode.Wrap(errcode.CodeOf(err), "resume detached recovery session", err)
	}
	return s.finishRecoveredStart(ctx, teamID, target, target.SessionID, reason, true)
}

func (s *teamTaskService) respawnRecoveredTask(
	ctx context.Context, callerSessionID string, rec *vendorstore.TeamRecord,
	target *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord, wt string, payload []byte, reason string,
) error {
	wt, err := s.ensureWorktreeForRecovery(ctx, rec, target, tasks, wt)
	if err != nil {
		return err
	}
	req, release, err := s.prepareRespawnRequest(ctx, rec, target, wt)
	if err != nil {
		return err
	}
	defer release()
	if target.SessionID != "" {
		s.closeTaskSession(ctx, target.SessionID)
	}
	if err := s.appendRecoveryEvent(ctx, callerSessionID, target.TaskID, recoveryDecisionResume, req.SessionID, payload, "", "", nil); err != nil {
		return err
	}
	if err := s.sessions.StartTaskSession(ctx, req); err != nil {
		s.failRecoveredTask(ctx, rec.TeamID, target.TaskID, vendorstore.TaskStateStarting, req.SessionID, failSessionStart)
		return errcode.Wrap(errcode.CodeOf(err), "start recovery task session", err)
	}
	return s.finishRecoveredStart(ctx, rec.TeamID, target, req.SessionID, reason, false)
}

func (s *teamTaskService) finishRecoveredStart(ctx context.Context, teamID string, target *vendorstore.TaskRecord, sessionID, reason string, resumed bool) error {
	if err := s.markRecoveredTaskStarted(ctx, teamID, target.TaskID, sessionID); err != nil {
		s.closeTaskSession(ctx, sessionID)
		s.failRecoveredTask(ctx, teamID, target.TaskID, vendorstore.TaskStateStarting, sessionID, failSessionStart)
		return err
	}
	s.sendRecoveryPrompt(ctx, teamID, target, sessionID, reason, resumed)
	return nil
}

func (s *teamTaskService) appendRecoveryEvent(ctx context.Context, callerSessionID, taskID, outcome, newSessionID string, payload []byte, reparentTo, readdressFrom string, readdressKeep []string) error {
	return s.appendTeamWithRetry(ctx, callerSessionID, func(_ context.Context, cur *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		curTarget, delErr := cur.requireDelegator(taskID)
		if delErr != nil {
			return nil, delErr
		}
		if curTarget.State != vendorstore.TaskStateUnknown {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not in unknown state")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate recovery event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: curTarget.TaskID, EventType: vendorstore.EventTaskRecoveryDecided,
			Outcome: outcome, SessionID: newSessionID, ReparentTo: reparentTo,
			ReaddressMailboxFrom: readdressFrom, ReaddressMailboxKeep: readdressKeep,
			ProtectedPayload: payload,
		}, nil
	})
}

func (s *teamTaskService) markRecoveredTaskStarted(ctx context.Context, teamID, taskID, sessionID string) error {
	return s.appendTaskEventWithRetry(ctx, teamID, func(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || cur.State != vendorstore.TaskStateStarting {
			return nil, errcode.New(errcode.FailedPrecondition, "task is no longer starting for recovery")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate started event ID", idErr)
		}
		armTimers := buildTaskDeadlineTimers(ctx, s.store, s.teamsCfg, s.now(), rec, cur)
		if s.budgets != nil {
			s.budgets.trackTaskWatch(teamID, cur, vendorstore.TaskStateRunning, sessionID, rec.LastSeq+1, s.now().UnixMilli())
			s.budgets.Notify()
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskStarted, ArmTimers: armTimers,
		}, nil
	})
}

func (s *teamTaskService) failRecoveredTask(ctx context.Context, teamID, taskID string, expectedState vendorstore.TaskState, sessionID, code string) {
	if err := s.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || cur.State != expectedState || (sessionID != "" && cur.SessionID != sessionID) {
			return nil, errcode.New(errcode.FailedPrecondition, "task is no longer in expected recovery state")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate recovery failure event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskEnded,
			Outcome: vendorstore.TaskOutcomeFailed, FailureCode: code,
			Mailbox: digestItemRows(taskID, resolvedParentID(cur.ParentTaskID, tasks), code),
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "failing recovered task failed", "team_id", teamID, "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if s.wake != nil {
		s.wake.Notify()
	}
}

func (s *teamTaskService) ensureWorktreeForRecovery(ctx context.Context, rec *vendorstore.TeamRecord, target *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord, wt string) (string, error) {
	if _, statErr := os.Stat(wt); statErr == nil {
		return wt, nil
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return "", errcode.Wrap(errcode.Unavailable, "inspect task worktree", statErr)
	}
	root, head, err := s.recoveryRepoRoot(ctx, rec.SessionID)
	if err != nil {
		return "", err
	}
	parent, err := parentBranch(target, tasks)
	if err != nil {
		return "", err
	}
	if branchErr := s.git.CreateTeamBranch(ctx, root, rec.TeamID, head); branchErr != nil && errcode.CodeOf(branchErr) != errcode.AlreadyExists {
		return "", boundaryError("create the team branch", branchErr)
	}
	created, err := s.git.CreateTaskWorktree(ctx, s.dataDir, root, repoIDFor(root), gitTaskID(target.TaskID), parent)
	if err != nil {
		return "", boundaryError("create recovery worktree", err)
	}
	return created, nil
}

func (s *teamTaskService) recoveryRepoRoot(ctx context.Context, leadSessionID string) (string, string, error) {
	if s.git == nil || s.roots == nil {
		return "", "", errcode.New(errcode.FailedPrecondition, "gitops is not configured")
	}
	lead, err := s.store.Snapshot(ctx, leadSessionID)
	if err != nil {
		return "", "", boundaryError("read lead session", err)
	}
	root, err := s.roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return "", "", boundaryError("resolve workspace root", err)
	}
	head, err := s.git.WorktreeHead(ctx, root)
	if err != nil || head == "" {
		return "", "", errcode.New(errcode.FailedPrecondition, "task repository has no HEAD commit")
	}
	return root, head, nil
}

func (s *teamTaskService) prepareRespawnRequest(ctx context.Context, rec *vendorstore.TeamRecord, target *vendorstore.TaskRecord, wt string) (*taskSessionRequest, func(), error) {
	_, role, code, err := resolveTaskProfile(ctx, s.store, rec.TeamID, target.TaskID, target.CatalogRole)
	if err != nil {
		return nil, nil, err
	}
	if code != "" {
		return nil, nil, errcode.New(errcode.FailedPrecondition, "task profile is invalid")
	}
	lead, err := s.store.Snapshot(ctx, rec.SessionID)
	if err != nil {
		return nil, nil, boundaryError("read lead session", err)
	}
	newSessionID, err := s.ids.NewID()
	if err != nil {
		return nil, nil, errcode.Wrap(errcode.Internal, "allocate recovery session ID", err)
	}
	release, err := s.sessions.ReserveSlot(ctx, newSessionID, role.Vendor, vendorDefaultAccount)
	if err != nil {
		return nil, nil, boundaryError("reserve recovery session slot", err)
	}
	return &taskSessionRequest{
		SessionID: newSessionID, Vendor: role.Vendor, Model: role.Model, Effort: role.Effort,
		Cwd: wt, WorkspaceID: lead.WorkspaceID, RootName: lead.WorkspaceRoot,
		TeamID: rec.TeamID, TaskID: target.TaskID, Role: target.HierarchyRole,
		ApprovalMode: approvalModeFor(role.Permission), SlotReserved: true,
	}, release, nil
}

func (s *teamTaskService) sendRecoveryPrompt(ctx context.Context, teamID string, target *vendorstore.TaskRecord, sessionID, reason string, resumed bool) {
	mode := "respawned in the existing worktree"
	if resumed {
		mode = "resumed"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Your session for task %s (role %q) was %s after a host restart. Inspect the worktree, continue your task, and call fugue_task_done when finished.", target.TaskID, target.CatalogRole, mode)
	if raw, err := s.store.OpenTaskSpec(ctx, teamID, target.TaskID); err == nil && json.Valid(raw) {
		var spec teams.ProtectedTaskContent
		if json.Unmarshal(raw, &spec) == nil && spec.Goal != "" {
			b.WriteString("\n\nGoal:\n" + spec.Goal)
		}
	}
	if reason != "" {
		b.WriteString("\n\nDelegator recovery note:\n" + reason)
	}
	opCtx := context.WithoutCancel(ctx)
	if err := s.sessions.SendAsync(ctx, sessionID, vendor.Input{Text: b.String(), Origin: vendor.OriginFugue}, func(sendErr error) {
		if sendErr != nil && !errors.Is(sendErr, ErrHostStopping) {
			s.closeTaskSession(opCtx, sessionID)
			s.failRecoveredTask(opCtx, teamID, target.TaskID, vendorstore.TaskStateRunning, sessionID, failIntro)
		}
	}); err != nil {
		s.closeTaskSession(ctx, sessionID)
		s.failRecoveredTask(ctx, teamID, target.TaskID, vendorstore.TaskStateRunning, sessionID, failIntro)
	}
}

func (s *teamTaskService) taskWorktreeDir(ctx context.Context, rec *vendorstore.TeamRecord, taskID string) (string, error) {
	if s.roots == nil {
		return "", errcode.New(errcode.FailedPrecondition, "workspace roots are not configured")
	}
	lead, err := s.store.Snapshot(ctx, rec.SessionID)
	if err != nil {
		return "", boundaryError("read lead session", err)
	}
	root, err := s.roots.SelectSessionCwd(ctx, lead.WorkspaceID, lead.WorkspaceRoot)
	if err != nil {
		return "", boundaryError("resolve workspace root", err)
	}
	wt, err := gitops.TaskWorktreePath(s.dataDir, repoIDFor(root), gitTaskID(taskID))
	if err != nil {
		return "", boundaryError("derive task worktree", err)
	}
	return wt, nil
}

func (s *teamTaskService) ensureAnswerTaskSession(ctx context.Context, rec *vendorstore.TeamRecord, task *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord) (string, bool, error) {
	if s.canResumeSession(ctx, task.SessionID) || s.sessions == nil {
		return "", false, nil
	}
	if task.SessionID != "" {
		if info, err := s.store.Snapshot(ctx, task.SessionID); err == nil && info.State == vendorstore.StateReady {
			return "", false, nil
		}
	}
	wt, err := s.taskWorktreeDir(ctx, rec, task.TaskID)
	if err != nil {
		return "", false, err
	}
	if wt, err = s.ensureWorktreeForRecovery(ctx, rec, task, tasks, wt); err != nil {
		return "", false, err
	}
	req, release, err := s.prepareRespawnRequest(ctx, rec, task, wt)
	if err != nil {
		return "", false, err
	}
	defer release()
	if task.SessionID != "" {
		s.closeTaskSession(ctx, task.SessionID)
	}
	if err := s.sessions.StartTaskSession(ctx, req); err != nil {
		return "", false, errcode.Wrap(errcode.CodeOf(err), "start recovery answer session", err)
	}
	return req.SessionID, true, nil
}

func (s *teamTaskService) trackAnsweredWatches(ctx context.Context, teamID string, evs []*vendorstore.AppendTeamEventInput) {
	if s.budgets == nil {
		return
	}
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return
	}
	var updated bool
	for _, ev := range evs {
		if task, ok := findTask(tasks, ev.TaskID); ok && task.State == vendorstore.TaskStateRunning {
			s.budgets.trackTaskWatch(teamID, task, vendorstore.TaskStateRunning, task.SessionID, rec.LastSeq, s.now().UnixMilli())
			updated = true
		}
	}
	if updated {
		s.budgets.Notify()
	}
}

func (s *teamTaskService) invalidateTaskQuestionCard(ctx context.Context, teamID, taskID string) {
	if s.interactions == nil || s.interactions.owner == nil {
		return
	}
	_, seq, found, err := s.store.OpenLatestTaskEventPayloadWithSeq(ctx, teamID, taskID, vendorstore.EventTaskQuestionAsked)
	if err != nil || !found || seq <= 0 {
		return
	}
	if opID, opErr := interaction.NativeOperationKey(teamID, taskID, uint64(seq)); opErr == nil {
		if _, invErr := s.interactions.owner.InvalidateOperation(opID); invErr != nil {
			slog.WarnContext(ctx, "invalidating recovered task question card failed", "task_id", taskID, "error_code", string(errcode.CodeOf(invErr)))
		}
	}
}
