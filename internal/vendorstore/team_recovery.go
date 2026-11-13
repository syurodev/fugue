package vendorstore

import (
	"context"
	"database/sql"
	"fmt"
)

// RecoverTeams enforces OR-15 on host restart: any task in starting, running,
// asking, or checking is moved to unknown with a TaskMarkedUnknown event and is
// never automatically re-run. Only an explicit recovery decision leaves unknown.
// RecoverTeams operates on metadata only so it runs cleanly on process start
// before the vault is unlocked.
func (s *Store) RecoverTeams(ctx context.Context) ([]RecoveredTask, error) {
	candidates, err := queryTasksNeedingUnknown(ctx, s.db)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin recover teams tx: %w", err)
	}
	defer rollback(ctx, tx)

	now := s.clock.Now().UnixMilli()
	recovered, err := markTasksUnknownTx(ctx, tx, candidates, now)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit recover teams tx: %w", err)
	}
	return recovered, nil
}

type recoveryCandidate struct {
	TeamID        string
	TaskID        string
	PreviousState TaskState
}

func queryTasksNeedingUnknown(ctx context.Context, db *sql.DB) ([]recoveryCandidate, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT team_id, task_id, state
		FROM team_tasks
		WHERE state IN ('running', 'starting', 'asking', 'checking')
		ORDER BY team_id ASC, task_id ASC`,
	)
	if err != nil {
		return nil, fmt.Errorf("query tasks for restart recovery: %w", err)
	}
	defer closeRows(ctx, rows)

	var out []recoveryCandidate
	for rows.Next() {
		var c recoveryCandidate
		var s string
		if err := rows.Scan(&c.TeamID, &c.TaskID, &s); err != nil {
			return nil, fmt.Errorf("scan recovery candidate: %w", err)
		}
		c.PreviousState = TaskState(s)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recovery candidates: %w", err)
	}
	return out, nil
}

func markTasksUnknownTx(ctx context.Context, tx *sql.Tx, candidates []recoveryCandidate, now int64) ([]RecoveredTask, error) {
	lastSeqByTeam := make(map[string]int64)
	recovered := make([]RecoveredTask, 0, len(candidates))
	for _, c := range candidates {
		seq, ok := lastSeqByTeam[c.TeamID]
		if !ok {
			if err := tx.QueryRowContext(ctx, `SELECT last_seq FROM team_records WHERE team_id = ?`, c.TeamID).Scan(&seq); err != nil {
				return nil, fmt.Errorf("read team sequence during recovery: %w", err)
			}
		}
		seq++
		lastSeqByTeam[c.TeamID] = seq
		eventID := fmt.Sprintf("recover:%s:%s:%d", c.TeamID, c.TaskID, seq)
		if err := applyRecoveredTaskRow(ctx, tx, c, seq, eventID, now); err != nil {
			return nil, err
		}
		recovered = append(recovered, RecoveredTask{
			TeamID:        c.TeamID,
			TaskID:        c.TaskID,
			PreviousState: c.PreviousState,
			Sequence:      seq,
		})
	}
	for teamID, seq := range lastSeqByTeam {
		if _, err := tx.ExecContext(ctx, `
			UPDATE team_records SET last_seq = ?, updated_at = ? WHERE team_id = ?`,
			seq, now, teamID,
		); err != nil {
			return nil, fmt.Errorf("update recovered team last_seq: %w", err)
		}
	}
	return recovered, nil
}

func applyRecoveredTaskRow(ctx context.Context, tx *sql.Tx, c recoveryCandidate, seq int64, eventID string, now int64) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, ?, ?, ?, ?, '', ?, ?, '', NULL, ?)`,
		c.TeamID, seq, eventID, c.TaskID, string(EventTaskMarkedUnknown),
		string(c.PreviousState), string(TaskStateUnknown), now,
	); err != nil {
		return fmt.Errorf("insert TaskMarkedUnknown event: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE team_tasks SET state = ?, last_event_seq = ?, updated_at = ?
		WHERE team_id = ? AND task_id = ?`,
		string(TaskStateUnknown), seq, now, c.TeamID, c.TaskID,
	); err != nil {
		return fmt.Errorf("mark task unknown: %w", err)
	}
	return nil
}
