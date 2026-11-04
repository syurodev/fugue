package execstore

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

const (
	fieldRunRevision = "RunRevision"
	cmdAdmitRun      = "AdmitRun"
	cmdRequestStop   = "RequestStop"
)

func isSafeAdmitField(field string) bool {
	return field == "token" || field == "AdmissionToken" || field == "ContextEpoch"
}

func isSafeStopField(field string) bool {
	return field == "stop" || field == "Reason" || field == "Mode"
}

func isSafeCloseField(field string) bool {
	switch field {
	case "RunID", fieldRunRevision, "ExactAttemptIDs", "CauseIDs", "Actor", "UnavailabilityReason":
		return true
	default:
		return false
	}
}

func isSafeContextField(field string) bool {
	switch field {
	case "OldContextRevision", "NewContextRevision", "Epoch", "StartRunID", "Mode", "Actor", "ValidationEvidence":
		return true
	default:
		return false
	}
}

func isSafeMetadataField(kind, field string) bool {
	switch kind {
	case "AcceptRun":
		return field == "InputRef"
	case cmdAdmitRun:
		return isSafeAdmitField(field)
	case "AcquireRunOwnership":
		return field == "OwnerIncarnation"
	case "AdvanceRun":
		return field == "StepIndex"
	case cmdRequestStop:
		return isSafeStopField(field)
	case "CloseRunWithoutPayload":
		return isSafeCloseField(field)
	case "SelectRunContext":
		return isSafeContextField(field)
	case "ResumeRun", "RecoverRun":
		return field == fieldRunRevision
	default:
		return false
	}
}

func isMetadataOnlyPermitted(kind string) bool {
	switch kind {
	case "AcceptRun", cmdAdmitRun, "AcquireRunOwnership", "AdvanceRun",
		cmdRequestStop, "CloseRunWithoutPayload", "SelectRunContext",
		"ResumeRun", "RecoverRun":
		return true
	default:
		return false
	}
}

func discardConn(conn *sql.Conn) {
	if rawErr := conn.Raw(func(driverConn any) error {
		if closer, ok := driverConn.(io.Closer); ok {
			if closeErr := closer.Close(); closeErr != nil {
				return driver.ErrBadConn
			}
		}
		return driver.ErrBadConn
	}); rawErr != nil {
		if closeErr := conn.Close(); closeErr != nil {
			return
		}
		return
	}
	if closeErr := conn.Close(); closeErr != nil {
		return
	}
}

func rollbackAndVerifyClean(ctx context.Context, conn *sql.Conn, connClean *bool) {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	if _, err := conn.ExecContext(cleanupCtx, "ROLLBACK"); err != nil {
		*connClean = false
		return
	}
	if _, err := conn.ExecContext(cleanupCtx, "BEGIN IMMEDIATE; ROLLBACK;"); err != nil {
		*connClean = false
		return
	}
	*connClean = true
}

func loadCommittedRunState(ctx context.Context, conn *sql.Conn, runID string, currentSeq uint64) (*kernel.State, error) {
	state := kernel.NewState()
	if currentSeq == 0 {
		return state, nil
	}

	rows, err := conn.QueryContext(
		ctx,
		`SELECT run_id, sequence, event_id, session_id, event_type,
				schema_version, protocol_version, command_id, causation_id,
				correlation_id, origin, writer_incarnation, admission_token,
				queue_revision, recorded_at, data, payload_id
		 FROM exec_events
		 WHERE run_id = ? AND sequence >= 1 AND sequence <= ?
		 ORDER BY sequence ASC`,
		runID, currentSeq,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: query committed events: %w", ErrStoreUnavailable, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			return
		}
	}()

	var expectedSeq uint64 = 1
	for rows.Next() {
		evt, scanErr := scanDurableEvent(rows, expectedSeq)
		if scanErr != nil {
			return nil, scanErr
		}
		expectedSeq++
		state = kernel.Evolve(state, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate committed events: %w", ErrStoreUnavailable, err)
	}
	if expectedSeq-1 != currentSeq {
		return nil, fmt.Errorf("%w: sequence count mismatch: loaded %d, want %d", ErrCorruptHistory, expectedSeq-1, currentSeq)
	}
	return state, nil
}

