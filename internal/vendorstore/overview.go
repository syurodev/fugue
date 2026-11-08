package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// F9.4b read-only additions: the session list the transcript service registers
// topics for at startup, and one atomic session overview (session row, open
// turns, open transcript items) behind the durable topic snapshot. Only
// vendorstore reads vendor_* tables (ADR 0002 §5).

// openItemKinds are the item kinds whose stored metadata can describe a still
// open item; every other kind is complete once recorded (transcript §6).
const openItemKinds = `'tool_call','subagent','task_link','approval','team_proposal'`

// ListSessions returns every stored session, oldest first. The transcript
// service registers its topics for these at host startup (F9.4b).
func (s *Store) ListSessions(ctx context.Context) ([]Session, error) {
	rows, err := s.db.QueryContext(ctx, sessionColumns+` FROM vendor_sessions ORDER BY created_at,session_id`)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	result := []Session{}
	for rows.Next() {
		var session Session
		if err = rows.Scan(scanSession(&session)...); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		result = append(result, session)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}

// Overview reads the session row, its open turns and its open items in one
// read transaction, so the durable topic snapshot reflects exactly one store
// sequence. A session that does not exist reports ErrSessionNotFound.
func (s *Store) Overview(ctx context.Context, session string) (Session, []string, []StoredItem, error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	var result Session
	err = tx.QueryRowContext(ctx, sessionColumns+` FROM vendor_sessions WHERE session_id=?`,
		session).Scan(scanSession(&result)...)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, nil, nil, ErrSessionNotFound
	}
	if err != nil {
		return Session{}, nil, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	turns, err := openTurnIDs(ctx, tx, session)
	if err != nil {
		return Session{}, nil, nil, err
	}
	items, err := openItems(ctx, tx, session)
	if err != nil {
		return Session{}, nil, nil, err
	}
	return result, turns, items, nil
}

// sessionColumns is the shared session projection; scanSession matches its order.
const sessionColumns = `SELECT session_id,vendor,account_id,role,state,last_sequence,COALESCE(team_id,''),
	COALESCE(task_id,''),resume_handle,process_pid,process_start_token,process_scope_unit,
	approval_mode,policy_revision,waiting_reason,close_reason,stop_proof,created_at,updated_at`

// scanSession returns the scan destinations for a Session in sessionColumns order.
func scanSession(s *Session) []any {
	return []any{
		&s.ID, &s.Vendor, &s.AccountID, &s.Role, &s.State, &s.LastSequence,
		&s.TeamID, &s.TaskID, &s.ResumeHandle, &s.ProcessPID, &s.ProcessStartToken,
		&s.ProcessScopeUnit, &s.ApprovalMode, &s.PolicyRevision, &s.WaitingReason,
		&s.CloseReason, &s.StopProof, &s.CreatedAt, &s.UpdatedAt,
	}
}

// StoredItemMeta returns one item's projection without its protected content,
// so a locked-vault read stays metadata-only (transcript §6). An unknown item
// reports ErrItemNotFound.
func (s *Store) StoredItemMeta(ctx context.Context, session, id string) (StoredItem, error) {
	var item StoredItem
	var metadata string
	err := scanItem(s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM vendor_transcript_items WHERE session_id=? AND item_id=?`,
		session, id), &item, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredItem{}, ErrItemNotFound
	}
	if err != nil {
		return StoredItem{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err = json.Unmarshal([]byte(metadata), &item.Metadata); err != nil {
		return StoredItem{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return item, nil
}

// openTurnIDs returns the session's unended turn IDs, oldest first, inside the
// caller's transaction.
func openTurnIDs(ctx context.Context, tx *sql.Tx, session string) ([]string, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT turn_id FROM vendor_turns WHERE session_id=? AND outcome='' ORDER BY created_at,turn_id`,
		session)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var result []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		result = append(result, id)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}

// openItems returns the transcript items whose latest stored metadata still
// describes an open state: a tool call or subagent that is pending or running,
// a task link in an active lane, an approval without a decision and a team
// proposal still pending. The predicate lives in the metadata JSON, which the
// CHECK constraint guarantees is valid. Runs inside the caller's transaction.
func openItems(ctx context.Context, tx *sql.Tx, session string) ([]StoredItem, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT `+itemColumns+` FROM vendor_transcript_items
		WHERE session_id=? AND kind IN (`+openItemKinds+`) AND (
			kind IN ('tool_call','subagent') AND json_extract(metadata,'$.status') IN ('pending','running')
			OR kind='task_link' AND json_extract(metadata,'$.status') IN
				('queued','starting','running','asking','checking','review','integrating')
			OR kind='approval' AND json_extract(metadata,'$.decision')=''
			OR kind='team_proposal' AND json_extract(metadata,'$.decision')='pending'
		) ORDER BY sequence`, session)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	result := []StoredItem{}
	for rows.Next() {
		var item StoredItem
		var metadata string
		if err = scanItem(rows, &item, &metadata); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if decodeErr := json.Unmarshal([]byte(metadata), &item.Metadata); decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, decodeErr)
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}
