package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func recordPurgeTombstone(ctx context.Context, conn *sql.Conn, sessionID, now string) error {
	var purgeCount int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", sessionID).Scan(&purgeCount); err != nil {
		return fmt.Errorf("%w: check purges: %w", ErrStoreUnavailable, err)
	}
	if purgeCount == 0 {
		if _, err := conn.ExecContext(ctx, "INSERT INTO exec_purges (session_id, requested_at) VALUES (?, ?)", sessionID, now); err != nil {
			return fmt.Errorf("%w: insert exec_purges: %w", ErrStoreUnavailable, err)
		}
	}
	return nil
}

func shredSessionKey(ctx context.Context, conn *sql.Conn, sessionID, now string) error {
	var wrappedKeyLen int
	keyErr := conn.QueryRowContext(ctx, "SELECT length(wrapped_key) FROM exec_session_keys WHERE session_id = ?", sessionID).Scan(&wrappedKeyLen)
	if errors.Is(keyErr, sql.ErrNoRows) {
		return nil
	}
	if keyErr != nil {
		return fmt.Errorf("%w: query exec_session_keys for shred: %w", ErrStoreUnavailable, keyErr)
	}
	if wrappedKeyLen <= 0 {
		return nil
	}
	_, updateErr := conn.ExecContext(
		ctx,
		"UPDATE exec_session_keys SET wrapped_key = zeroblob(?), shredded_at = ? WHERE session_id = ?",
		wrappedKeyLen, now, sessionID,
	)
	if updateErr != nil {
		return fmt.Errorf("%w: shred exec_session_keys: %w", ErrStoreUnavailable, updateErr)
	}
	return nil
}

func deleteExecutionData(ctx context.Context, conn *sql.Conn, sessionID string) error {
	deleteQueries := []string{
		"DELETE FROM exec_transcript WHERE session_id = ?",
		"DELETE FROM exec_receipts WHERE session_id = ?",
		"DELETE FROM exec_attempts WHERE run_id IN (SELECT run_id FROM exec_runs WHERE session_id = ?)",
		"DELETE FROM exec_invocations WHERE run_id IN (SELECT run_id FROM exec_runs WHERE session_id = ?)",
		"DELETE FROM exec_events WHERE session_id = ?",
		"DELETE FROM exec_admission WHERE session_id = ?",
		"DELETE FROM exec_runs WHERE session_id = ?",
		"DELETE FROM exec_payloads WHERE session_id = ?",
		"DELETE FROM exec_session_keys WHERE session_id = ?",
	}

	for _, q := range deleteQueries {
		if _, err := conn.ExecContext(ctx, q, sessionID); err != nil {
			return fmt.Errorf("%w: purge query %q: %w", ErrStoreUnavailable, q, err)
		}
	}
	return nil
}

func (s *Store) beginPurgeTx(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, "PRAGMA secure_delete = ON;"); err != nil {
		return fmt.Errorf("%w: enable secure_delete: %w", ErrStoreUnavailable, err)
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); err != nil {
		return fmt.Errorf("%w: begin immediate transaction: %w", ErrStoreUnavailable, err)
	}
	if _, err := conn.ExecContext(ctx, "PRAGMA defer_foreign_keys = ON;"); err != nil {
		return fmt.Errorf("%w: defer foreign keys: %w", ErrStoreUnavailable, err)
	}
	return nil
}

func (s *Store) validatePurgeArgs(sessionID string) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if sessionID == "" || len(sessionID) > 128 {
		return errcode.New(errcode.InvalidArgument, "session ID is invalid")
	}
	return nil
}

func (s *Store) executePurgeOperations(ctx context.Context, conn *sql.Conn, sessionID string) error {
	now := s.clk.Now().UTC().Format(time.RFC3339Nano)
	if err := recordPurgeTombstone(ctx, conn, sessionID, now); err != nil {
		return err
	}
	if err := shredSessionKey(ctx, conn, sessionID, now); err != nil {
		return err
	}
	if err := deleteExecutionData(ctx, conn, sessionID); err != nil {
		return err
	}
	return s.checkFault(FaultBeforePurgeCommit)
}

// PurgeSession executes an atomic, permanent crypto-shred and deletion of all
// execution data for sessionID.
//
// Limits:
// Crypto-shred permanently zeroes and removes the wrapped key and execution
// records from the active database file, rendering past payloads unrecoverable
// even with an unlocked vault. However, crypto-shred cannot erase historical
// OS-level backups, filesystem snapshots, flash wear-leveling remapped blocks,
// or previously deleted journal files outside the active database file.
func (s *Store) PurgeSession(ctx context.Context, sessionID string) error {
	if err := s.validatePurgeArgs(sessionID); err != nil {
		return err
	}

	s.keyCache.purge(sessionID)

	conn, err := s.acquireConfiguredConn(ctx)
	if conn != nil {
		defer func() {
			if closeErr := conn.Close(); closeErr != nil {
				return
			}
		}()
	}
	if err != nil {
		return err
	}

	if err := s.beginPurgeTx(ctx, conn); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			if _, rollbackErr := conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK;"); rollbackErr != nil {
				return
			}
		}
	}()

	if err := s.executePurgeOperations(ctx, conn, sessionID); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "COMMIT;"); err != nil {
		return fmt.Errorf("%w: commit purge transaction: %w", ErrStoreUnavailable, err)
	}
	committed = true
	return nil
}
