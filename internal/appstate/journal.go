package appstate

// The change journal behind the durable `appstate` topic (host-api §5.2,
// §6): every committed document change writes one row carrying identity and
// revision metadata only — never a document — in the same transaction, so
// snapshot and catch-up are exact against the store.

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// The journal row kinds; they are the `appstate` topic's event types.
const (
	changeWindowPut      = "window_put"
	changeWindowDeleted  = "window_deleted"
	changePreferencesPut = "preferences_put"
)

// JournalRetention is the newest number of journal rows the store keeps:
// every append prunes the rows beyond it in the same transaction, so the
// journal is bounded no matter how often the app saves. AUTOINCREMENT keeps
// the sequence monotonic across pruning, so a cursor never regresses; a
// cursor older than the oldest kept row is a gap and fails typed instead of
// catching up over silent holes.
const JournalRetention = 4096

// Change is one journal row.
type Change struct {
	Sequence int64
	Kind     string
	WindowID string
	Revision uint64
}

// Snapshot is one consistent view of every stored document and the journal
// sequence that produced it.
type Snapshot struct {
	Windows     []Window
	Preferences Preferences
	Sequence    int64
}

// appendChange writes one journal row and prunes the journal, inside the
// caller's transaction, and returns the row's sequence for the publish.
func appendChange(ctx context.Context, tx *sql.Tx, kind, windowID string, revision uint64, now time.Time) (int64, error) {
	res, err := tx.ExecContext(ctx,
		`INSERT INTO app_state_events (kind, window_id, revision, recorded_at) VALUES (?, ?, ?, ?)`,
		kind, windowID, revision, now.UnixMilli())
	if err != nil {
		return 0, writeFailed("append the app-state change", err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM app_state_events WHERE seq NOT IN (SELECT seq FROM app_state_events ORDER BY seq DESC LIMIT ?)`,
		JournalRetention); err != nil {
		return 0, writeFailed("prune the app-state journal", err)
	}
	seq, err := res.LastInsertId()
	if err != nil {
		return 0, writeFailed("read the app-state change sequence", err)
	}
	return seq, nil
}

// ChangesSince returns every journal row after seq, oldest first. A cursor
// older than the oldest retained row fails with ErrChangeCursorTooOld.
func (s *Store) ChangesSince(ctx context.Context, seq int64) ([]Change, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, readFailed("begin the app-state catch-up", err)
	}
	defer rollback(ctx, tx)
	var oldest sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MIN(seq) FROM app_state_events`).Scan(&oldest); err != nil {
		return nil, readFailed("read the app-state journal oldest row", err)
	}
	if oldest.Valid && seq+1 < oldest.Int64 {
		return nil, ErrChangeCursorTooOld
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT seq, kind, window_id, revision FROM app_state_events WHERE seq > ? ORDER BY seq`, seq)
	if err != nil {
		return nil, readFailed("read the app-state journal", err)
	}
	defer rows.Close()
	out := []Change{}
	for rows.Next() {
		var c Change
		if err := rows.Scan(&c.Sequence, &c.Kind, &c.WindowID, &c.Revision); err != nil {
			return nil, readFailed("scan an app-state change", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("read the app-state journal", err)
	}
	return out, nil
}

// Snapshot returns every stored document and the journal sequence that
// reflects it, read in one transaction so the two can never disagree.
func (s *Store) Snapshot(ctx context.Context) (Snapshot, error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Snapshot{}, readFailed("begin the app-state snapshot", err)
	}
	defer rollback(ctx, tx)
	snap := Snapshot{Windows: []Window{}}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM app_state_events`).Scan(&snap.Sequence); err != nil {
		return Snapshot{}, readFailed("read the app-state journal sequence", err)
	}
	windows, err := snapshotWindows(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Windows = windows
	prefs, err := snapshotPreferences(ctx, tx)
	if err != nil {
		return Snapshot{}, err
	}
	snap.Preferences = prefs
	return snap, nil
}

func snapshotWindows(ctx context.Context, tx *sql.Tx) ([]Window, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT window_id, revision, layout, updated_at FROM app_windows ORDER BY window_id`)
	if err != nil {
		return nil, readFailed("read the app windows", err)
	}
	defer rows.Close()
	out := []Window{}
	for rows.Next() {
		w, scanErr := scanWindow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("read the app windows", err)
	}
	return out, nil
}

func snapshotPreferences(ctx context.Context, tx *sql.Tx) (Preferences, error) {
	var (
		p         Preferences
		document  []byte
		updatedAt sql.NullInt64
	)
	err := tx.QueryRowContext(ctx,
		`SELECT revision, preferences, updated_at FROM app_preferences WHERE id = 1`).
		Scan(&p.Revision, &document, &updatedAt)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return Preferences{}, nil
	case err != nil:
		return Preferences{}, readFailed("read the app preferences", err)
	}
	p.Document = document
	if updatedAt.Valid {
		p.UpdatedAt = time.UnixMilli(updatedAt.Int64).UTC()
	}
	return p, nil
}
