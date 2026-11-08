package vendorstore

import (
	"context"
	"fmt"
	"time"
)

// ProcessIdentity identifies a process whose lifetime may outlive the host.
type ProcessIdentity struct {
	SessionID string
	PID       int64
	StartTime string
}

// Recover detaches unfinished sessions and marks open turns unknown without replay.
func (s *Store) Recover(ctx context.Context) ([]ProcessIdentity, error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin recovery: %w", ErrUnavailable, err)
	}
	defer rollback(tx)
	rows, err := tx.QueryContext(ctx, `SELECT session_id, process_pid, process_start_time,state FROM vendor_sessions ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("%w: read recovery identities: %w", ErrUnavailable, err)
	}
	defer closeRows(rows)
	var result []ProcessIdentity
	for rows.Next() {
		var identity ProcessIdentity
		var state string
		if operationErr1 := rows.Scan(&identity.SessionID, &identity.PID, &identity.StartTime, &state); operationErr1 != nil {
			closeRows(rows)
			return nil, fmt.Errorf("%w: scan recovery identity: %w", ErrUnavailable, operationErr1)
		}
		if s.cache.IsPurged(identity.SessionID) {
			return nil, ErrPurged
		}
		if identity.PID > 0 && state != StateClosed {
			result = append(result, identity)
		}
	}
	if operationErr2 := rows.Err(); operationErr2 != nil {
		closeRows(rows)
		return nil, fmt.Errorf("%w: read recovery identities: %w", ErrUnavailable, operationErr2)
	}
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	if _, operationErr4 := tx.ExecContext(ctx, `UPDATE vendor_turns SET outcome='unknown', ended_at=? WHERE outcome=''`, now); operationErr4 != nil {
		return nil, fmt.Errorf("%w: recover turns: %w", ErrUnavailable, operationErr4)
	}
	if _, operationErr5 := tx.ExecContext(ctx, `UPDATE vendor_sessions SET state='detached', waiting_reason='', updated_at=? WHERE state <> 'closed'`, now); operationErr5 != nil {
		return nil, fmt.Errorf("%w: recover sessions: %w", ErrUnavailable, operationErr5)
	}
	if operationErr6 := tx.Commit(); operationErr6 != nil {
		return nil, fmt.Errorf("%w: commit recovery: %w", ErrUnavailable, operationErr6)
	}
	return result, nil
}
