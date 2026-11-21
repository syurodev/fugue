package appstate

// The SQL store for app_windows and app_preferences (host-api §5.2). One
// mutating transaction writes the document row and its journal entry
// together, so a snapshot of the documents and the journal sequence that
// follows it can never disagree. CAS: expected 0 creates, anything else must
// equal the stored revision, and a mismatch fails with the current revision.

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
)

// Window is one stored window layout.
type Window struct {
	WindowID  string
	Revision  uint64
	Layout    []byte
	UpdatedAt time.Time
}

// Preferences is the one preferences document the whole app shares.
// Revision 0 with an empty Document means the app never saved any.
type Preferences struct {
	Revision  uint64
	Document  []byte
	UpdatedAt time.Time
}

// Store owns the app_windows, app_preferences and app_state_events tables.
type Store struct {
	db *sql.DB
}

// NewStore returns a store over db.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// ListWindows returns every saved window ordered by window_id.
func (s *Store) ListWindows(ctx context.Context) ([]Window, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT window_id, revision, layout, updated_at FROM app_windows ORDER BY window_id`)
	if err != nil {
		return nil, readFailed("list the app windows", err)
	}
	defer closeRows(ctx, rows)
	out := []Window{}
	for rows.Next() {
		w, scanErr := scanWindow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, readFailed("list the app windows", err)
	}
	return out, nil
}

type rowScanner interface{ Scan(dest ...any) error }

func scanWindow(sc rowScanner) (Window, error) {
	var (
		w         Window
		layout    []byte
		updatedAt int64
	)
	if err := sc.Scan(&w.WindowID, &w.Revision, &layout, &updatedAt); err != nil {
		return Window{}, readFailed("scan an app window", err)
	}
	w.Layout = layout
	w.UpdatedAt = time.UnixMilli(updatedAt).UTC()
	return w, nil
}

// PutWindow stores layout under CAS: expected 0 creates the window, anything
// else must equal its stored revision. It returns the new revision and the
// journal sequence to publish.
func (s *Store) PutWindow(ctx context.Context, windowID string, expected uint64, layout []byte, now time.Time) (uint64, int64, error) {
	if !ids.IsUUID(windowID) {
		return 0, 0, ErrWindowIDInvalid
	}
	if err := ValidateWindowLayout(layout); err != nil {
		return 0, 0, err
	}
	return mutateStore(ctx, s.db, now, changeWindowPut, windowID, func(tx *sql.Tx) (uint64, error) {
		return putWindowRow(ctx, tx, windowID, expected, layout, now)
	})
}

// DeleteWindow deletes one window under CAS: expected must equal its stored
// revision, so the app that saw the window last wins the delete.
func (s *Store) DeleteWindow(ctx context.Context, windowID string, expected uint64, now time.Time) (int64, error) {
	if !ids.IsUUID(windowID) {
		return 0, ErrWindowIDInvalid
	}
	_, seq, err := mutateStore(ctx, s.db, now, changeWindowDeleted, windowID, func(tx *sql.Tx) (uint64, error) {
		return deleteWindowRow(ctx, tx, windowID, expected)
	})
	return seq, err
}

// Preferences returns the stored preferences document; Revision 0 means the
// app never saved any.
func (s *Store) Preferences(ctx context.Context) (Preferences, error) {
	var (
		p         Preferences
		document  []byte
		updatedAt sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx,
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

// PutPreferences stores the preferences document under CAS, like PutWindow.
func (s *Store) PutPreferences(ctx context.Context, expected uint64, prefs []byte, now time.Time) (uint64, int64, error) {
	if err := ValidatePreferences(prefs); err != nil {
		return 0, 0, err
	}
	return mutateStore(ctx, s.db, now, changePreferencesPut, "", func(tx *sql.Tx) (uint64, error) {
		return putPreferencesRow(ctx, tx, expected, prefs, now)
	})
}

// mutateStore runs one document mutation and its journal entry in one
// transaction, so snapshot and catch-up are exact against the store. It
// returns the new revision and the journal sequence to publish.
func mutateStore(ctx context.Context, db *sql.DB, now time.Time, kind, windowID string, write func(tx *sql.Tx) (uint64, error)) (uint64, int64, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, writeFailed("begin the app state write", err)
	}
	defer rollback(ctx, tx)
	revision, err := write(tx)
	if err != nil {
		return 0, 0, err
	}
	seq, err := appendChange(ctx, tx, kind, windowID, revision, now)
	if err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, writeFailed("commit the app state write", err)
	}
	return revision, seq, nil
}

// putWindowRow applies one CAS write inside the caller's transaction and
// returns the new revision.
func putWindowRow(ctx context.Context, tx *sql.Tx, windowID string, expected uint64, layout []byte, now time.Time) (uint64, error) {
	current, found, err := windowRevision(ctx, tx, windowID)
	if err != nil {
		return 0, err
	}
	switch {
	case !found:
		return insertWindowRow(ctx, tx, windowID, expected, layout, now)
	case current != expected:
		return 0, newConflict(current)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE app_windows SET revision = revision + 1, layout = ?, updated_at = ? WHERE window_id = ?`,
		layout, now.UnixMilli(), windowID); err != nil {
		return 0, writeFailed("update the app window", err)
	}
	return current + 1, nil
}

