package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TeamSummaryRecord is the metadata-only summary of a team and its task state
// counts (host-api v0.19 §5.3, INV-09).
type TeamSummaryRecord struct {
	TeamRecord
	RunningCount int
	AskingCount  int
	FailedCount  int
}

// ListTeams returns metadata-only team summaries, optionally filtered by
// workspaceID and/or sessionID, newest first. It succeeds even while the
// vault is locked (INV-09).
func (s *Store) ListTeams(ctx context.Context, workspaceID, sessionID string) ([]TeamSummaryRecord, error) {
	query, args := listTeamsQuery(workspaceID, sessionID)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("%w: list teams: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)

	var out []TeamSummaryRecord
	for rows.Next() {
		rec, scanErr := scanTeamSummaryRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams: %w", err)
	}
	return out, nil
}

func listTeamsQuery(workspaceID, sessionID string) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT r.team_id, r.workspace_id, r.session_id, r.state, r.waiting_reason, r.last_seq, r.task_count,
		       r.failure_code, r.created_at, r.updated_at, r.ended_at,
		       COALESCE(SUM(CASE WHEN t.state IN ('starting', 'running', 'checking', 'review', 'integrating') THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN t.state = 'asking' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN t.state = 'failed' THEN 1 ELSE 0 END), 0)
		FROM team_records r
		LEFT JOIN team_tasks t ON t.team_id = r.team_id`)
	var conds []string
	var args []any
	if workspaceID != "" {
		conds = append(conds, "r.workspace_id = ?")
		args = append(args, workspaceID)
	}
	if sessionID != "" {
		conds = append(conds, "r.session_id = ?")
		args = append(args, sessionID)
	}
	if len(conds) > 0 {
		b.WriteString(" WHERE " + strings.Join(conds, " AND "))
	}
	b.WriteString(" GROUP BY r.team_id ORDER BY r.created_at DESC, r.team_id DESC")
	return b.String(), args
}

func scanTeamSummaryRow(rows *sql.Rows) (TeamSummaryRecord, error) {
	var rec TeamSummaryRecord
	var stateStr string
	var endedAt sql.NullInt64
	if err := rows.Scan(
		&rec.TeamID, &rec.WorkspaceID, &rec.SessionID, &stateStr, &rec.WaitingReason,
		&rec.LastSeq, &rec.TaskCount, &rec.FailureCode,
		&rec.CreatedAt, &rec.UpdatedAt, &endedAt,
		&rec.RunningCount, &rec.AskingCount, &rec.FailedCount,
	); err != nil {
		return TeamSummaryRecord{}, fmt.Errorf("scan team summary: %w", err)
	}
	rec.State = TeamState(stateStr)
	if endedAt.Valid {
		rec.EndedAt = endedAt.Int64
	}
	return rec, nil
}

// TeamTaskLastChannelSeqs returns the highest channel_seq associated with each
// task_id (as sender, recipient, or thread root) in teamID. Succeeds while the
// vault is locked (INV-09).
func (s *Store) TeamTaskLastChannelSeqs(ctx context.Context, teamID string) (map[string]int64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT task_id, MAX(channel_seq) FROM (
			SELECT sender_task_id AS task_id, channel_seq FROM team_channel_messages WHERE team_id = ? AND sender_task_id != ''
			UNION ALL
			SELECT recipient_task_id AS task_id, channel_seq FROM team_channel_messages WHERE team_id = ? AND recipient_task_id != ''
			UNION ALL
			SELECT thread_root_task_id AS task_id, channel_seq FROM team_channel_messages WHERE team_id = ? AND thread_root_task_id != ''
		) GROUP BY task_id`,
		teamID, teamID, teamID,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: query task channel seqs: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)

	out := make(map[string]int64)
	for rows.Next() {
		var taskID string
		var seq int64
		if scanErr := rows.Scan(&taskID, &seq); scanErr != nil {
			return nil, fmt.Errorf("scan task channel seq: %w", scanErr)
		}
		out[taskID] = seq
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate task channel seqs: %w", err)
	}
	return out, nil
}

// OpenTeamProposedPayload opens the sealed TeamProposed payload for teamID, or
// returns found=false when none exists. Fails closed with vault.ErrLocked
// when the vault is locked.
func (s *Store) OpenTeamProposedPayload(ctx context.Context, teamID string) ([]byte, bool, error) {
	if _, err := s.requireUnlockedVault(); err != nil {
		return nil, false, err
	}
	var seq int64
	var hasPayload int
	err := s.db.QueryRowContext(ctx,
		`SELECT sequence, (payload_envelope IS NOT NULL) FROM team_task_events
		 WHERE team_id = ? AND event_type = ?
		 ORDER BY sequence ASC LIMIT 1`,
		teamID, string(EventTeamProposed),
	).Scan(&seq, &hasPayload)
	if errors.Is(err, sql.ErrNoRows) || hasPayload == 0 {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: query team proposed event: %w", ErrUnavailable, err)
	}
	plain, err := s.OpenTeamEventPayload(ctx, teamID, seq)
	if err != nil {
		return nil, false, err
	}
	return plain, true, nil
}
