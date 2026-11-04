package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/codec"
)

// verifyRecoveredConnection verifies that conn satisfies durability requirements:
// journal_mode=DELETE, synchronous=FULL (2), foreign_keys=ON (1), and is in autocommit mode.
func verifyRecoveredConnection(ctx context.Context, conn *sql.Conn) error {
	var journalMode string
	if scanErr := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); scanErr != nil {
		return fmt.Errorf("%w: read journal_mode on recovered connection: %w", ErrStoreUnavailable, scanErr)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "delete") {
		return fmt.Errorf("%w: unexpected journal_mode %q", ErrUnsafePragmas, journalMode)
	}

	var syncMode int
	if scanErr := conn.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&syncMode); scanErr != nil {
		return fmt.Errorf("%w: read synchronous on recovered connection: %w", ErrStoreUnavailable, scanErr)
	}
	if syncMode != 2 {
		return fmt.Errorf("%w: unexpected synchronous %d (want 2/FULL)", ErrUnsafePragmas, syncMode)
	}

	var foreignKeys int
	if scanErr := conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); scanErr != nil {
		return fmt.Errorf("%w: read foreign_keys on recovered connection: %w", ErrStoreUnavailable, scanErr)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("%w: unexpected foreign_keys %d (want 1/ON)", ErrUnsafePragmas, foreignKeys)
	}

	// Verify the connection is clean and in autocommit mode.
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE; ROLLBACK;"); err != nil {
		return fmt.Errorf("%w: verify clean autocommit on recovered connection: %w", ErrStoreUnavailable, err)
	}

	return nil
}

// ResolveCommit determines the outcome of an uncertain COMMIT by reacquiring
// a verified physical connection on the shared database handle and reading
// the command receipt.
func (s *Store) ResolveCommit(ctx context.Context, commandID string) (CommitStatus, codec.Receipt, error) {
	if s == nil || s.db == nil {
		return CommitStatusUnavailable, codec.Receipt{}, ErrStoreUnavailable
	}

	conn, err := s.db.Conn(ctx)
	if err != nil {
		return CommitStatusUnavailable, codec.Receipt{}, fmt.Errorf("%w: reacquire connection: %w", ErrStoreUnavailable, err)
	}
	connClean := false
	defer func() {
		if !connClean {
			discardConn(conn)
		} else {
			if closeErr := conn.Close(); closeErr != nil {
				return
			}
		}
	}()

	if verifyErr := verifyRecoveredConnection(ctx, conn); verifyErr != nil {
		if errors.Is(verifyErr, ErrUnsafePragmas) {
			return CommitStatusUnavailable, codec.Receipt{}, verifyErr
		}
		return CommitStatusUnavailable, codec.Receipt{}, verifyErr
	}
	connClean = true

	receipt, found, err := queryReceipt(ctx, conn, commandID)
	if err != nil {
		return CommitStatusUnavailable, codec.Receipt{}, fmt.Errorf("%w: read receipt on recovered connection: %w", ErrStoreUnavailable, err)
	}
	if !found {
		return CommitStatusNotCommitted, codec.Receipt{}, ErrNotCommitted
	}

	return CommitStatusCommitted, receipt.Receipt, nil
}
