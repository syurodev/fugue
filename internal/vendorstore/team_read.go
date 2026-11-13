package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
)

// TeamSnapshot reads the metadata-only team record and all projected tasks for teamID.
// It succeeds even when the vault is locked (INV-09).
func (s *Store) TeamSnapshot(ctx context.Context, teamID string) (TeamRecord, []TaskRecord, error) {
	rec, err := queryTeamRecord(ctx, s.db, teamID)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	tasks, err := queryTeamTasks(ctx, s.db, teamID)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	return rec, tasks, nil
}

func queryTeamRecord(ctx context.Context, db *sql.DB, teamID string) (TeamRecord, error) {
	var rec TeamRecord
	var stateStr string
	var endedAt sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT team_id, workspace_id, session_id, state, last_seq, task_count,
		       failure_code, created_at, updated_at, ended_at
		FROM team_records WHERE team_id = ?`, teamID,
	).Scan(
		&rec.TeamID, &rec.WorkspaceID, &rec.SessionID, &stateStr,
		&rec.LastSeq, &rec.TaskCount, &rec.FailureCode,
		&rec.CreatedAt, &rec.UpdatedAt, &endedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamRecord{}, fmt.Errorf("%w: %s", ErrTeamNotFound, teamID)
	}
	if err != nil {
		return TeamRecord{}, fmt.Errorf("query team snapshot: %w", err)
	}
	rec.State = TeamState(stateStr)
	if endedAt.Valid {
		rec.EndedAt = endedAt.Int64
	}
	return rec, nil
}

func queryTeamTasks(ctx context.Context, db *sql.DB, teamID string) ([]TaskRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT task_id, team_id, parent_task_id, hierarchy_role, catalog_role,
		       deliverable, depends_on_json, depth, state, failure_code,
		       last_event_seq, created_at, updated_at, ended_at
		FROM team_tasks WHERE team_id = ? ORDER BY created_at ASC, task_id ASC`, teamID,
	)
	if err != nil {
		return nil, fmt.Errorf("query team tasks: %w", err)
	}
	defer closeRows(ctx, rows)

	var out []TaskRecord
	for rows.Next() {
		task, scanErr := scanTaskRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, task)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate team tasks: %w", err)
	}
	return out, nil
}

func scanTaskRow(rows *sql.Rows) (TaskRecord, error) {
	var t TaskRecord
	var roleStr, delivStr, depsJSON, stateStr string
	var endedAt sql.NullInt64
	if err := rows.Scan(
		&t.TaskID, &t.TeamID, &t.ParentTaskID, &roleStr, &t.CatalogRole,
		&delivStr, &depsJSON, &t.Depth, &stateStr, &t.FailureCode,
		&t.LastEventSeq, &t.CreatedAt, &t.UpdatedAt, &endedAt,
	); err != nil {
		return TaskRecord{}, fmt.Errorf("scan team task: %w", err)
	}
	t.HierarchyRole = TeamRole(roleStr)
	t.Deliverable = TeamDeliverable(delivStr)
	t.State = TaskState(stateStr)
	if err := json.Unmarshal([]byte(depsJSON), &t.DependsOn); err != nil {
		return TaskRecord{}, fmt.Errorf("decode depends_on: %w", err)
	}
	if endedAt.Valid {
		t.EndedAt = endedAt.Int64
	}
	return t, nil
}

// TeamEventsSince reads metadata-only event records with sequence > afterSeq.
func (s *Store) TeamEventsSince(ctx context.Context, teamID string, afterSeq int64, limit int) ([]RecordedTeamEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT team_id, sequence, event_id, task_id, event_type, outcome,
		       from_state, to_state, failure_code, payload_envelope IS NOT NULL, created_at
		FROM team_task_events
		WHERE team_id = ? AND sequence > ?
		ORDER BY sequence ASC LIMIT ?`,
		teamID, afterSeq, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("query team events: %w", err)
	}
	defer closeRows(ctx, rows)

	var events []RecordedTeamEvent
	for rows.Next() {
		var ev RecordedTeamEvent
		var typeStr string
		if err := rows.Scan(
			&ev.TeamID, &ev.Sequence, &ev.EventID, &ev.TaskID, &typeStr, &ev.Outcome,
			&ev.FromState, &ev.ToState, &ev.FailureCode, &ev.HasPayload, &ev.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan team event: %w", err)
		}
		ev.EventType = TeamEventType(typeStr)
		events = append(events, ev)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate team events: %w", err)
	}
	return events, nil
}

// OpenTaskSpec decrypts a task's sealed protected specification content.
// Fails closed with vault.ErrLocked when the vault is not unlocked.
func (s *Store) OpenTaskSpec(ctx context.Context, teamID, taskID string) ([]byte, error) {
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return nil, err
	}
	var keyID string
	var wrapped, envelope []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT r.key_id, r.wrapped_key, t.spec_envelope
		FROM team_tasks t
		JOIN team_records r ON r.team_id = t.team_id
		WHERE t.team_id = ? AND t.task_id = ?`,
		teamID, taskID,
	).Scan(&keyID, &wrapped, &envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s", ErrTaskNotFound, taskID)
	}
	if err != nil {
		return nil, fmt.Errorf("query sealed task spec: %w", err)
	}
	return s.openTeamCiphertext(snap, teamID, keyID, wrapped, teamSpecRecordType, taskID, "spec", envelope)
}

// OpenTeamEventPayload decrypts a sealed event payload while requiring an unlocked vault.
func (s *Store) OpenTeamEventPayload(ctx context.Context, teamID string, sequence int64) ([]byte, error) {
	snap, err := s.requireUnlockedVault()
	if err != nil {
		return nil, err
	}
	var keyID, eventID, eventType string
	var wrapped, envelope []byte
	err = s.db.QueryRowContext(ctx, `
		SELECT r.key_id, r.wrapped_key, e.event_id, e.event_type, e.payload_envelope
		FROM team_task_events e
		JOIN team_records r ON r.team_id = e.team_id
		WHERE e.team_id = ? AND e.sequence = ?`,
		teamID, sequence,
	).Scan(&keyID, &wrapped, &eventID, &eventType, &envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("%w: %s seq %d", ErrTeamNotFound, teamID, sequence)
	}
	if err != nil {
		return nil, fmt.Errorf("query sealed event payload: %w", err)
	}
	if len(envelope) == 0 {
		return nil, nil
	}
	return s.openTeamCiphertext(snap, teamID, keyID, wrapped, teamEventRecordType, eventID, eventType, envelope)
}

func (s *Store) openTeamCiphertext(
	snap vault.Snapshot,
	teamID, keyID string,
	wrapped []byte,
	recordType, payloadID, kind string,
	envelope []byte,
) ([]byte, error) {
	var plaintext []byte
	err := s.vault.WithUnlocked(snap, func() error {
		rawKey, unwrapErr := s.unwrapTeamKey(teamID, keyID, wrapped)
		if unwrapErr != nil {
			return unwrapErr
		}
		opened, openErr := sealing.OpenPayload(recordType, teamID, payloadID, kind, keyID, rawKey, envelope)
		if openErr != nil {
			return fmt.Errorf("open sealed payload: %w", openErr)
		}
		plaintext = opened
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("open team payload: %w", err)
	}
	return plaintext, nil
}
