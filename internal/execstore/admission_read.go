package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// AdmissionRecord is the durable session ownership and context projection.
type AdmissionRecord struct {
	SessionID         string
	ActiveRunID       string
	OwnerIncarnation  uint64
	AdmissionToken    uint64
	Revision          uint64
	QueueRevision     uint64
	ContextEpoch      uint64
	ContextRevision   uint64
	ContextStartRunID string
}

// ReadAdmission returns the session's control row, or a zero record before its first append.
func (s *Store) ReadAdmission(ctx context.Context, sessionID string) (AdmissionRecord, error) {
	if s == nil || s.db == nil {
		return AdmissionRecord{}, ErrStoreUnavailable
	}
	var row AdmissionRecord
	var active, start sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT session_id, active_run_id, owner_incarnation, admission_token,
 revision, queue_revision, context_epoch, context_revision, context_start_run_id
 FROM exec_admission WHERE session_id = ?`, sessionID).Scan(&row.SessionID, &active, &row.OwnerIncarnation, &row.AdmissionToken, &row.Revision, &row.QueueRevision, &row.ContextEpoch, &row.ContextRevision, &start)
	if errors.Is(err, sql.ErrNoRows) {
		return AdmissionRecord{}, nil
	}
	if err != nil {
		return AdmissionRecord{}, fmt.Errorf("%w: read admission: %w", ErrStoreUnavailable, err)
	}
	row.ActiveRunID, row.ContextStartRunID = active.String, start.String
	return row, nil
}
