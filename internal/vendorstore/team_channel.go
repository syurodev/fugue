package vendorstore

// F11.10a team channel (orchestration v0.3 §6.7, Q-176 A, Q-178 A). The
// channel is a projection of the team event log: every line is written in the
// same transaction as its source event and carries only metadata — the
// content stays sealed in the source event payload (INV-09). Replaying the
// log through channelLinesForEvent reproduces the rows exactly, so the table
// is never a second source of truth.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// retryChainSep separates a retry root from its attempt marker (Q-126:
// `<root>~r<n>`); task IDs never contain `~` otherwise (gitTaskID maps it to
// `.`), so the last marker is the chain's.
const retryChainSep = "~r"

// ThreadRootTaskID resolves the retry-chain root of a task ID, so every
// attempt of one task shares a channel thread (§6.7).
func ThreadRootTaskID(taskID string) string {
	if i := strings.LastIndex(taskID, retryChainSep); i > 0 {
		if _, err := strconv.Atoi(taskID[i+len(retryChainSep):]); err == nil {
			return taskID[:i]
		}
	}
	return taskID
}

// channelLine is one projected line of a single event, before numbering.
type channelLine struct {
	kind       string
	threadRoot string
	senderKind string
	senderTask string
	recipient  string
}

// channelPayloadFacts is the event-local payload data the mapping needs. Only
// allowlisted metadata fields are parsed (INV-09); the payload bytes never
// leave the store unsealed.
type channelPayloadFacts struct {
	Author   string `json:"author_task_id"`
	TeamWide bool   `json:"team_wide"`
	ToTaskID string `json:"to_task_id"`
	AnswerBy string `json:"answered_by"`
}

// channelLinesForEvent maps one event to its channel lines using only
// event-row-local data: the type, the task ID, the outcome, the failure code
// and the sealed payload's allowlisted fields. It is the rebuild function:
// replaying the log through it reproduces every row (§6.7, Q-178 A).
func channelLinesForEvent(ev *AppendTeamEventInput) []channelLine {
	root := ""
	if ev.TaskID != "" {
		root = ThreadRootTaskID(ev.TaskID)
	}
	facts := channelPayloadFacts{}
	if len(ev.ProtectedPayload) > 0 {
		// A payload that does not parse leaves the facts zero; the line keeps
		// its metadata and renders from it.
		_ = json.Unmarshal(ev.ProtectedPayload, &facts)
	}
	switch ev.EventType {
	case EventTeamProposed:
		return []channelLine{{kind: ChannelKindPlan, senderKind: ChannelSenderAgent}}
	case EventTeamStarted, EventTeamAmended, EventTeamDecided:
		return []channelLine{{kind: ChannelKindDecision, senderKind: ChannelSenderUser}}
	case EventTeamNoted:
		return []channelLine{{kind: ChannelKindNote, senderKind: ChannelSenderAgent, senderTask: facts.Author}}
	case EventTaskAssigned:
		return []channelLine{{kind: ChannelKindTask, threadRoot: root, senderKind: ChannelSenderFugue}}
	case EventTaskSuperseded:
		// The retry chain's attempt-open line (§5.4, §6.7): the supersede and
		// the queued next attempt commit in the same transaction, so this is
		// the moment the new attempt opens; the rendered text names the
		// attempt and the failure code.
		return []channelLine{{kind: ChannelKindTask, threadRoot: root, senderKind: ChannelSenderFugue}}
	case EventTaskQuestionAsked:
		return []channelLine{{kind: ChannelKindQuestion, threadRoot: root, senderKind: ChannelSenderAgent, senderTask: ev.TaskID}}
	case EventTaskQuestionAnswered:
		sender := ChannelSenderUser
		senderTask := ""
		if facts.AnswerBy != "" {
			sender = ChannelSenderAgent
			senderTask = facts.AnswerBy
		}
		return []channelLine{{kind: ChannelKindAnswer, threadRoot: root, senderKind: sender, senderTask: senderTask}}
	case EventTaskReported:
		return []channelLine{{kind: ChannelKindResult, threadRoot: root, senderKind: ChannelSenderAgent, senderTask: ev.TaskID}}
	case EventTaskMessaged:
		return []channelLine{{kind: ChannelKindSend, threadRoot: root, senderKind: ChannelSenderAgent, senderTask: ev.TaskID, recipient: facts.ToTaskID}}
	case EventTaskStalled:
		lines := []channelLine{{kind: ChannelKindStall, threadRoot: root, senderKind: ChannelSenderWatchdog}}
		if facts.TeamWide {
			// §6.7: the same stall also shows in the general channel when the
			// whole team is waiting.
			lines = append(lines, channelLine{kind: ChannelKindStall, senderKind: ChannelSenderWatchdog})
		}
		return lines
	case EventTaskEnded:
		if ev.Outcome == outcomeFailed {
			return []channelLine{{kind: ChannelKindFailed, threadRoot: root, senderKind: ChannelSenderAgent, senderTask: ev.TaskID}}
		}
		return nil
	default:
		return nil
	}
}

