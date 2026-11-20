package app

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

const (
	maxCancelReasonBytes = 512
	cancelFailureCode    = "cancelled_by_delegator"
)

// Cancel cancels a non-terminal direct child task (and any non-terminal
// descendants in its subtree) and closes their active sessions.
func (s *teamTaskService) Cancel(ctx context.Context, sessionID, taskID, reason string) (string, error) {
	cutReason, _ := truncateUTF8(strings.TrimSpace(reason), maxCancelReasonBytes)
	payload, err := json.Marshal(struct {
		Reason string `json:"reason,omitempty"`
	}{Reason: cutReason})
	if err != nil {
		return "", errcode.Wrap(errcode.Internal, "encode cancel reason", err)
	}
	var rootID, rootSessionID string
	if err := s.appendTeamWithRetry(ctx, sessionID, func(_ context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error) {
		ev, sid, buildErr := s.buildCancelEvent(c, taskID, payload)
		if buildErr != nil {
			return nil, buildErr
		}
		rootID, rootSessionID = ev.TaskID, sid
		return ev, nil
	}); err != nil {
		return "", err
	}
	descSessions := s.cancelSubtreeDescendants(ctx, sessionID, rootID, payload)
	sessions := make([]string, 0, 1+len(descSessions))
	if rootSessionID != "" {
		sessions = append(sessions, rootSessionID)
	}
	sessions = append(sessions, descSessions...)
	s.closeCancelledSessions(ctx, sessions)
	return "task " + rootID + " cancelled", nil
}

func (s *teamTaskService) buildCancelEvent(c *callerTeamContext, taskID string, payload []byte) (*vendorstore.AppendTeamEventInput, string, error) {
	target, delErr := c.requireDelegator(taskID)
	if delErr != nil {
		return nil, "", delErr
	}
	if teamTaskTerminal(target.State) {
		return nil, "", errcode.New(errcode.FailedPrecondition, "task is already terminal")
	}
	evID, idErr := s.ids.NewID()
	if idErr != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "generate cancel event ID", idErr)
	}
	return &vendorstore.AppendTeamEventInput{
		EventID: evID, TaskID: target.TaskID, EventType: vendorstore.EventTaskEnded,
		Outcome: string(vendorstore.TaskStateCancelled), FailureCode: cancelFailureCode,
		ProtectedPayload: payload,
	}, target.SessionID, nil
}

func (s *teamTaskService) closeCancelledSessions(ctx context.Context, sessions []string) {
	for _, sid := range sessions {
		if s.interactions != nil && s.interactions.owner != nil {
			s.interactions.owner.ResetSession(sid)
		}
		if s.sessions == nil {
			continue
		}
		if closeErr := s.sessions.Close(ctx, sid, vendor.CloseHostStop); closeErr != nil {
			slog.WarnContext(ctx, "closing cancelled task session failed", "session_id", sid, "error_code", string(errcode.CodeOf(closeErr)))
		}
	}
}

func (s *teamTaskService) cancelSubtreeDescendants(ctx context.Context, sessionID, rootID string, payload []byte) []string {
	c, err := s.resolveCaller(ctx, sessionID)
	if err != nil {
		return nil
	}
	desc := collectSubtreeTasks(c.tasks, rootID)
	sessions := make([]string, 0, len(desc))
	for i := range desc {
		if teamTaskTerminal(desc[i].State) {
			continue
		}
		if sid := s.cancelDescendant(ctx, c.team.TeamID, desc[i].TaskID, payload); sid != "" {
			sessions = append(sessions, sid)
		}
	}
	return sessions
}

func (s *teamTaskService) cancelDescendant(ctx context.Context, teamID, taskID string, payload []byte) string {
	var sid string
	if err := s.appendTaskEventWithRetry(ctx, teamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, taskID)
		if !ok || teamTaskTerminal(cur.State) {
			return nil, errcode.New(errcode.FailedPrecondition, "descendant already terminal")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate cancel event ID", idErr)
		}
		sid = cur.SessionID
		if cur.State == vendorstore.TaskStateUnknown {
			return &vendorstore.AppendTeamEventInput{
				EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskRecoveryDecided,
				Outcome: "cancel", ProtectedPayload: payload,
			}, nil
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: taskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TaskStateCancelled), FailureCode: cancelFailureCode,
			ProtectedPayload: payload,
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "cancelling subtree descendant failed", "task_id", taskID, "error_code", string(errcode.CodeOf(err)))
		return ""
	}
	return sid
}

func collectSubtreeTasks(tasks []vendorstore.TaskRecord, rootID string) []vendorstore.TaskRecord {
	byID, retryChild := resolveTaskChain(tasks)
	queue := []string{rootID}
	seen := map[string]bool{rootID: true}
	var out []vendorstore.TaskRecord
	for len(queue) > 0 {
		parent := queue[0]
		queue = queue[1:]
		for i := range tasks {
			if seen[tasks[i].TaskID] || !sameAttemptChain(tasks[i].ParentTaskID, parent, byID, retryChild) {
				continue
			}
			seen[tasks[i].TaskID] = true
			out = append(out, tasks[i])
			queue = append(queue, tasks[i].TaskID)
		}
	}
	return out
}