// insertWindowRow creates one window, unless the caller expected an existing
// revision or the saved-window cap of host-api §5.2 is already reached.
func insertWindowRow(ctx context.Context, tx *sql.Tx, windowID string, expected uint64, layout []byte, now time.Time) (uint64, error) {
	if expected != 0 {
		return 0, newConflict(0)
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_windows`).Scan(&count); err != nil {
		return 0, writeFailed("count the app windows", err)
	}
	if count >= MaxWindows {
		return 0, ErrTooManyWindows
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO app_windows (window_id, revision, layout, created_at, updated_at) VALUES (?, 1, ?, ?, ?)`,
		windowID, layout, now.UnixMilli(), now.UnixMilli()); err != nil {
		return 0, writeFailed("insert the app window", err)
	}
	return 1, nil
}

// deleteWindowRow deletes one window row inside the caller's transaction and
// returns the revision it had when deleted.
func deleteWindowRow(ctx context.Context, tx *sql.Tx, windowID string, expected uint64) (uint64, error) {
	current, found, err := windowRevision(ctx, tx, windowID)
	switch {
	case err != nil:
		return 0, err
	case !found || current != expected:
		return 0, newConflict(current)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM app_windows WHERE window_id = ?`, windowID); err != nil {
		return 0, writeFailed("delete the app window", err)
	}
	return current, nil
}

// putPreferencesRow applies one CAS write to the single preferences row.
func putPreferencesRow(ctx context.Context, tx *sql.Tx, expected uint64, prefs []byte, now time.Time) (uint64, error) {
	current, found, err := preferencesRevision(ctx, tx)
	if err != nil {
		return 0, err
	}
	switch {
	case !found:
		return insertPreferencesRow(ctx, tx, expected, prefs, now)
	case current != expected:
		return 0, newConflict(current)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE app_preferences SET revision = revision + 1, preferences = ?, updated_at = ? WHERE id = 1`,
		prefs, now.UnixMilli()); err != nil {
		return 0, writeFailed("update the app preferences", err)
	}
	return current + 1, nil
}

func insertPreferencesRow(ctx context.Context, tx *sql.Tx, expected uint64, prefs []byte, now time.Time) (uint64, error) {
	if expected != 0 {
		return 0, newConflict(0)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO app_preferences (id, revision, preferences, updated_at) VALUES (1, 1, ?, ?)`,
		prefs, now.UnixMilli()); err != nil {
		return 0, writeFailed("insert the app preferences", err)
	}
	return 1, nil
}

// windowRevision reads the current revision of one window; found reports
// whether the row exists. The schema CHECK keeps revisions >= 1, so a
// smaller value is a broken database and fails typed instead of converting.
func windowRevision(ctx context.Context, tx *sql.Tx, windowID string) (uint64, bool, error) {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM app_windows WHERE window_id = ?`, windowID).Scan(&current)
	return checkedRevision(err, current, "read the app window revision")
}

func preferencesRevision(ctx context.Context, tx *sql.Tx) (uint64, bool, error) {
	var current int64
	err := tx.QueryRowContext(ctx, `SELECT revision FROM app_preferences WHERE id = 1`).Scan(&current)
	return checkedRevision(err, current, "read the app preferences revision")
}

func checkedRevision(err error, current int64, what string) (uint64, bool, error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, writeFailed(what, err)
	case current < 1:
		return 0, false, errcode.New(errcode.Internal, what+" below 1")
	}
	return uint64(current), true, nil
}

func rollback(ctx context.Context, tx *sql.Tx) {
	// sql.ErrTxDone means it already committed or rolled back, which is the
	// normal path of a deferred call; any other failure is logged without
	// payload because the caller already has its own result.
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		slog.WarnContext(ctx, "app state rollback failed", "err", err.Error())
	}
}

func closeRows(ctx context.Context, rows *sql.Rows) {
	if err := rows.Close(); err != nil {
		slog.WarnContext(ctx, "app state rows close failed", "err", err.Error())
	}
}

func readFailed(what string, err error) error {
	return errcode.Wrap(errcode.Unavailable, what, err)
}

func writeFailed(what string, err error) error {
	return errcode.Wrap(errcode.Internal, what, err)
}