// appendChannelForEvent numbers and writes the lines of one appended event
// inside its transaction. The per-team numbering is contiguous by the table
// trigger; MAX+1 under the append's write lock cannot race.
func appendChannelForEvent(ctx context.Context, tx *sql.Tx, teamID string, seq int64, ev *AppendTeamEventInput, now int64) error {
	lines := channelLinesForEvent(ev)
	if len(lines) == 0 {
		return nil
	}
	var next int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(channel_seq), 0) + 1 FROM team_channel_messages WHERE team_id = ?`,
		teamID,
	).Scan(&next); err != nil {
		return fmt.Errorf("read the channel sequence: %w", err)
	}
	for i := range lines {
		line := &lines[i]
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO team_channel_messages (
				team_id, channel_seq, kind, thread_root_task_id, sender_kind,
				sender_task_id, recipient_task_id, source_seq, bytes, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			teamID, next+int64(i), line.kind, line.threadRoot, line.senderKind,
			line.senderTask, line.recipient, seq, len(ev.ProtectedPayload), now,
		); err != nil {
			return fmt.Errorf("insert the channel line: %w", err)
		}
	}
	return nil
}

// TeamChannelMessages pages one team's channel metadata (INV-09): newest
// first, Before and Since as exclusive cursors, scoped by the query's thread
// roots. Reading it never wakes anyone and needs no vault.
func (s *Store) TeamChannelMessages(ctx context.Context, teamID string, q TeamChannelPageQuery) ([]TeamChannelMessage, error) {
	if teamID == "" {
		return nil, fmt.Errorf("%w: team_id is required", ErrTeamInvalidInput)
	}
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 500
	}
	var b strings.Builder
	b.WriteString(`SELECT team_id, channel_seq, kind, thread_root_task_id, sender_kind,
		sender_task_id, recipient_task_id, source_seq, bytes, created_at
		FROM team_channel_messages WHERE team_id = ?`)
	args := []any{teamID}
	switch {
	case q.GeneralOnly:
		b.WriteString(" AND thread_root_task_id = ''")
	case len(q.ThreadRoots) > 0:
		b.WriteString(" AND thread_root_task_id IN (" + strings.Repeat("?,", len(q.ThreadRoots)-1) + "?)")
		for _, r := range q.ThreadRoots {
			args = append(args, r)
		}
	}
	if q.Before > 0 {
		b.WriteString(" AND channel_seq < ?")
		args = append(args, q.Before)
	}
	if q.Since > 0 {
		b.WriteString(" AND channel_seq > ?")
		args = append(args, q.Since)
	}
	b.WriteString(" ORDER BY channel_seq DESC LIMIT ?")
	args = append(args, q.Limit)
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("%w: query the channel: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []TeamChannelMessage
	for rows.Next() {
		var m TeamChannelMessage
		if err := rows.Scan(&m.TeamID, &m.ChannelSeq, &m.Kind, &m.ThreadRoot, &m.SenderKind,
			&m.SenderTaskID, &m.RecipientTaskID, &m.SourceSeq, &m.Bytes, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan the channel: %w", ErrUnavailable, err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the channel: %w", ErrUnavailable, err)
	}
	return out, nil
}

// TeamChannelLinesForSources returns the metadata of every channel line that
// projects the given source sequences, so a digest can cite the `#n` of its
// source lines (§6.7). Metadata only; needs no vault.
func (s *Store) TeamChannelLinesForSources(ctx context.Context, teamID string, sources []int64) (map[int64][]TeamChannelMessage, error) {
	if teamID == "" || len(sources) == 0 {
		return map[int64][]TeamChannelMessage{}, nil
	}
	var b strings.Builder
	b.WriteString(`SELECT team_id, channel_seq, kind, thread_root_task_id, sender_kind,
		sender_task_id, recipient_task_id, source_seq, bytes, created_at
		FROM team_channel_messages WHERE team_id = ? AND source_seq IN (
		` + strings.Repeat("?,", len(sources)-1) + "?) ORDER BY channel_seq")
	args := []any{teamID}
	for _, seq := range sources {
		args = append(args, seq)
	}
	rows, err := s.db.QueryContext(ctx, b.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("%w: query the channel sources: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	out := map[int64][]TeamChannelMessage{}
	for rows.Next() {
		var m TeamChannelMessage
		if err := rows.Scan(&m.TeamID, &m.ChannelSeq, &m.Kind, &m.ThreadRoot, &m.SenderKind,
			&m.SenderTaskID, &m.RecipientTaskID, &m.SourceSeq, &m.Bytes, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("%w: scan the channel source: %w", ErrUnavailable, err)
		}
		out[m.SourceSeq] = append(out[m.SourceSeq], m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the channel sources: %w", ErrUnavailable, err)
	}
	return out, nil
}

// CountTaskMessages counts the sibling-send events inside the sliding window
// ending now, per sender and for the whole team (§6.7: 6 per agent and 30 per
// team per 10 minutes, Q-177 A). Events are the count source, so the caps
// hold across restarts without a second tally.
func (s *Store) CountTaskMessages(ctx context.Context, teamID, senderTaskID string, sinceMs int64) (byAgent, team int64, err error) {
	if teamID == "" || senderTaskID == "" {
		return 0, 0, fmt.Errorf("%w: team_id and sender task are required", ErrTeamInvalidInput)
	}
	err = s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(task_id = ?), 0),
			COUNT(*)
		FROM team_task_events
		WHERE team_id = ? AND event_type = ? AND created_at > ?`,
		senderTaskID, teamID, string(EventTaskMessaged), sinceMs,
	).Scan(&byAgent, &team)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: count the sibling sends: %w", ErrUnavailable, err)
	}
	return byAgent, team, nil
}
