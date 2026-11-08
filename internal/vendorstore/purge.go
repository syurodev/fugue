package vendorstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// PurgeSession permanently revokes the session key and removes its records.
// Historical backups and filesystem snapshots are outside crypto-shred's scope.
func (s *Store) PurgeSession(ctx context.Context, session string) error {
	if len(session) == 0 || len(session) > 128 {
		return errcode.New(errcode.InvalidArgument, "invalid session ID")
	}
	s.prepare.Lock()
	defer s.prepare.Unlock()
	s.cache.BeginPurge(session)
	permanent := false
	defer func() { s.cache.EndPurge(session, permanent) }()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeConn(ctx, conn)
	if _, pragmaErr := conn.ExecContext(ctx, "PRAGMA secure_delete=ON"); pragmaErr != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, pragmaErr)
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() {
		rollbackErr := tx.Rollback()
		if rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			// An unconfirmed rollback keeps the purge barrier up: fail closed.
			permanent = true
			slog.WarnContext(ctx, "vendor purge rollback failed", "err", rollbackErr.Error())
		}
	}()
	if writeErr := s.purgeRows(ctx, tx, session); writeErr != nil {
		return writeErr
	}
	if s.beforeCommit != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, s.beforeCommit)
	}
	if err = tx.Commit(); err != nil {
		permanent = true
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	permanent = true
	return nil
}

func (s *Store) purgeRows(ctx context.Context, tx *sql.Tx, session string) error {
	var err error
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	queries := []string{
		`INSERT INTO vendor_purges(session_id,requested_at) VALUES (?,?) ON CONFLICT DO NOTHING`,
		`UPDATE vendor_session_keys SET wrapped_key=zeroblob(length(wrapped_key)),shredded_at=? WHERE session_id=?`,
	}
	if _, err = tx.ExecContext(ctx, queries[0], session, now); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if _, err = tx.ExecContext(ctx, queries[1], now, session); err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	for _, query := range []string{
		`DELETE FROM vendor_transcript_items WHERE session_id=?`,
		`DELETE FROM vendor_events WHERE session_id=?`,
		`DELETE FROM vendor_turns WHERE session_id=?`,
		`DELETE FROM vendor_payloads WHERE session_id=?`,
		`DELETE FROM vendor_session_keys WHERE session_id=?`,
		`DELETE FROM vendor_sessions WHERE session_id=?`,
	} {
		if _, err = tx.ExecContext(ctx, query, session); err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return nil
}
