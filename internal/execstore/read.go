package execstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

// ReadRun retrieves the current projected control state of runID.
func (s *Store) ReadRun(ctx context.Context, runID string) (RunRecord, error) {
	if s == nil || s.db == nil {
		return RunRecord{}, ErrStoreUnavailable
	}
	var (
		run, session, lifecycle, phase, reasonsJSON, outcome string
		lastSeq, rev, protoVer, token, contextEpoch          uint64
		stepIndex                                            int
	)
	err := s.db.QueryRowContext(
		ctx,
		`SELECT run_id, session_id, last_sequence, revision, protocol_version,
				lifecycle, phase, wait_reasons, outcome, admission_token, step_index, context_epoch
		 FROM exec_runs WHERE run_id = ?`,
		runID,
	).Scan(
		&run, &session, &lastSeq, &rev, &protoVer,
		&lifecycle, &phase, &reasonsJSON, &outcome,
		&token, &stepIndex, &contextEpoch,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RunRecord{}, ErrRunNotFound
	}
	if err != nil {
		return RunRecord{}, fmt.Errorf("%w: read exec_runs: %w", ErrStoreUnavailable, err)
	}

	var waitReasons []kernel.WaitReason
	if err := json.Unmarshal([]byte(reasonsJSON), &waitReasons); err != nil {
		return RunRecord{}, fmt.Errorf("%w: unmarshal wait reasons: %w", ErrCorruptHistory, err)
	}

	return RunRecord{
		RunID:           run,
		SessionID:       session,
		LastSequence:    lastSeq,
		Revision:        rev,
		ProtocolVersion: protoVer,
		Lifecycle:       kernel.Lifecycle(lifecycle),
		Phase:           kernel.Phase(phase),
		WaitReasons:     waitReasons,
		Outcome:         kernel.RunOutcome(outcome),
		AdmissionToken:  token,
		StepIndex:       stepIndex,
		ContextEpoch:    contextEpoch,
	}, nil
}

