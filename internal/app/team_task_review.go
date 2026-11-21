package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/interaction"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const maxAcceptNoteBytes = 4 << 10

// Accept completes a report/no-diff direct child task in review (`complete_no_diff`
// -> `done`) or approves a diff task (`approved` -> `integrating`) and enqueues
// it into the target branch merge queue (§9).
func (s *teamTaskService) Accept(ctx context.Context, sessionID, taskID, note string) (string, error) {
	trimmed := strings.TrimSpace(note)
	if len(trimmed) > maxAcceptNoteBytes {
		return "", errcode.New(errcode.InvalidArgument, "note exceeds 4 KiB limit")
	}
	payload, err := encodeAcceptNote(trimmed)
	if err != nil {
		return "", err
	}
	if diffErr := s.maybeAskDiffStepApproval(ctx, sessionID, taskID); diffErr != nil {
		return "", diffErr
	}
	teamID, targetID, targetSessionID, isDiff, err := s.appendAcceptEvent(ctx, sessionID, taskID, payload)
	if err != nil {
		return "", err
	}
	if isDiff {
		if enqErr := s.mergeQueue.Enqueue(ctx, teamID, targetID); enqErr != nil {
			return "", enqErr
		}
		return "task " + targetID + " accepted for integration", nil
	}
	s.closeTaskSession(ctx, targetSessionID)
	s.dispatchAfterChange(ctx, teamID)
	return "task " + targetID + " accepted and done", nil
}

func (s *teamTaskService) appendAcceptEvent(ctx context.Context, sessionID, taskID string, payload []byte) (teamID, targetID, targetSessionID string, isDiff bool, err error) {
	err = s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		target, delErr := c.requireDelegator(taskID)
		if delErr != nil {
			return nil, delErr
		}
		if target.State != vendorstore.TaskStateReview {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not in review")
		}
		outcome := "complete_no_diff"
		if target.Deliverable != vendorstore.DeliverableReport {
			if s.mergeQueue == nil {
				return nil, errcode.New(errcode.FailedPrecondition, "diff task integration requires F11.5 merge queue")
			}
			outcome = "approved"
			isDiff = true
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate accept event ID", idErr)
		}
		teamID, targetID, targetSessionID = c.team.TeamID, target.TaskID, target.SessionID
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: target.TaskID, EventType: vendorstore.EventTaskReviewed,
			Outcome: outcome, ProtectedPayload: payload,
		}, nil
	})
	return teamID, targetID, targetSessionID, isDiff, err
}

func encodeAcceptNote(trimmed string) ([]byte, error) {
	if trimmed == "" {
		return nil, nil
	}
	payload, err := json.Marshal(struct {
		Note string `json:"note"`
	}{Note: trimmed})
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "encode accept note", err)
	}
	return payload, nil
}

func (s *teamTaskService) maybeAskDiffStepApproval(ctx context.Context, sessionID, taskID string) error {
	if s.teamsCfg == nil {
		return nil
	}
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return err
	}
	target, delErr := c.requireDelegator(taskID)
	if delErr != nil {
		return delErr
	}
	if target.State != vendorstore.TaskStateReview || target.Deliverable == vendorstore.DeliverableReport {
		return nil
	}
	cfg, cfgErr := s.teamsCfg(ctx, c.team.WorkspaceID)
	if cfgErr != nil {
		return boundaryError("read teams config", cfgErr)
	}
	if !cfg.ApproveEveryDiffStep {
		return nil
	}
	return s.askDiffStepCard(ctx, sessionID, c.team.WorkspaceID, c.team.TeamID, target.TaskID)
}

