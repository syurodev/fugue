package execstore

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// Append applies a proposed state transition batch inside an immediate transaction.
func (s *Store) Append(ctx context.Context, req *AppendRequest) (AppendOutcome, error) {
	if req == nil {
		return AppendOutcome{}, fmt.Errorf("%w: append request is required", ErrInvalidBatch)
	}
	if err := s.validateRequest(req); err != nil {
		return AppendOutcome{}, err
	}

	// 1. Dedup preflight outside transaction
	existing, found, err := s.lookupReceiptForDedup(ctx, req.CommandID)
	if err != nil {
		return AppendOutcome{}, err
	}
	if found {
		return s.checkPreflightDedup(ctx, req, &existing)
	}

	// 2. Begin transaction on a dedicated connection
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: acquire connection: %w", ErrStoreUnavailable, err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			return
		}
	}()

	if _, beginErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE; PRAGMA defer_foreign_keys = ON;"); beginErr != nil {
		return AppendOutcome{}, fmt.Errorf("%w: begin immediate transaction: %w", ErrStoreUnavailable, beginErr)
	}
	committed := false
	defer func() {
		if !committed {
			if _, rollbackErr := conn.ExecContext(ctx, "ROLLBACK"); rollbackErr != nil {
				return
			}
		}
	}()

	outcome, err := s.executeAppendTx(ctx, conn, req)
	if err != nil {
		return AppendOutcome{}, err
	}
	committed = true
	return outcome, nil
}

func validateRequestIdentity(req *AppendRequest) error {
	if req.SessionID == "" || req.RunID == "" || req.CommandID == "" {
		return fmt.Errorf("%w: session ID, run ID and command ID are required", ErrInvalidBatch)
	}
	if req.Kind == "" || req.Principal == "" || req.Target == "" || req.CodecVersion == "" {
		return fmt.Errorf("%w: command metadata (kind, principal, target, codec version) is required", ErrInvalidBatch)
	}
	return nil
}

func validateRequestFences(req *AppendRequest) error {
	hasProtected := req.ProtectedInputID != ""
	hasMetadata := len(req.MetadataInput) > 0
	if hasProtected == hasMetadata {
		return fmt.Errorf("%w: exactly one of ProtectedInputID or MetadataInput must be provided", ErrInvalidBatch)
	}
	if (req.AdmissionFence == nil && req.QueueFence == nil) || (req.AdmissionFence != nil && req.QueueFence != nil) {
		return fmt.Errorf("%w: exactly one of AdmissionFence or QueueFence must be provided", ErrInvalidBatch)
	}
	if req.CorrelationID == "" {
		req.CorrelationID = req.CommandID
	}
	return nil
}

func (s *Store) validateRequest(req *AppendRequest) error {
	if err := validateRequestIdentity(req); err != nil {
		return err
	}
	return validateRequestFences(req)
}

func (s *Store) checkPreflightDedup(ctx context.Context, req *AppendRequest, existing *codec.Receipt) (AppendOutcome, error) {
	if existing.CodecVersion != req.CodecVersion {
		return AppendOutcome{}, fmt.Errorf("%w: existing=%s, new=%s", ErrCodecVersionMismatch, existing.CodecVersion, req.CodecVersion)
	}
	if existing.Kind != req.Kind || existing.Principal != req.Principal || existing.Target != req.Target {
		return AppendOutcome{}, fmt.Errorf("%w: command metadata mismatch", ErrReceiptConflict)
	}

	if existing.MetadataOnly {
		if bytes.Equal(existing.CanonicalInput, req.CanonicalInput) {
			return AppendOutcome{Duplicate: true, Receipt: *existing}, nil
		}
		return AppendOutcome{}, fmt.Errorf("%w: metadata-only content mismatch", ErrReceiptConflict)
	}

	if s.equality == nil {
		return AppendOutcome{}, fmt.Errorf("%w: protected equality verifier is not configured", ErrProtectedInputUnavailable)
	}
	existingInput, err := s.equality.OpenPayload(ctx, existing.CommandID, existing.InputPayloadID)
	if err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: open protected payload: %w", ErrProtectedInputUnavailable, err)
	}
	if bytes.Equal(existingInput, req.CanonicalInput) {
		return AppendOutcome{Duplicate: true, Receipt: *existing}, nil
	}
	return AppendOutcome{}, fmt.Errorf("%w: protected payload content mismatch", ErrReceiptConflict)
}

func (s *Store) executeAppendTx(ctx context.Context, conn *sql.Conn, req *AppendRequest) (AppendOutcome, error) {
	// Recheck purges
	var purging int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", req.SessionID).Scan(&purging); err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: check purges: %w", ErrStoreUnavailable, err)
	}
	if purging > 0 {
		return AppendOutcome{}, ErrSessionPurging
	}

	// Recheck receipt inside transaction
	existing, found, err := queryReceipt(ctx, conn, req.CommandID)
	if err != nil {
		return AppendOutcome{}, err
	}
	if found {
		return s.checkPreflightDedup(ctx, req, &existing)
	}

	// Recheck admission row and fences
	admission, _, err := s.recheckFences(ctx, conn, req)
	if err != nil {
		return AppendOutcome{}, err
	}

	// Recheck run row and revision
	newRunRevision, currentSeq, err := s.recheckRunRevision(ctx, conn, req)
	if err != nil {
		return AppendOutcome{}, err
	}

	// Validate event sequence and evolve state
	state, err := s.foldBatchEvents(currentSeq, req.Events)
	if err != nil {
		return AppendOutcome{}, err
	}

	// Write boundaries with fault injection checks
	if err := s.writeBatchRows(ctx, conn, req, state, admission, newRunRevision); err != nil {
		return AppendOutcome{}, err
	}

	if err := s.checkFault(FaultBeforeCommit); err != nil {
		return AppendOutcome{}, err
	}

	if _, err := conn.ExecContext(ctx, "COMMIT"); err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: commit transaction: %w", ErrStoreUnavailable, err)
	}

	if err := s.checkFault(FaultAfterCommit); err != nil {
		return AppendOutcome{}, err
	}

	receipt := codec.Receipt{
		CommandID:      req.CommandID,
		Kind:           req.Kind,
		Principal:      req.Principal,
		Target:         req.Target,
		CodecVersion:   req.CodecVersion,
		InputPayloadID: req.ProtectedInputID,
		MetadataOnly:   len(req.MetadataInput) > 0,
		CanonicalInput: req.CanonicalInput,
		Result:         req.Result,
	}
	return AppendOutcome{Committed: true, Receipt: receipt}, nil
}

func (s *Store) foldBatchEvents(currentSeq uint64, events []kernel.Event) (*kernel.State, error) {
	state := kernel.NewState()
	expectedSeq := currentSeq + 1
	for _, evt := range events {
		if evt == nil {
			return nil, fmt.Errorf("%w: event is nil", ErrInvalidBatch)
		}
		env := evt.EventEnvelope()
		if env.Sequence != expectedSeq {
			return nil, fmt.Errorf("%w: sequence gap: got %d, want %d", ErrInvalidBatch, env.Sequence, expectedSeq)
		}
		expectedSeq++
		state = kernel.Evolve(state, evt)
	}
	return state, nil
}