// ReadEvents retrieves a contiguous, verified sequence range of events for runID.
func (s *Store) ReadEvents(ctx context.Context, runID string, fromSequence, toSequence uint64) ([]kernel.Event, error) {
	if s == nil || s.db == nil {
		return nil, ErrStoreUnavailable
	}
	if toSequence < fromSequence {
		return nil, fmt.Errorf("%w: toSequence %d < fromSequence %d", ErrInvalidBatch, toSequence, fromSequence)
	}

	rows, err := s.db.QueryContext(
		ctx,
		`SELECT run_id, sequence, event_id, session_id, event_type,
				schema_version, protocol_version, command_id, causation_id,
				correlation_id, origin, writer_incarnation, admission_token,
				queue_revision, recorded_at, data, payload_id
		 FROM exec_events
		 WHERE run_id = ? AND sequence >= ? AND sequence <= ?
		 ORDER BY sequence ASC`,
		runID, fromSequence, toSequence,
	)
	if rows != nil {
		defer func() {
			if closeErr := rows.Close(); closeErr != nil {
				return
			}
		}()
	}
	if err != nil {
		return nil, fmt.Errorf("%w: query exec_events: %w", ErrStoreUnavailable, err)
	}

	var events []kernel.Event
	expectedSeq := fromSequence
	for rows.Next() {
		evt, err := scanDurableEvent(rows, expectedSeq)
		if err != nil {
			return nil, err
		}
		expectedSeq++
		events = append(events, evt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: iterate exec_events: %w", ErrStoreUnavailable, err)
	}

	return events, nil
}

func scanDurableEvent(rows *sql.Rows, expectedSeq uint64) (kernel.Event, error) {
	var d DurableEvent
	var recordedStr string
	var payloadID sql.NullString
	if err := rows.Scan(
		&d.RunID, &d.Sequence, &d.EventID, &d.SessionID, &d.EventType,
		&d.SchemaVersion, &d.ProtocolVersion, &d.CommandID, &d.CausationID,
		&d.CorrelationID, &d.Origin, &d.WriterIncarnation, &d.AdmissionToken,
		&d.QueueRevision, &recordedStr, &d.Data, &payloadID,
	); err != nil {
		return nil, fmt.Errorf("%w: scan exec_events row: %w", ErrStoreUnavailable, err)
	}
	if payloadID.Valid {
		d.PayloadID = payloadID.String
	}
	parsedTime, err := time.Parse(time.RFC3339Nano, recordedStr)
	if err != nil {
		return nil, fmt.Errorf("%w: parse event timestamp: %w", ErrCorruptHistory, err)
	}
	d.RecordedAt = parsedTime

	if d.Sequence != expectedSeq {
		return nil, fmt.Errorf("%w: sequence gap: got %d, want %d", ErrSequenceGap, d.Sequence, expectedSeq)
	}

	return DecodeEvent(&d)
}

// LookupReceipt retrieves a persisted command execution receipt by command ID.
func (s *Store) LookupReceipt(ctx context.Context, commandID string) (codec.Receipt, bool, error) {
	if s == nil || s.db == nil {
		return codec.Receipt{}, false, ErrStoreUnavailable
	}
	sr, found, err := queryReceipt(ctx, s.db, commandID)
	if err != nil {
		return codec.Receipt{}, false, err
	}
	if !found {
		return codec.Receipt{}, false, nil
	}
	return sr.Receipt, true, nil
}

func verifyRebuiltProjections(state *kernel.State, runRec *RunRecord) error {
	if state.LastSequence != runRec.LastSequence {
		return fmt.Errorf("%w: rebuilt sequence %d != stored sequence %d", ErrCorruptHistory, state.LastSequence, runRec.LastSequence)
	}
	if kernel.DeriveLifecycle(state) != runRec.Lifecycle {
		return fmt.Errorf("%w: rebuilt lifecycle %v != stored lifecycle %v", ErrCorruptHistory, kernel.DeriveLifecycle(state), runRec.Lifecycle)
	}
	if string(kernel.DerivePhase(state)) != string(runRec.Phase) {
		return fmt.Errorf("%w: rebuilt phase %v != stored phase %v", ErrCorruptHistory, kernel.DerivePhase(state), runRec.Phase)
	}
	if string(state.Outcome) != string(runRec.Outcome) {
		return fmt.Errorf("%w: rebuilt outcome %v != stored outcome %v", ErrCorruptHistory, state.Outcome, runRec.Outcome)
	}
	if !slices.Equal(kernel.DeriveWaitReasons(state), runRec.WaitReasons) {
		return fmt.Errorf("%w: rebuilt wait reasons %v != stored wait reasons %v", ErrCorruptHistory, kernel.DeriveWaitReasons(state), runRec.WaitReasons)
	}
	if state.CurrentStepIndex != runRec.StepIndex {
		return fmt.Errorf("%w: rebuilt step index %d != stored step index %d", ErrCorruptHistory, state.CurrentStepIndex, runRec.StepIndex)
	}
	if state.ContextEpoch != runRec.ContextEpoch {
		return fmt.Errorf("%w: rebuilt context epoch %d != stored context epoch %d", ErrCorruptHistory, state.ContextEpoch, runRec.ContextEpoch)
	}
	return nil
}

// RebuildRun reconstructs the pure kernel state of runID by replaying its complete event history.
// It verifies that projections match stored control projections without invoking model or tools.
func (s *Store) RebuildRun(ctx context.Context, runID string) (*kernel.State, error) {
	events, err := s.ReadEvents(ctx, runID, 1, math.MaxInt64)
	if err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, ErrRunNotFound
	}

	state := kernel.NewState()
	for _, evt := range events {
		state = kernel.Evolve(state, evt)
	}

	invariants := kernel.CheckInvariants(state)
	if len(invariants) > 0 {
		return nil, fmt.Errorf("%w: invariant violations during rebuild: %v", ErrCorruptHistory, invariants)
	}

	runRec, err := s.ReadRun(ctx, runID)
	if err != nil {
		return nil, err
	}

	if err := verifyRebuiltProjections(state, &runRec); err != nil {
		return nil, err
	}

	return state, nil
}
