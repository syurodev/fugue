package vendorstore

import (
	"context"
	"fmt"
	"time"
)

// ProcessIdentity identifies a process whose lifetime may outlive the host. The
// fields are the format internal/process Reap verifies (F9.2b): StartToken is an
// opaque token and ScopeUnit is empty when the process was uncontained.
type ProcessIdentity struct {
	SessionID  string
	PID        int64
	StartToken string
	ScopeUnit  string
}

// Recover detaches unfinished sessions and marks open turns unknown without replay.
//
// It returns the identity of every non-closed session that recorded a process,
// including sessions an earlier restart already detached: a detached session
// keeps its identity until a close event proves the process stopped. Reporting
// an identity again is safe because Reap verifies the start token before it
// signals anything, so a recycled PID is never killed.
func (s *Store) Recover(ctx context.Context) ([]ProcessIdentity, error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: begin recovery: %w", ErrUnavailable, err)
	}
	defer rollback(ctx, tx)
	rows, err := tx.QueryContext(ctx,
		`SELECT session_id,process_pid,process_start_token,process_scope_unit,state
		FROM vendor_sessions ORDER BY session_id`)
	if err != nil {
		return nil, fmt.Errorf("%w: read recovery identities: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	var result []ProcessIdentity
	for rows.Next() {
		var identity ProcessIdentity
		var state string
		err = rows.Scan(&identity.SessionID, &identity.PID, &identity.StartToken, &identity.ScopeUnit, &state)
		if err != nil {
			return nil, fmt.Errorf("%w: scan recovery identity: %w", ErrUnavailable, err)
		}
		if identity.PID > 0 && state != StateClosed {
			result = append(result, identity)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: read recovery identities: %w", ErrUnavailable, err)
	}
	now := s.clock.Now().UTC().Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx,
		`UPDATE vendor_turns SET outcome=?,ended_at=? WHERE outcome=''`, OutcomeUnknown, now)
	if err != nil {
		return nil, fmt.Errorf("%w: recover turns: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx,
		`INSERT INTO vendor_session_changes(session_id,kind,state,waiting_reason,recorded_at)
		SELECT session_id,?,?,'',? FROM vendor_sessions WHERE state NOT IN (?,?) ORDER BY session_id`,
		ChangeStateChanged, StateDetached, now, StateClosed, StateDetached)
	if err != nil {
		return nil, fmt.Errorf("%w: record recovered sessions: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE vendor_sessions SET state=?,waiting_reason='',updated_at=? WHERE state <> ?`,
		StateDetached, now, StateClosed)
	if err != nil {
		return nil, fmt.Errorf("%w: recover sessions: %w", ErrUnavailable, err)
	}
	if err = tx.Commit(); err != nil {
		return nil, fmt.Errorf("%w: commit recovery: %w", ErrUnavailable, err)
	}
	return result, nil
}
