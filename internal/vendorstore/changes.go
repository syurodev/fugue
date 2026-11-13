package vendorstore

import (
	"context"
	"database/sql"
	"fmt"
)

// The cross-session change journal behind the durable vendor-sessions topic
// (F9.9c, Q-58 A). Every session creation and every projected state change
// writes one row in the same transaction as the session row, so the topic's
// snapshot and catch-up are exact against the store.

// Change kinds.
const (
	ChangeCreated      = "created"
	ChangeStateChanged = "state_changed"
)

// SessionChange is one journal row: identity and state metadata only.
type SessionChange struct {
	Sequence      int64
	SessionID     string
	Kind          string
	State         string
	WaitingReason string
}

func recordChange(ctx context.Context, tx *sql.Tx, session, kind, state, waiting, now string) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO vendor_session_changes(session_id,kind,state,waiting_reason,recorded_at) VALUES (?,?,?,?,?)`,
		session, kind, state, waiting, now)
	if err != nil {
		return fmt.Errorf("%w: record session change: %w", ErrUnavailable, err)
	}
	return nil
}

// SessionChangesSince returns at most limit changes after seq, oldest first.
func (s *Store) SessionChangesSince(ctx context.Context, seq int64, limit int) ([]SessionChange, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT seq,session_id,kind,state,waiting_reason FROM vendor_session_changes WHERE seq>? ORDER BY seq LIMIT ?`,
		seq, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	out := []SessionChange{}
	for rows.Next() {
		var change SessionChange
		if err = rows.Scan(&change.Sequence, &change.SessionID, &change.Kind, &change.State, &change.WaitingReason); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		out = append(out, change)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return out, nil
}

// SessionList reads every session and the journal's last sequence in one read
// transaction, so a list plus catch-up from that sequence has no gap.
func (s *Store) SessionList(ctx context.Context) ([]Session, int64, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	var last int64
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq),0) FROM vendor_session_changes`).Scan(&last); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	rows, err := tx.QueryContext(ctx, sessionColumns+` FROM vendor_sessions ORDER BY created_at,session_id`)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	sessions := []Session{}
	for rows.Next() {
		var session Session
		if err = rows.Scan(scanSession(&session)...); err != nil {
			return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		sessions = append(sessions, session)
	}
	if err = rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return sessions, last, nil
}
