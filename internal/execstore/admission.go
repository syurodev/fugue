package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/kernel"
)

type storedAdmission struct {
	activeRunID      sql.NullString
	ownerIncarnation uint64
	admissionToken   uint64
	revision         uint64
	queueRevision    uint64
	contextEpoch     uint64
	contextRevision  uint64
	requirement      string
	startRunID       sql.NullString
	causeRunID       sql.NullString
	decidedEventID   sql.NullString
}

func validateAdmissionFence(fence *AdmissionFence, adm *storedAdmission, runID string) error {
	if fence == nil {
		return nil
	}
	if adm.activeRunID.Valid && adm.activeRunID.String != runID {
		return fmt.Errorf("%w: active run ID mismatch: active is %s, request for %s", ErrStaleFence, adm.activeRunID.String, runID)
	}
	if adm.activeRunID.Valid && adm.activeRunID.String == runID {
		if adm.admissionToken != fence.Token {
			return fmt.Errorf("%w: admission token mismatch: active=%d, fence=%d", ErrStaleFence, adm.admissionToken, fence.Token)
		}
		if adm.ownerIncarnation != fence.OwnerIncarnation {
			return fmt.Errorf("%w: owner incarnation mismatch: active=%d, fence=%d", ErrStaleFence, adm.ownerIncarnation, fence.OwnerIncarnation)
		}
		return nil
	}
	if adm.admissionToken >= fence.Token {
		return fmt.Errorf("%w: admission token must increase: stored=%d, new=%d", ErrStaleFence, adm.admissionToken, fence.Token)
	}
	return nil
}

func validateQueueFence(fence *QueueFence, adm *storedAdmission) error {
	if fence == nil {
		return nil
	}
	if adm.queueRevision != fence.QueueRevision {
		return fmt.Errorf("%w: queue revision mismatch: stored=%d, fence=%d", ErrStaleFence, adm.queueRevision, fence.QueueRevision)
	}
	return nil
}

func (s *Store) verifyAdmissionFences(req *AppendRequest) error {
	if req.AdmissionFence != nil && req.AdmissionFence.OwnerIncarnation != s.host.LockGeneration {
		return fmt.Errorf("%w: owner incarnation %d does not match host %d", ErrStaleFence, req.AdmissionFence.OwnerIncarnation, s.host.LockGeneration)
	}

	for _, evt := range req.Events {
		admEvt, ok := evt.(*kernel.RunAdmittedEvent)
		if !ok {
			continue
		}
		if req.AdmissionFence == nil {
			return fmt.Errorf("%w: RunAdmittedEvent requires AdmissionFence", ErrStaleFence)
		}
		if req.AdmissionFence.Token != admEvt.Token {
			return fmt.Errorf("%w: fence token %d != event token %d", ErrStaleFence, req.AdmissionFence.Token, admEvt.Token)
		}
		if req.AdmissionFence.OwnerIncarnation != admEvt.OwnerIncarnation {
			return fmt.Errorf("%w: fence incarnation %d != event incarnation %d", ErrStaleFence, req.AdmissionFence.OwnerIncarnation, admEvt.OwnerIncarnation)
		}
	}
	return nil
}

