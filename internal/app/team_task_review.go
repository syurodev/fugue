package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const maxAcceptNoteBytes = 4 << 10

// Accept completes a report/no-diff direct child task in review (`complete_no_diff`
// -> `done`) and dispatches any dependent tasks that become startable (Q-125, Q-126).
// Diff tasks are refused until F11.5 wires the merge queue.
func (s *teamTaskService) Accept(ctx context.Context, sessionID, taskID, note string) (string, error) {
	trimmed := strings.TrimSpace(note)
	if len(trimmed) > maxAcceptNoteBytes {
		return "", errcode.New(errcode.InvalidArgument, "note exceeds 4 KiB limit")
	}
	var payload []byte
	if trimmed != "" {
		var encErr error
		payload, encErr = json.Marshal(struct {
			Note string `json:"note"`
		}{Note: trimmed})
		if encErr != nil {
			return "", errcode.Wrap(errcode.Internal, "encode accept note", encErr)
		}
	}
	var teamID, targetID, targetSessionID string
	if err := s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		target, delErr := c.requireDelegator(taskID)
		if delErr != nil {
			return nil, delErr
		}
		if target.State != vendorstore.TaskStateReview {
			return nil, errcode.New(errcode.FailedPrecondition, "task is not in review")
		}
		if target.Deliverable != vendorstore.DeliverableReport {
			return nil, errcode.New(errcode.FailedPrecondition, "diff task integration requires F11.5 merge queue")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate accept event ID", idErr)
		}
		teamID, targetID, targetSessionID = c.team.TeamID, target.TaskID, target.SessionID
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: target.TaskID, EventType: vendorstore.EventTaskReviewed,
			Outcome: "complete_no_diff", ProtectedPayload: payload,
		}, nil
	}); err != nil {
		return "", err
	}
	s.closeTaskSession(ctx, targetSessionID)
	s.dispatchAfterChange(ctx, teamID)
	return "task " + targetID + " accepted and done", nil
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
		c, target, err := s.validateRetryTarget(ctx, sessionID, taskID, role, model)
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
		})
		if retryErr == nil {
			return res, c.team.TeamID, target.SessionID, nil
		}
		if errors.Is(retryErr, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return vendorstore.TaskRetryResult{}, "", "", boundaryError("retry task", retryErr)
	}
	return vendorstore.TaskRetryResult{}, "", "", errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) validateRetryTarget(ctx context.Context, sessionID, taskID, role, model string) (*callerTeamContext, *vendorstore.TaskRecord, error) {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	target, err := c.requireDelegator(taskID)
	if err != nil {
		return nil, nil, err
	}
	if role != "" && role != target.CatalogRole {
		return nil, nil, errcode.New(errcode.FailedPrecondition, "changing role requires fugue_team_amend")
	}
	if model != "" {
		_, resolved, failCode, profErr := resolveTaskProfile(ctx, s.store, c.team.TeamID, target.TaskID, target.CatalogRole)
		if profErr != nil {
			return nil, nil, profErr
		}
		if failCode == "" && model != resolved.Model {
			return nil, nil, errcode.New(errcode.FailedPrecondition, "changing model requires fugue_team_amend")
		}
	}
	if target.State != vendorstore.TaskStateFailed {
		return nil, nil, errcode.New(errcode.FailedPrecondition, "only failed tasks can be retried")
	}
	if target.Attempt >= 1+vendorstore.MaxTaskRetries {
		return nil, nil, errcode.New(errcode.FailedPrecondition, "retry limit reached; use fugue_team_amend to add another attempt")
	}
	return c, target, nil
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
