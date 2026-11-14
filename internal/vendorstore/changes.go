package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
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

// ChangeJournalRetention is the newest number of journal rows the store keeps
// (F9.9e): every insert prunes the rows beyond it in the same transaction, so
// the journal is bounded no matter how many sessions run. AUTOINCREMENT keeps
// the sequence monotonic across pruning and purging, so a client's cursor
// never regresses; a cursor older than the oldest kept row is a gap and fails
// typed instead of catching up over silent holes.
const ChangeJournalRetention = 4096

// ErrChangeCursorTooOld reports a catch-up from a cursor older than the oldest
// retained journal row: the events in between are gone and the caller must
// resynchronize from a snapshot. The topic layer maps it to the hidden
// resync signal, so no client sees this error raw.
var ErrChangeCursorTooOld = errcode.New(errcode.FailedPrecondition, "the vendor session change cursor is older than the retained journal")

// SessionChange is one journal row: identity and state metadata only.
type SessionChange struct {
	Sequence      int64
	SessionID     string
	Kind          string
	State         string
	WaitingReason string
}

// CommandRevision returns the newest durable vendor-sessions change for a session.
func (s *Store) CommandRevision(ctx context.Context, session string) (int64, error) {
	var revision int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(c.seq),0) FROM vendor_sessions s
		LEFT JOIN vendor_session_changes c ON c.session_id=s.session_id WHERE s.session_id=?`, session).Scan(&revision)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrSessionNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return revision, nil
}

func recordChange(ctx context.Context, tx *sql.Tx, session, kind, state, waiting, now string, retention int) error {
	_, err := tx.ExecContext(ctx,
		`INSERT INTO vendor_session_changes(session_id,kind,state,waiting_reason,recorded_at) VALUES (?,?,?,?,?)`,
		session, kind, state, waiting, now)
	if err != nil {
		return fmt.Errorf("%w: record session change: %w", ErrUnavailable, err)
	}
	return pruneChanges(ctx, tx, retention)
}

// pruneChanges keeps only the journal's newest retention rows. Every insert
// calls it in the insert's transaction, so a reader never sees the insert
// without the pruning or the other way round. A non-positive retention is a
// store misconfiguration, not a request to keep nothing.
func pruneChanges(ctx context.Context, tx *sql.Tx, retention int) error {
	if retention < 1 {
		retention = ChangeJournalRetention
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM vendor_session_changes WHERE seq IN (
			SELECT seq FROM vendor_session_changes ORDER BY seq DESC LIMIT -1 OFFSET ?)`,
		retention)
	if err != nil {
		return fmt.Errorf("%w: prune session changes: %w", ErrUnavailable, err)
	}
	return nil
}

// SessionChangesSince returns at most limit changes after seq, oldest first.
// A cursor older than the oldest retained row fails with ErrChangeCursorTooOld
// instead of catching up over a pruned gap.
func (s *Store) SessionChangesSince(ctx context.Context, seq int64, limit int) ([]SessionChange, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	var oldest sql.NullInt64
	if err = tx.QueryRowContext(ctx, `SELECT MIN(seq) FROM vendor_session_changes`).Scan(&oldest); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if oldest.Valid && seq+1 < oldest.Int64 {
		return nil, ErrChangeCursorTooOld
	}
	rows, err := tx.QueryContext(ctx,
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