func (s *Store) recheckFences(ctx context.Context, conn *sql.Conn, req *AppendRequest) (*storedAdmission, bool, error) {
	var adm storedAdmission
	err := conn.QueryRowContext(
		ctx,
		`SELECT active_run_id, owner_incarnation, admission_token, revision, queue_revision,
				context_epoch, context_start_run_id, context_revision, context_requirement,
				context_cause_run_id, context_decided_event_id
		 FROM exec_admission WHERE session_id = ?`,
		req.SessionID,
	).Scan(
		&adm.activeRunID, &adm.ownerIncarnation, &adm.admissionToken, &adm.revision, &adm.queueRevision,
		&adm.contextEpoch, &adm.startRunID, &adm.contextRevision, &adm.requirement,
		&adm.causeRunID, &adm.decidedEventID,
	)

	if errors.Is(err, sql.ErrNoRows) {
		if req.ExpectedAdmissionRevision != 1 {
			return nil, false, fmt.Errorf("%w: admission row does not exist, expected revision %d", ErrStaleRevision, req.ExpectedAdmissionRevision)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: read exec_admission: %w", ErrStoreUnavailable, err)
	}

	if req.ExpectedAdmissionRevision != adm.revision {
		return nil, false, fmt.Errorf("%w: admission revision got %d, expected %d", ErrStaleRevision, adm.revision, req.ExpectedAdmissionRevision)
	}

	if fenceErr := s.verifyAdmissionFences(req); fenceErr != nil {
		return nil, false, fenceErr
	}
	if fenceErr := validateAdmissionFence(req.AdmissionFence, &adm, req.RunID); fenceErr != nil {
		return nil, false, fenceErr
	}
	if fenceErr := validateQueueFence(req.QueueFence, &adm); fenceErr != nil {
		return nil, false, fenceErr
	}

	return &adm, true, nil
}

func insertInitialAdmission(ctx context.Context, conn *sql.Conn, req *AppendRequest, state *kernel.State) error {
	var activeRun *string
	var token, incarnation uint64
	if req.AdmissionFence != nil {
		if !state.Finished {
			activeRun = &req.RunID
		}
		token = req.AdmissionFence.Token
		incarnation = req.AdmissionFence.OwnerIncarnation
	}
	var queueRev uint64
	if req.QueueFence != nil {
		queueRev = req.QueueFence.QueueRevision
	}

	reqStr := string(state.ContextRequirement)
	if reqStr == "" {
		reqStr = "none"
	}

	_, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_admission (
			session_id, active_run_id, owner_incarnation, admission_token,
			revision, queue_revision, context_epoch, context_start_run_id,
			context_revision, context_requirement, context_cause_run_id, context_decided_event_id
		) VALUES (?, ?, ?, ?, 1, ?, ?, NULL, ?, ?, NULL, NULL)`,
		req.SessionID, activeRun, incarnation, token, queueRev,
		state.ContextEpoch, state.ContextRevision, reqStr,
	)
	if err != nil {
		return fmt.Errorf("%w: insert initial exec_admission: %w", ErrInvalidBatch, err)
	}
	return nil
}

func computeUpdatedAdmission(req *AppendRequest, state *kernel.State, adm *storedAdmission) storedAdmission {
	updated := *adm
	updated.revision = adm.revision + 1
	if req.AdmissionFence != nil {
		updated.admissionToken = req.AdmissionFence.Token
		updated.ownerIncarnation = req.AdmissionFence.OwnerIncarnation
		updated.activeRunID = sql.NullString{String: req.RunID, Valid: !state.Finished}
	} else if req.QueueFence != nil {
		updated.queueRevision = adm.queueRevision + 1
	}
	if state.ContextEpoch > updated.contextEpoch {
		updated.contextEpoch = state.ContextEpoch
	}
	if state.ContextRevision > updated.contextRevision {
		updated.contextRevision = state.ContextRevision
	}
	if state.ContextRequirement != "" && string(state.ContextRequirement) != updated.requirement {
		updated.requirement = string(state.ContextRequirement)
	}
	return updated
}

func (s *Store) updateAdmissionRow(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
	adm *storedAdmission,
) error {
	if adm == nil {
		return nil
	}

	up := computeUpdatedAdmission(req, state, adm)

	res, err := conn.ExecContext(
		ctx,
		`UPDATE exec_admission
		 SET revision = ?,
		     active_run_id = ?,
		     owner_incarnation = ?,
		     admission_token = ?,
		     queue_revision = ?,
		     context_epoch = ?,
		     context_revision = ?,
		     context_requirement = ?
		 WHERE session_id = ? AND revision = ?`,
		up.revision,
		up.activeRunID,
		up.ownerIncarnation,
		up.admissionToken,
		up.queueRevision,
		up.contextEpoch,
		up.contextRevision,
		up.requirement,
		req.SessionID,
		adm.revision,
	)
	if err != nil {
		return fmt.Errorf("%w: update exec_admission: %w", ErrInvalidBatch, err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: check rows affected: %w", ErrStoreUnavailable, err)
	}
	if rows != 1 {
		return fmt.Errorf("%w: admission update CAS failed", ErrStaleRevision)
	}
	return nil
}
