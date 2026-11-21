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

// teamQueryer abstracts the database and transaction handles, so a snapshot
// can read the team record and its tasks in one consistent view.
type teamQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// TeamSnapshot reads the metadata-only team record and all projected tasks for teamID.
// Both queries run in one read transaction, so they always describe the same
// committed version even when an append commits concurrently. It succeeds even
// when the vault is locked (INV-09).
func (s *Store) TeamSnapshot(ctx context.Context, teamID string) (TeamRecord, []TaskRecord, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TeamRecord{}, nil, fmt.Errorf("%w: begin team snapshot: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	rec, err := queryTeamRecord(ctx, tx, teamID)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	tasks, err := queryTeamTasks(ctx, tx, teamID)
	if err != nil {
		return TeamRecord{}, nil, err
	}
	return rec, tasks, nil
}

func queryTeamRecord(ctx context.Context, db teamQueryer, teamID string) (TeamRecord, error) {
	var rec TeamRecord
	var stateStr string
	var endedAt sql.NullInt64
	err := db.QueryRowContext(ctx, `
		SELECT team_id, workspace_id, session_id, state, waiting_reason, last_seq, task_count,
		       failure_code, created_at, updated_at, ended_at
		FROM team_records WHERE team_id = ?`, teamID,
	).Scan(
		&rec.TeamID, &rec.WorkspaceID, &rec.SessionID, &stateStr, &rec.WaitingReason,
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

func queryTeamTasks(ctx context.Context, db teamQueryer, teamID string) ([]TaskRecord, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT task_id, team_id, parent_task_id, hierarchy_role, catalog_role,
		       deliverable, depends_on_json, depth, state, failure_code,
		       last_event_seq, created_at, updated_at, ended_at, session_id, explicit_start,
		       no_report_nudged, attempt, retry_of
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
		&t.LastEventSeq, &t.CreatedAt, &t.UpdatedAt, &endedAt, &t.SessionID, &t.ExplicitStart,
		&t.NoReportNudged, &t.Attempt, &t.RetryOf,
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

// ProposalItemID returns the transcript item ID of the session's
// team_proposal item for proposalID, newest first. The lookup goes through
// the idx_vendor_transcript_items_proposal expression index (0011), so it
// does not depend on how many items the session recorded since the proposal
// (F11.2a review: a pending proposal must stay reachable for the whole life
// of a long session). ok is false when no item was recorded yet.
func (s *Store) ProposalItemID(ctx context.Context, sessionID, proposalID string) (string, bool, error) {
	var itemID string
	err := s.db.QueryRowContext(ctx, `
		SELECT item_id FROM vendor_transcript_items
		WHERE session_id = ? AND kind = ?
		  AND json_extract(metadata, '$.proposal_id') = ?
		ORDER BY sequence DESC LIMIT 1`,
		sessionID, string(KindTeamProposal), proposalID,
	).Scan(&itemID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: query the proposal item: %w", ErrUnavailable, err)
	}
	return itemID, true, nil
}

// PendingTeamForSession returns the still-proposed team a session owns, if any
// (F11.2a, Q-103: one pending proposal per session). A session can own at most
// one because proposing supersedes the previous pending team first.
func (s *Store) PendingTeamForSession(ctx context.Context, sessionID string) (TeamRecord, bool, error) {
	var teamID, stateStr string
	err := s.db.QueryRowContext(ctx, `
		SELECT team_id, state FROM team_records
		WHERE session_id = ? AND state = ?
		ORDER BY created_at DESC, team_id DESC LIMIT 1`,
		sessionID, string(TeamStateProposed),
	).Scan(&teamID, &stateStr)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamRecord{}, false, nil
	}
	if err != nil {
		return TeamRecord{}, false, fmt.Errorf("%w: query pending team: %w", ErrUnavailable, err)
	}
	rec, err := queryTeamRecord(ctx, s.db, teamID)
	if err != nil {
		return TeamRecord{}, false, err
	}
	return rec, true, nil
}

// ActiveTeamForSession returns one active team the session owns, if any
// (F11.2b: fugue_team_amend is offered only to the lead of an active team).
// A session can own several active teams, so the amend call carries the team
// ID; this query only gates whether the tool is offered at all.
func (s *Store) ActiveTeamForSession(ctx context.Context, sessionID string) (TeamRecord, bool, error) {
	var teamID string
	err := s.db.QueryRowContext(ctx, `
		SELECT team_id FROM team_records
		WHERE session_id = ? AND state = ?
		ORDER BY created_at DESC, team_id DESC LIMIT 1`,
		sessionID, string(TeamStateActive),
	).Scan(&teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return TeamRecord{}, false, nil
	}
	if err != nil {
		return TeamRecord{}, false, fmt.Errorf("%w: query the active team: %w", ErrUnavailable, err)
	}
	rec, err := queryTeamRecord(ctx, s.db, teamID)
	if err != nil {
		return TeamRecord{}, false, err
	}
	return rec, true, nil
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
	// Unwrap outside WithUnlocked: the unwrap calls back into the vault, whose
	// OpenRecord takes the same read lock WithUnlocked holds. Nested read locks
	// block once a writer waits, so a concurrent Lock would deadlock both.
	rawKey, err := s.unwrapTeamKey(teamID, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("open team payload: %w", err)
	}
	defer clear(rawKey)
	var plaintext []byte
	err = s.vault.WithUnlocked(snap, func() error {
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

// ActiveTeamIDs lists the teams in the active state, oldest first. The
// dispatcher walks them when the vault unlocks (F11.3a).
func (s *Store) ActiveTeamIDs(ctx context.Context) ([]string, error) {
	return s.TeamIDsByState(ctx, TeamStateActive)
}

// AllTeamIDs lists every team ID, oldest first (F11.10b: the host registers a
// `team:<id>` channel topic for each team at startup, ended teams included —
// their channel stays readable).
func (s *Store) AllTeamIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id FROM team_records ORDER BY created_at ASC, team_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("%w: query all teams: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%w: scan the team ids: %w", ErrUnavailable, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate the team ids: %w", ErrUnavailable, err)
	}
	return out, nil
}

// TeamIDsByState lists the team IDs currently in state, oldest first.
func (s *Store) TeamIDsByState(ctx context.Context, state TeamState) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT team_id FROM team_records WHERE state = ? ORDER BY created_at ASC, team_id ASC`, string(state))
	if err != nil {
		return nil, fmt.Errorf("%w: query teams by state: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan team by state: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate teams by state: %w", err)
	}
	return out, nil
}

// TaskForSession returns the team task a session runs, if any. It reads the
// store, never the caller's claims: a session's team role is what its task
// row says (Q-117, authorization in Go).
func (s *Store) TaskForSession(ctx context.Context, sessionID string) (TaskRecord, bool, error) {
	if sessionID == "" {
		return TaskRecord{}, false, nil
	}
	var teamID, taskID string
	err := s.db.QueryRowContext(ctx,
		`SELECT team_id, task_id FROM team_tasks WHERE session_id = ? ORDER BY created_at DESC, task_id DESC LIMIT 1`,
		sessionID).Scan(&teamID, &taskID)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, false, nil
	}
	if err != nil {
		return TaskRecord{}, false, fmt.Errorf("%w: query the session task: %w", ErrUnavailable, err)
	}
	_, tasks, err := s.TeamSnapshot(ctx, teamID)
	if err != nil {
		return TaskRecord{}, false, err
	}
	for i := range tasks {
		if tasks[i].TaskID == taskID {
			return tasks[i], true, nil
		}
	}
	return TaskRecord{}, false, nil
}

// MarkTaskNoReportNudged atomically records the one-time no-report nudge on a
// task row, returning true when this call flipped it from false to true.
func (s *Store) MarkTaskNoReportNudged(ctx context.Context, teamID, taskID string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE team_tasks SET no_report_nudged = 1 WHERE team_id = ? AND task_id = ? AND no_report_nudged = 0`,
		teamID, taskID)
	if err != nil {
		return false, fmt.Errorf("%w: mark task no_report_nudged: %w", ErrUnavailable, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("%w: count task no_report_nudged: %w", ErrUnavailable, err)
	}
	return rows > 0, nil
}

// CountTeamNotes returns how many TeamNoted events the team has recorded.
func (s *Store) CountTeamNotes(ctx context.Context, teamID string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM team_task_events WHERE team_id = ? AND event_type = ?`,
		teamID, string(EventTeamNoted)).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("%w: count team notes: %w", ErrUnavailable, err)
	}
	return count, nil
}

// OpenTeamNotePayloads opens the sealed TeamNoted payloads of teamID in
// append order, returning the requested page and the total note count.
func (s *Store) OpenTeamNotePayloads(ctx context.Context, teamID string, offset, limit int) ([][]byte, int, error) {
	total, err := s.CountTeamNotes(ctx, teamID)
	if err != nil {
		return nil, 0, err
	}
	if limit <= 0 || offset >= total {
		return nil, total, nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence FROM team_task_events
		 WHERE team_id = ? AND event_type = ?
		 ORDER BY sequence ASC LIMIT ? OFFSET ?`,
		teamID, string(EventTeamNoted), limit, offset)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: query team notes: %w", ErrUnavailable, err)
	}
	var seqs []int64
	for rows.Next() {
		var seq int64
		if scanErr := rows.Scan(&seq); scanErr != nil {
			closeRows(ctx, rows)
			return nil, 0, fmt.Errorf("scan team note: %w", scanErr)
		}
		seqs = append(seqs, seq)
	}
	if err := rows.Err(); err != nil {
		closeRows(ctx, rows)
		return nil, 0, fmt.Errorf("iterate team notes: %w", err)
	}
	closeRows(ctx, rows)

	out := make([][]byte, 0, len(seqs))
	for _, seq := range seqs {
		plain, openErr := s.OpenTeamEventPayload(ctx, teamID, seq)
		if openErr != nil {
			return nil, 0, openErr
		}
		out = append(out, plain)
	}
	return out, total, nil
}

// OpenLatestTaskEventPayload opens the latest sealed payload of eventType for
// a task in teamID, or returns found=false when none exists.
func (s *Store) OpenLatestTaskEventPayload(ctx context.Context, teamID, taskID string, eventType TeamEventType) ([]byte, bool, error) {
	plain, _, found, err := s.OpenLatestTaskEventPayloadWithSeq(ctx, teamID, taskID, eventType)
	return plain, found, err
}

// OpenLatestTaskEventPayloadWithSeq opens the latest sealed payload of
// eventType for a task in teamID and also returns the event sequence.
func (s *Store) OpenLatestTaskEventPayloadWithSeq(ctx context.Context, teamID, taskID string, eventType TeamEventType) ([]byte, int64, bool, error) {
	var seq int64
	var hasPayload int
	err := s.db.QueryRowContext(ctx,
		`SELECT sequence, (payload_envelope IS NOT NULL) FROM team_task_events
		 WHERE team_id = ? AND task_id = ? AND event_type = ?
		 ORDER BY sequence DESC LIMIT 1`,
		teamID, taskID, string(eventType)).Scan(&seq, &hasPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, 0, false, nil
	}
	if err != nil {
		return nil, 0, false, fmt.Errorf("%w: query latest task event: %w", ErrUnavailable, err)
	}
	if hasPayload == 0 {
		return nil, seq, false, nil
	}
	plain, err := s.OpenTeamEventPayload(ctx, teamID, seq)
	if err != nil {
		return nil, 0, false, err
	}
	return plain, seq, true, nil
}

// CountSessionTurns returns the total number of vendor turns started on sessionID.
func (s *Store) CountSessionTurns(ctx context.Context, sessionID string) (int, error) {
	if sessionID == "" {
		return 0, nil
	}
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM vendor_turns WHERE session_id = ?`, sessionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("%w: count session turns: %w", ErrUnavailable, err)
	}
	return count, nil
}