func (s *teamTaskService) askDiffStepCard(ctx context.Context, sessionID, workspaceID, teamID, taskID string) error {
	if s.interactions == nil || s.interactions.owner == nil {
		return errcode.New(errcode.FailedPrecondition, "interaction service is not configured")
	}
	invID, err := s.ids.NewID()
	if err != nil {
		return errcode.Wrap(errcode.Internal, "generate diff approval ID", err)
	}
	opID, err := interaction.NativeOperationKey(invID, invID, 1)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "key diff approval", err)
	}
	digest := sha256.Sum256([]byte(teamID + ":" + taskID + ":diff_step"))
	ticket, err := s.interactions.Ensure(&interaction.Spec{
		OperationID: opID,
		Kind:        interaction.KindConfirm,
		Summary:     "Approve integrating diff for task " + taskID,
		Risk:        string(interaction.RiskWrite),
		Offers:      []interaction.Outcome{interaction.OutcomeAllowedOnce},
		Binding: interaction.Binding{
			SessionID: sessionID, WorkspaceID: workspaceID,
			InvocationID: invID, AttemptID: invID, CheckGeneration: 1,
			ToolName: "diff_step_approval", ArgsDigest: hex.EncodeToString(digest[:]),
		},
	})
	if err != nil {
		return err
	}
	if !ticket.Resolved {
		if ticket, err = s.interactions.AwaitResolved(ctx, ticket.RequestID); err != nil {
			if _, cancelErr := s.interactions.owner.InvalidateOperation(opID); cancelErr != nil {
				return errcode.Wrap(errcode.Internal, "cancel abandoned diff approval", cancelErr)
			}
			return err
		}
	}
	if ticket.Outcome != interaction.OutcomeAllowedOnce {
		return errcode.New(errcode.PermissionDenied, "diff integration rejected by user")
	}
	return nil
}

// Reject transitions a direct child task from review back to running with
// requested changes delivered immediately through the mailbox (§4, §6.6).
func (s *teamTaskService) Reject(ctx context.Context, sessionID, taskID, reason string) (string, error) {
	trimmed, err := validateDigestMessage("reason", reason)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(struct {
		Reason string `json:"reason"`
	}{Reason: trimmed})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode reject reason", err)
	}
	var targetID string
	if err := s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		target, delErr := c.requireDelegator(taskID)
		if delErr != nil {
			return nil, delErr
		}
		if target.State != vendorstore.TaskStateReview {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not in review")
		}
		mailPayload, mErr := json.Marshal(mailboxDigestItem{
			TaskID: c.caller.TaskID, State: "changes_requested", Summary: trimmed,
		})
		if mErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "encode reject mailbox payload", mErr)
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate reject event ID", idErr)
		}
		targetID = target.TaskID
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: target.TaskID, EventType: vendorstore.EventTaskReviewed,
			Outcome: "request_changes", ProtectedPayload: payload,
			Mailbox: []vendorstore.MailboxEnqueueInput{{
				RecipientTaskID: target.TaskID, Kind: vendorstore.MailboxKindSend, Payload: mailPayload,
			}},
		}, nil
	}); err != nil {
		return "", err
	}
	if s.wake != nil {
		s.wake.Notify()
	}
	return "task " + targetID + " rejected back to running", nil
}

// Retry supersedes a failed direct child task (`failed` -> `stopped` via
// TaskSuperseded) and queues `<root>~r<n>` with the identical role and model
// up to two retries without user approval (Q-126, OR-E16).
func (s *teamTaskService) Retry(ctx context.Context, sessionID, taskID, role, model string) (string, error) {
	res, teamID, oldSessionID, err := s.retryWithConflictLoop(ctx, sessionID, taskID, role, model)
	if err != nil {
		return "", err
	}
	s.closeTaskSession(ctx, oldSessionID)
	s.dispatchAfterChange(ctx, teamID)
	return fmt.Sprintf("task %s queued as retry attempt %d", res.NewTask.TaskID, res.NewTask.Attempt), nil
}

