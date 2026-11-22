package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vault"
	"github.com/syurodev/fugue/internal/vendorstore"
)

// RecoverTaskCommand carries one RecoverTask call (§5.3).
type RecoverTaskCommand struct {
	CommandID string
	TeamID    string
	TaskID    string
	Decision  string
	Reason    string
}

type recoverTaskDedupEntry struct {
	node        TaskNodeView
	fingerprint string
}

// RecoverTask applies a local user's recovery decision (resume, keep_diff, or
// cancel) to an unknown task with command_id deduplication (§5.3).
func (s *TasksService) RecoverTask(ctx context.Context, cmd *RecoverTaskCommand) (TaskNodeView, error) {
	if err := validateRecoverCommand(cmd); err != nil {
		return TaskNodeView{}, err
	}
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return TaskNodeView{}, errcode.New(errcode.Unavailable, "the vault is locked; task recovery is unavailable")
	}
	fp := recoverTaskFingerprint(cmd)
	if cached, known, conflict := s.lookupRecoverDedup(cmd.CommandID, fp); conflict {
		return TaskNodeView{}, errcode.New(errcode.InvalidArgument, "command_id was already used for a different command")
	} else if known {
		return cached, nil
	}
	if s.teamTasks == nil {
		return TaskNodeView{}, errcode.New(errcode.Unavailable, "team recovery service is unavailable")
	}
	if err := s.teamTasks.RecoverByUser(ctx, cmd.TeamID, cmd.TaskID, cmd.Decision, cmd.Reason); err != nil {
		return TaskNodeView{}, err
	}
	node, err := s.readRecoveredTaskNode(ctx, cmd.TeamID, cmd.TaskID)
	if err != nil {
		return TaskNodeView{}, err
	}
	s.rememberRecoverDedup(cmd.CommandID, fp, &node)
	return node, nil
}

func validateRecoverCommand(cmd *RecoverTaskCommand) error {
	if cmd == nil || cmd.CommandID == "" {
		return errcode.New(errcode.InvalidArgument, "command_id is required")
	}
	if cmd.TeamID == "" {
		return errcode.New(errcode.TasksTeamNotFound, "team_id is required")
	}
	if cmd.TaskID == "" {
		return errcode.New(errcode.TasksTaskNotFound, "task_id is required")
	}
	if !validRecoveryDecision(cmd.Decision) {
		return errcode.New(errcode.InvalidArgument, "decision must be resume, keep_diff, or cancel")
	}
	return nil
}

func (s *TasksService) readRecoveredTaskNode(ctx context.Context, teamID, taskID string) (TaskNodeView, error) {
	_, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		return TaskNodeView{}, s.teamReadError(err)
	}
	task, ok := findTask(tasks, taskID)
	if !ok {
		return TaskNodeView{}, errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
	}
	lastSeqs, err := s.store.TeamTaskLastChannelSeqs(ctx, teamID)
	if err != nil {
		return TaskNodeView{}, boundaryError("read task channel sequences", err)
	}
	node, _ := s.buildTaskNode(ctx, teamID, task, lastSeqs, nil, false)
	return node, nil
}

// RecoverByUser applies a local user's §12 recovery decision (resume,
// keep_diff, or cancel) to a task in TaskStateUnknown (host-api v0.19 §5.3).
func (s *teamTaskService) RecoverByUser(ctx context.Context, teamID, taskID, decision, reason string) error {
	if !validRecoveryDecision(decision) {
		return errcode.New(errcode.InvalidArgument, "decision must be resume, keep_diff, or cancel")
	}
	cutReason, _ := truncateUTF8(strings.TrimSpace(reason), maxCancelReasonBytes)
	rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
	if err != nil {
		if errors.Is(err, vendorstore.ErrTeamNotFound) {
			return errcode.New(errcode.TasksTeamNotFound, "the team does not exist")
		}
		return boundaryError("read the team", err)
	}
	target, ok := findTask(tasks, taskID)
	if !ok {
		return errcode.New(errcode.TasksTaskNotFound, "the team task does not exist in this team")
	}
	if target.State != vendorstore.TaskStateUnknown {
		return errcode.New(errcode.TasksTaskNotUnknown, "the team task is not in unknown state")
	}
	var leadTaskID string
	for i := range tasks {
		if tasks[i].HierarchyRole == vendorstore.RoleLead {
			leadTaskID = tasks[i].TaskID
			break
		}
	}
	_, err = s.executeRecovery(ctx, "", &rec, leadTaskID, target, tasks, decision, cutReason)
	return err
}

func validRecoveryDecision(decision string) bool {
	return decision == recoveryDecisionResume || decision == recoveryDecisionKeepDiff || decision == recoveryDecisionCancel
}

func (s *teamTaskService) executeRecovery(
	ctx context.Context, callerSessionID string, rec *vendorstore.TeamRecord, leadTaskID string,
	target *vendorstore.TaskRecord, tasks []vendorstore.TaskRecord, decision, cutReason string,
) (string, error) {
	switch decision {
	case recoveryDecisionCancel:
		return s.recoverCancel(ctx, callerSessionID, rec, leadTaskID, target, cutReason)
	case recoveryDecisionKeepDiff:
		return s.recoverKeepDiff(ctx, callerSessionID, rec, target, cutReason)
	default:
		return s.recoverResume(ctx, callerSessionID, rec, target, tasks, cutReason)
	}
}

func recoverTaskFingerprint(cmd *RecoverTaskCommand) string {
	h := sha256.New()
	var lenBuf [20]byte
	for _, part := range []string{cmd.TeamID, cmd.TaskID, cmd.Decision, strings.TrimSpace(cmd.Reason)} {
		_, _ = h.Write(strconv.AppendInt(lenBuf[:0], int64(len(part)), 10))
		_, _ = h.Write([]byte(":"))
		_, _ = h.Write([]byte(part))
		_, _ = h.Write([]byte("\n"))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func (s *TasksService) lookupRecoverDedup(commandID, fp string) (TaskNodeView, bool, bool) {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	entry, ok := s.recoverDedup[commandID]
	if !ok {
		return TaskNodeView{}, false, false
	}
	if entry.fingerprint != fp {
		return TaskNodeView{}, true, true
	}
	return entry.node, true, false
}

func (s *TasksService) rememberRecoverDedup(commandID, fp string, node *TaskNodeView) {
	s.dedupMu.Lock()
	defer s.dedupMu.Unlock()
	if s.recoverDedup == nil {
		s.recoverDedup = make(map[string]recoverTaskDedupEntry, 16)
	}
	if _, ok := s.recoverDedup[commandID]; !ok {
		s.recoverDedupOrder = append(s.recoverDedupOrder, commandID)
		for len(s.recoverDedupOrder) > tasksDedupCapacity {
			oldest := s.recoverDedupOrder[0]
			s.recoverDedupOrder = s.recoverDedupOrder[1:]
			delete(s.recoverDedup, oldest)
		}
	}
	s.recoverDedup[commandID] = recoverTaskDedupEntry{node: *node, fingerprint: fp}
}