func foldNewBatchEvents(currentSeq uint64, events []kernel.Event, state *kernel.State) (*kernel.State, error) {
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

func (s *Store) foldBatchEvents(currentSeq uint64, events []kernel.Event) (*kernel.State, error) {
	return foldNewBatchEvents(currentSeq, events, kernel.NewState())
}

// Append applies a proposed state transition batch inside an immediate transaction.
func (s *Store) Append(ctx context.Context, req *AppendRequest) (AppendOutcome, error) {
	if s == nil || s.db == nil {
		return AppendOutcome{}, ErrStoreUnavailable
	}
	if req == nil {
		return AppendOutcome{}, fmt.Errorf("%w: append request is required", ErrInvalidBatch)
	}
	if req.SessionID == "" || req.RunID == "" || req.CommandID == "" {
		return AppendOutcome{}, fmt.Errorf("%w: session ID, run ID and command ID are required", ErrInvalidBatch)
	}

	// 1. Dedup preflight outside transaction
	existing, found, err := s.lookupReceiptForDedup(ctx, req.CommandID)
	if err != nil {
		return AppendOutcome{}, err
	}
	if found {
		return s.checkPreflightDedup(ctx, req, &existing)
	}

	// 2. Validate request
	if reqErr := s.validateRequest(req); reqErr != nil {
		return AppendOutcome{}, reqErr
	}

	// 3. Begin transaction on dedicated connection
	return s.appendTx(ctx, req)
}

func (s *Store) appendTx(ctx context.Context, req *AppendRequest) (AppendOutcome, error) {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: acquire connection: %w", ErrStoreUnavailable, err)
	}
	committed := false
	connClean := false
	defer func() {
		if !committed && !connClean {
			rollbackAndVerifyClean(ctx, conn, &connClean)
		}
		if !connClean {
			discardConn(conn)
		} else {
			if closeErr := conn.Close(); closeErr != nil {
				return
			}
		}
	}()

	if _, beginErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE; PRAGMA defer_foreign_keys = ON;"); beginErr != nil {
		return AppendOutcome{}, fmt.Errorf("%w: begin immediate transaction: %w", ErrStoreUnavailable, beginErr)
	}

	outcome, err := s.executeAppendTx(ctx, conn, req)
	if outcome.Committed {
		committed = true
		connClean = true
	}
	if err != nil {
		return outcome, err
	}
	connClean = true
	if !outcome.Duplicate {
		committed = true
	}
	return outcome, nil
}

func validateRequestIdentity(req *AppendRequest) error {
	if req.Kind == "" || req.Principal == "" || req.Target == "" || req.CodecVersion == "" {
		return fmt.Errorf("%w: command metadata (kind, principal, target, codec version) is required", ErrInvalidBatch)
	}
	return nil
}

func validateRawMetadataFields(kind string, raw map[string]any) error {
	for key, val := range raw {
		if !isSafeMetadataField(kind, key) {
			return fmt.Errorf("%w: field %q is not permitted in metadata-only command %q", ErrInvalidBatch, key, kind)
		}
		if key == "Reason" {
			if str, isStr := val.(string); isStr && (len(str) > 64 || strings.Contains(str, " ")) {
				return fmt.Errorf("%w: invalid reason enum %q", ErrInvalidBatch, str)
			}
		}
	}
	return nil
}

func validateMetadataInput(req *AppendRequest) error {
	if len(req.MetadataInput) == 0 {
		return nil
	}
	if len(req.CanonicalInput) == 0 {
		req.CanonicalInput = req.MetadataInput
	} else if !bytes.Equal(req.MetadataInput, req.CanonicalInput) {
		return fmt.Errorf("%w: CanonicalInput must equal MetadataInput for metadata-only commands", ErrInvalidBatch)
	}
	if !isMetadataOnlyPermitted(req.Kind) {
		return fmt.Errorf("%w: command kind %q is not permitted as metadata-only", ErrInvalidBatch, req.Kind)
	}

	dec := json.NewDecoder(bytes.NewReader(req.MetadataInput))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("%w: metadata input must be valid JSON object: %w", ErrInvalidBatch, err)
	}
	if err := validateRawMetadataFields(req.Kind, raw); err != nil {
		return err
	}

	canonicalBytes, err := codec.MarshalCanonical(raw)
	if err != nil {
		return fmt.Errorf("%w: marshal canonical metadata: %w", ErrInvalidBatch, err)
	}
	req.MetadataInput = canonicalBytes
	req.CanonicalInput = canonicalBytes
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
	if hasMetadata {
		if err := validateMetadataInput(req); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) validateRequest(req *AppendRequest) error {
	if err := validateRequestIdentity(req); err != nil {
		return err
	}
	if err := validateRequestFences(req); err != nil {
		return err
	}
	if req.QueueFence != nil {
		if req.Kind == cmdAdmitRun {
			return fmt.Errorf("%w: AdmitRun not permitted under QueueFence", ErrStaleFence)
		}
		for _, evt := range req.Events {
			if _, isAdmit := evt.(*kernel.RunAdmittedEvent); isAdmit {
				return fmt.Errorf("%w: RunAdmittedEvent not permitted under QueueFence", ErrStaleFence)
			}
		}
	}
	return nil
}