func (s *teamTaskService) retryWithConflictLoop(ctx context.Context, sessionID, taskID, role, model string) (vendorstore.TaskRetryResult, string, string, error) {
	for attempt := range maxAppendRetries {
		c, target, maxAgents, err := s.validateRetryTarget(ctx, sessionID, taskID, role, model)
		if err != nil {
			return vendorstore.TaskRetryResult{}, "", "", err
		}
		supID, err := s.ids.NewID()
		if err != nil {
			return vendorstore.TaskRetryResult{}, "", "", errcode.Wrap(errcode.Internal, "generate superseded event ID", err)
		}
		creID, err := s.ids.NewID()
		if err != nil {
			return vendorstore.TaskRetryResult{}, "", "", errcode.Wrap(errcode.Internal, "generate retry created event ID", err)
		}
		res, retryErr := s.store.RetryTask(ctx, c.team.TeamID, target.TaskID, &vendorstore.TaskRetryInput{
			ExpectedSeq: c.team.LastSeq, SupersededEventID: supID, CreatedEventID: creID,
			MaxTeamAgents: maxAgents,
		})
		if retryErr == nil {
			return res, c.team.TeamID, target.SessionID, nil
		}
		if errors.Is(retryErr, vendorstore.ErrTeamAgentCapReached) {
			return vendorstore.TaskRetryResult{}, "", "", errcode.New(errcode.FailedPrecondition, "team agent cap reached; only the delegator may raise capacity via fugue_team_amend")
		}
		if errors.Is(retryErr, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return vendorstore.TaskRetryResult{}, "", "", boundaryError("retry task", retryErr)
	}
	return vendorstore.TaskRetryResult{}, "", "", errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) validateRetryTarget(ctx context.Context, sessionID, taskID, role, model string) (*callerTeamContext, *vendorstore.TaskRecord, int, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return nil, nil, 0, err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return nil, nil, 0, err
	}
	if role != "" && role != target.CatalogRole {
		return nil, nil, 0, errcode.New(errcode.FailedPrecondition, "changing role requires fugue_team_amend")
	}
	if model != "" {
		_, resolved, failCode, profErr := resolveTaskProfile(ctx, s.store, c.team.TeamID, target.TaskID, target.CatalogRole)
		if profErr != nil {
			return nil, nil, 0, profErr
		}
		if failCode == "" && model != resolved.Model {
			return nil, nil, 0, errcode.New(errcode.FailedPrecondition, "changing model requires fugue_team_amend")
		}
	}
	if target.State != vendorstore.TaskStateFailed {
		return nil, nil, 0, errcode.New(errcode.FailedPrecondition, "only failed tasks can be retried")
	}
	if target.Attempt >= 1+vendorstore.MaxTaskRetries {
		return nil, nil, 0, errcode.New(errcode.FailedPrecondition, "retry limit reached; use fugue_team_amend to add another attempt")
	}
	maxAgents := resolveTeamAgentCap(ctx, s.store, s.teamsCfg, &c.team)
	if countNonLeadTasks(c.tasks) >= maxAgents {
		return nil, nil, 0, errcode.New(errcode.FailedPrecondition, "team agent cap reached; only the delegator may raise capacity via fugue_team_amend")
	}
	return c, target, maxAgents, nil
}

func (s *teamTaskService) closeTaskSession(ctx context.Context, sessionID string) {
	if sessionID == "" || s.sessions == nil {
		return
	}
	if err := s.sessions.Close(ctx, sessionID, vendor.CloseHostStop); err != nil && !errors.Is(err, vendorstore.ErrSessionNotFound) {
		slog.WarnContext(ctx, "closing task session failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
	}
}

func (s *teamTaskService) dispatchAfterChange(ctx context.Context, teamID string) {
	if s.dispatch == nil {
		return
	}
	if err := s.dispatch.Dispatch(ctx, teamID); err != nil {
		slog.WarnContext(ctx, "dispatching team tasks after state change failed", "team_id", teamID, "error_code", string(errcode.CodeOf(err)))
	}
}
