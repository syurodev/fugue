package vendorstore

import (
	"context"
	"database/sql"
	"fmt"
)

// EndedTeams lists all terminal teams (done, cancelled, failed) that have a
// recorded ended_at timestamp, oldest-ended first. The result carries metadata
// only and succeeds even while the vault is locked (INV-09).
func (s *Store) EndedTeams(ctx context.Context) ([]TeamRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT team_id, workspace_id, session_id, state, waiting_reason, last_seq, task_count,
		       failure_code, created_at, updated_at, ended_at
		FROM team_records
		WHERE ended_at IS NOT NULL AND state IN (?, ?, ?)
		ORDER BY ended_at ASC, team_id ASC`,
		string(TeamStateDone), string(TeamStateCancelled), string(TeamStateFailed),
	)
	if err != nil {
		return nil, fmt.Errorf("%w: query ended teams: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)

	var out []TeamRecord
	for rows.Next() {
		var rec TeamRecord
		var stateStr string
		var endedAt sql.NullInt64
		if err := rows.Scan(
			&rec.TeamID, &rec.WorkspaceID, &rec.SessionID, &stateStr, &rec.WaitingReason,
			&rec.LastSeq, &rec.TaskCount, &rec.FailureCode,
			&rec.CreatedAt, &rec.UpdatedAt, &endedAt,
		); err != nil {
			return nil, fmt.Errorf("scan ended team: %w", err)
		}
		rec.State = TeamState(stateStr)
		if endedAt.Valid {
			rec.EndedAt = endedAt.Int64
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ended teams: %w", err)
	}
	return out, nil
}
