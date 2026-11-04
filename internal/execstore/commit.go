package execstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/codec"
)

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
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			return
		}
	}()

	var journalMode string
	if scanErr := conn.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); scanErr != nil {
		return CommitStatusUnavailable, codec.Receipt{}, fmt.Errorf("%w: read journal_mode on recovered connection: %w", ErrStoreUnavailable, scanErr)
	}
	if !strings.EqualFold(strings.TrimSpace(journalMode), "delete") {
		return CommitStatusUnavailable, codec.Receipt{}, fmt.Errorf("%w: unexpected journal_mode %q", ErrUnsafePragmas, journalMode)
	}

	receipt, found, err := queryReceipt(ctx, conn, commandID)
	if err != nil {
		return CommitStatusUnavailable, codec.Receipt{}, fmt.Errorf("%w: read receipt on recovered connection: %w", ErrStoreUnavailable, err)
	}
	if !found {
		return CommitStatusNotCommitted, codec.Receipt{}, ErrNotCommitted
	}

	return CommitStatusCommitted, receipt, nil
}
