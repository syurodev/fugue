package app

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/teams"
	"github.com/syurodev/fugue/internal/transcript"
	"github.com/syurodev/fugue/internal/vendor"
	"github.com/syurodev/fugue/internal/vendorstore"
)

func truncateUTF8(s string, maxBytes int) (string, bool) {
	if len(s) <= maxBytes {
		return s, false
	}
	cut := s[:maxBytes]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut, true
}

func sliceUTF8(s string, offset, limit int) string {
	if offset >= len(s) {
		return ""
	}
	for offset > 0 && offset < len(s) && !utf8.RuneStart(s[offset]) {
		offset++
	}
	if offset >= len(s) {
		return ""
	}
	cut, _ := truncateUTF8(s[offset:], limit)
	return cut
}

func (s *teamTaskService) appendTeamWithRetry(
	ctx context.Context,
	sessionID string,
	build func(ctx context.Context, c *callerTeamContext) (*vendorstore.AppendTeamEventInput, error),
) error {
	for attempt := range maxAppendRetries {
		c, err := s.resolveCaller(ctx, sessionID)
		if err != nil {
			return err
		}
		ev, err := build(ctx, c)
		if err != nil {
			return err
		}
		if _, err = s.store.AppendTeamEvent(ctx, c.team.TeamID, c.team.LastSeq, ev); err == nil {
			return nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return boundaryError("record team event", err)
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

func (s *teamTaskService) appendTaskEventWithRetry(
	ctx context.Context,
	teamID string,
	build func(rec *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error),
) error {
	for attempt := range maxAppendRetries {
		rec, tasks, err := s.store.TeamSnapshot(ctx, teamID)
		if err != nil {
			return boundaryError("read the team", err)
		}
		if rec.State != vendorstore.TeamStateActive {
			return errcode.New(errcode.FailedPrecondition, "the team is not active")
		}
		ev, err := build(&rec, tasks)
		if err != nil {
			return err
		}
		if _, err = s.store.AppendTeamEvent(ctx, teamID, rec.LastSeq, ev); err == nil {
			return nil
		}
		if errors.Is(err, vendorstore.ErrSequenceConflict) && attempt+1 < maxAppendRetries {
			continue
		}
		return boundaryError("record team event", err)
	}
	return errcode.New(errcode.FailedPrecondition, "team event sequence conflict")
}

// OnTurnCompleted enforces the Q-131 no-report rule when a task session
// completes a turn while still in TaskStateRunning. A team_manager with
// non-terminal children is never nudged or failed until its last child ends.
func (s *teamTaskService) OnTurnCompleted(ctx context.Context, sessionID string) {
	task, found, err := s.store.TaskForSession(ctx, sessionID)
	if err != nil {
		slog.WarnContext(ctx, "no-report check failed to read task", "session_id", sessionID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if !found || task.State != vendorstore.TaskStateRunning ||
		task.HierarchyRole == vendorstore.RoleLead || task.HierarchyRole == vendorstore.RoleSolo {
		return
	}
	if task.HierarchyRole == vendorstore.RoleTeamManager {
		_, tasks, snapErr := s.store.TeamSnapshot(ctx, task.TeamID)
		if snapErr != nil {
			slog.WarnContext(ctx, "no-report check failed to read team", "team_id", task.TeamID, "error_code", string(errcode.CodeOf(snapErr)))
			return
		}
		if hasNonTerminalChild(task.TaskID, tasks) {
			return
		}
	}
	firstMiss, err := s.store.MarkTaskNoReportNudged(ctx, task.TeamID, task.TaskID)
	if err != nil {
		slog.WarnContext(ctx, "marking no_report_nudged failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if firstMiss {
		if sendErr := s.sessions.Send(ctx, sessionID, vendor.Input{
			Text: "Your turn ended without calling fugue_task_done. Call fugue_task_done now with your summary, status, and any output.",
		}); sendErr != nil {
			slog.WarnContext(ctx, "sending no-report nudge failed", "session_id", sessionID, "error_code", string(errcode.CodeOf(sendErr)))
		}
		return
	}
	s.failTaskNoReport(ctx, &task, sessionID)
}

func (s *teamTaskService) failTaskNoReport(ctx context.Context, task *vendorstore.TaskRecord, sessionID string) {
	summary, _ := truncateUTF8(s.lastAssistantText(ctx, sessionID), teams.MaxReportSummaryBytes)
	if err := s.appendTaskEventWithRetry(ctx, task.TeamID, func(_ *vendorstore.TeamRecord, tasks []vendorstore.TaskRecord) (*vendorstore.AppendTeamEventInput, error) {
		cur, ok := findTask(tasks, task.TaskID)
		if !ok || cur.State != vendorstore.TaskStateRunning {
			return nil, errcode.New(errcode.FailedPrecondition, "task is no longer running")
		}
		evID, idErr := s.ids.NewID()
		if idErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "generate no-report event ID", idErr)
		}
		return &vendorstore.AppendTeamEventInput{
			EventID: evID, TaskID: cur.TaskID, EventType: vendorstore.EventTaskEnded,
			Outcome: string(vendorstore.TaskStateFailed), FailureCode: "no_report",
			Mailbox: digestSummaryRows(cur.TaskID, cur.ParentTaskID, string(vendorstore.TaskStateFailed), "no_report", summary, false),
		}, nil
	}); err != nil {
		slog.WarnContext(ctx, "recording no_report failure failed", "team_id", task.TeamID, "task_id", task.TaskID, "error_code", string(errcode.CodeOf(err)))
		return
	}
	if s.wake != nil {
		s.wake.Notify()
	}
}

func (s *teamTaskService) lastAssistantText(ctx context.Context, sessionID string) string {
	items, err := s.store.Transcript(ctx, sessionID, 0, 100)
	if err != nil {
		return ""
	}
	for i := len(items) - 1; i >= 0; i-- {
		if items[i].Kind != string(transcript.AssistantMessage) {
			continue
		}
		_, content, readErr := s.store.ReadItem(ctx, sessionID, items[i].ID)
		if readErr != nil || len(content) == 0 {
			continue
		}
		var msg struct {
			Text string `json:"Text"`
		}
		if json.Unmarshal(content, &msg) == nil && msg.Text != "" {
			return msg.Text
		}
		return string(content)
	}
	return ""
}