func (s *Store) checkPreflightDedup(ctx context.Context, req *AppendRequest, existing *storedReceipt) (AppendOutcome, error) {
	if existing.SessionID != req.SessionID {
		return AppendOutcome{}, fmt.Errorf("%w: receipt session ID mismatch: existing=%s, new=%s", ErrReceiptConflict, existing.SessionID, req.SessionID)
	}
	if existing.Receipt.CodecVersion != req.CodecVersion {
		return AppendOutcome{}, fmt.Errorf("%w: existing=%s, new=%s", ErrCodecVersionMismatch, existing.Receipt.CodecVersion, req.CodecVersion)
	}
	if existing.Receipt.Kind != req.Kind || existing.Receipt.Principal != req.Principal || existing.Receipt.Target != req.Target {
		return AppendOutcome{}, fmt.Errorf("%w: command metadata mismatch", ErrReceiptConflict)
	}

	if existing.Receipt.MetadataOnly {
		if bytes.Equal(existing.Receipt.CanonicalInput, req.CanonicalInput) {
			return AppendOutcome{Duplicate: true, Receipt: existing.Receipt}, nil
		}
		return AppendOutcome{}, fmt.Errorf("%w: metadata-only content mismatch", ErrReceiptConflict)
	}

	if s.equality == nil {
		return AppendOutcome{}, fmt.Errorf("%w: protected equality verifier is not configured", ErrProtectedInputUnavailable)
	}
	existingInput, err := s.equality.OpenPayload(ctx, existing.SessionID, existing.Receipt.InputPayloadID)
	if err != nil {
		return AppendOutcome{}, fmt.Errorf("%w: open protected payload: %w", ErrProtectedInputUnavailable, err)
	}
	if bytes.Equal(existingInput, req.CanonicalInput) {
		return AppendOutcome{Duplicate: true, Receipt: existing.Receipt}, nil
	}
	return AppendOutcome{}, fmt.Errorf("%w: protected payload content mismatch", ErrReceiptConflict)
}

func checkSessionPurging(ctx context.Context, conn *sql.Conn, sessionID string) error {
	var purging int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", sessionID).Scan(&purging); err != nil {
		return fmt.Errorf("%w: check purges: %w", ErrStoreUnavailable, err)
	}
	if purging > 0 {
		return ErrSessionPurging
	}
	return nil
}

func buildReceipt(req *AppendRequest) codec.Receipt {
	return codec.Receipt{
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
}

func (s *Store) checkTxReceipt(ctx context.Context, conn *sql.Conn, req *AppendRequest) (AppendOutcome, bool, error) {
	existing, found, err := queryReceipt(ctx, conn, req.CommandID)
	if err != nil {
		return AppendOutcome{}, false, err
	}
	if !found {
		return AppendOutcome{}, false, nil
	}
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, rbErr := conn.ExecContext(cleanupCtx, "ROLLBACK"); rbErr != nil {
		discardConn(conn)
	}
	outcome, dedupErr := s.checkPreflightDedup(ctx, req, &existing)
	return outcome, true, dedupErr
}

func (s *Store) prepareBatchState(ctx context.Context, conn *sql.Conn, req *AppendRequest, currentSeq uint64) (*kernel.State, error) {
	state, err := loadCommittedRunState(ctx, conn, req.RunID, currentSeq)
	if err != nil {
		return nil, err
	}
	if len(req.Events) > 0 {
		return foldNewBatchEvents(currentSeq, req.Events, state)
	}
	return state, nil
}

func (s *Store) executeAppendTx(ctx context.Context, conn *sql.Conn, req *AppendRequest) (AppendOutcome, error) {
	if err := checkSessionPurging(ctx, conn, req.SessionID); err != nil {
		return AppendOutcome{}, err
	}
	if outcome, found, err := s.checkTxReceipt(ctx, conn, req); found || err != nil {
		return outcome, err
	}
	admission, _, err := s.recheckFences(ctx, conn, req)
	if err != nil {
		return AppendOutcome{}, err
	}
	newRunRevision, currentSeq, err := s.recheckRunRevision(ctx, conn, req)
	if err != nil {
		return AppendOutcome{}, err
	}
	state, err := s.prepareBatchState(ctx, conn, req, currentSeq)
	if err != nil {
		return AppendOutcome{}, err
	}
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
		return AppendOutcome{Committed: true}, err
	}
	return AppendOutcome{Committed: true, Receipt: buildReceipt(req)}, nil
}
