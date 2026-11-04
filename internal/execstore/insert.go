package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func (s *Store) recheckRunRevision(ctx context.Context, conn *sql.Conn, req *AppendRequest) (uint64, uint64, error) {
	var storedSeq, storedRev uint64
	err := conn.QueryRowContext(
		ctx,
		"SELECT last_sequence, revision FROM exec_runs WHERE run_id = ?",
		req.RunID,
	).Scan(&storedSeq, &storedRev)

	if errors.Is(err, sql.ErrNoRows) {
		if req.ExpectedRunRevision != 0 {
			return 0, 0, fmt.Errorf("%w: run does not exist, expected revision %d", ErrRunNotFound, req.ExpectedRunRevision)
		}
		return 1, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("%w: read exec_runs: %w", ErrStoreUnavailable, err)
	}

	if req.ExpectedRunRevision != storedRev {
		return 0, 0, fmt.Errorf("%w: run revision got %d, expected %d", ErrStaleRevision, storedRev, req.ExpectedRunRevision)
	}

	return storedRev + 1, storedSeq, nil
}

func (s *Store) writePayloadRows(ctx context.Context, conn *sql.Conn, req *AppendRequest) error {
	if req.SealedSessionKey != nil {
		if err := s.checkFault(FaultBeforeSessionKeyInsert); err != nil {
			return err
		}
		if err := insertSessionKey(ctx, conn, req.SessionID, req.SealedSessionKey, s.clk.Now()); err != nil {
			return err
		}
	}
	if len(req.SealedPayloads) > 0 {
		if err := s.checkFault(FaultBeforePayloadInsert); err != nil {
			return err
		}
		if err := insertPayloads(ctx, conn, req.SessionID, req.SealedPayloads); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) writeProjectionRows(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
	admission *storedAdmission,
	newRunRevision uint64,
) error {
	if admission == nil {
		if err := insertInitialAdmission(ctx, conn, req, state); err != nil {
			return err
		}
	}
	if len(req.Events) == 0 {
		return nil
	}
	if err := s.checkFault(FaultBeforeRunInsert); err != nil {
		return err
	}
	if err := writeRunProjection(ctx, conn, req, state, newRunRevision); err != nil {
		return err
	}
	if err := s.checkFault(FaultBeforeEventAppend); err != nil {
		return err
	}
	return s.insertEvents(ctx, conn, req)
}

func (s *Store) writeTrailingRows(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
	admission *storedAdmission,
) error {
	if len(req.Events) > 0 {
		if err := s.checkFault(FaultBeforeProjections); err != nil {
			return err
		}
		if err := writeInvocationProjections(ctx, conn, req.RunID, state); err != nil {
			return err
		}
	}
	if err := s.checkFault(FaultBeforeTranscript); err != nil {
		return err
	}
	if err := writeTranscriptProjections(ctx, conn, req, state); err != nil {
		return err
	}
	if err := s.checkFault(FaultBeforeReceiptInsert); err != nil {
		return err
	}
	if err := insertReceipt(ctx, conn, req, s.clk.Now()); err != nil {
		return err
	}
	if err := s.checkFault(FaultBeforeAdmissionUpdate); err != nil {
		return err
	}
	if admission != nil {
		return s.updateAdmissionRow(ctx, conn, req, state, admission)
	}
	return nil
}

func (s *Store) writeBatchRows(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	state *kernel.State,
	admission *storedAdmission,
	newRunRevision uint64,
) error {
	if err := s.writePayloadRows(ctx, conn, req); err != nil {
		return err
	}
	if err := s.writeProjectionRows(ctx, conn, req, state, admission, newRunRevision); err != nil {
		return err
	}
	return s.writeTrailingRows(ctx, conn, req, state, admission)
}

func insertSessionKey(ctx context.Context, conn *sql.Conn, sessionID string, key *SealedSessionKey, now time.Time) error {
	_, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_session_keys (session_id, key_id, wrapped_key, created_at, shredded_at)
		 VALUES (?, ?, ?, ?, NULL)`,
		sessionID, key.KeyID, key.WrappedKey, now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("%w: insert exec_session_keys: %w", ErrInvalidBatch, err)
	}
	return nil
}

func insertPayloads(ctx context.Context, conn *sql.Conn, sessionID string, payloads []SealedPayload) error {
	for _, p := range payloads {
		_, err := conn.ExecContext(
			ctx,
			`INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			p.PayloadID, sessionID, p.Kind, p.KeyID, p.Size, p.Envelope,
		)
		if err != nil {
			return fmt.Errorf("%w: insert exec_payloads %s: %w", ErrInvalidBatch, p.PayloadID, err)
		}
	}
	return nil
}

func (s *Store) insertEvents(ctx context.Context, conn *sql.Conn, req *AppendRequest) error {
	for _, evt := range req.Events {
		id, err := s.ids.NewID()
		if err != nil {
			return fmt.Errorf("%w: generate event ID: %w", ErrStoreUnavailable, err)
		}
		var queueRev uint64
		if req.QueueFence != nil {
			queueRev = req.QueueFence.QueueRevision
		}
		payloadID := req.ProtectedInputID

		durable, err := EncodeEvent(evt, &StorageEnvelope{
			EventID:           id,
			SessionID:         req.SessionID,
			SchemaVersion:     CurrentSchemaVersion,
			ProtocolVersion:   CurrentProtocolVersion,
			CorrelationID:     req.CorrelationID,
			WriterIncarnation: s.host.LockGeneration,
			QueueRevision:     queueRev,
			RecordedAt:        s.clk.Now(),
			PayloadID:         payloadID,
		})
		if err != nil {
			return err
		}

		var payloadParam *string
		if durable.PayloadID != "" {
			p := durable.PayloadID
			payloadParam = &p
		}

		_, err = conn.ExecContext(
			ctx,
			`INSERT INTO exec_events (
				run_id, sequence, event_id, session_id, event_type,
				schema_version, protocol_version, command_id, causation_id,
				correlation_id, origin, writer_incarnation, admission_token,
				queue_revision, recorded_at, data, payload_id
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			durable.RunID, durable.Sequence, durable.EventID, durable.SessionID,
			durable.EventType, durable.SchemaVersion, durable.ProtocolVersion,
			durable.CommandID, durable.CausationID, durable.CorrelationID,
			durable.Origin, durable.WriterIncarnation, durable.AdmissionToken,
			durable.QueueRevision, durable.RecordedAt.Format(time.RFC3339Nano),
			durable.Data, payloadParam,
		)
		if err != nil {
			return fmt.Errorf("%w: insert exec_events %d: %w", ErrInvalidBatch, durable.Sequence, err)
		}
	}
	return nil
}

func insertReceipt(ctx context.Context, conn *sql.Conn, req *AppendRequest, now time.Time) error {
	var payloadID *string
	if req.ProtectedInputID != "" {
		p := req.ProtectedInputID
		payloadID = &p
	}
	var metaBytes []byte
	if len(req.MetadataInput) > 0 {
		metaBytes = req.MetadataInput
	}
	resultStr := "applied"
	if req.Result.IsRejection() {
		resultStr = "rejected"
	} else if req.Result.FirstSequence == 0 && req.Result.LastSequence == 0 {
		resultStr = "noop"
	}

	_, err := conn.ExecContext(
		ctx,
		`INSERT INTO exec_receipts (
			command_id, session_id, run_id, kind, principal, target,
			codec_version, input_payload_id, metadata_input, result,
			first_sequence, last_sequence, rejection_code, recorded_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		req.CommandID, req.SessionID, req.RunID, req.Kind, req.Principal, req.Target,
		req.CodecVersion, payloadID, metaBytes, resultStr,
		req.Result.FirstSequence, req.Result.LastSequence, req.Result.RejectionCode,
		now.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return fmt.Errorf("%w: insert exec_receipts: %w", ErrInvalidBatch, err)
	}
	return nil
}

type storedReceipt struct {
	Receipt   codec.Receipt
	SessionID string
	RunID     string
}

func (s *Store) lookupReceiptForDedup(ctx context.Context, commandID string) (storedReceipt, bool, error) {
	return queryReceipt(ctx, s.db, commandID)
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func queryReceipt(ctx context.Context, q queryer, commandID string) (storedReceipt, bool, error) {
	var (
		cmdID, sessID, runID, kind, principal, target, codecVer, resultStr, code string
		inputPayloadID                                                           sql.NullString
		metaInput                                                                []byte
		firstSeq, lastSeq                                                        uint64
	)
	err := q.QueryRowContext(
		ctx,
		`SELECT command_id, session_id, run_id, kind, principal, target, codec_version,
				input_payload_id, metadata_input, result, first_sequence,
				last_sequence, rejection_code
		 FROM exec_receipts WHERE command_id = ?`,
		commandID,
	).Scan(
		&cmdID, &sessID, &runID, &kind, &principal, &target, &codecVer,
		&inputPayloadID, &metaInput, &resultStr, &firstSeq,
		&lastSeq, &code,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return storedReceipt{}, false, nil
	}
	if err != nil {
		return storedReceipt{}, false, fmt.Errorf("%w: query receipt: %w", ErrStoreUnavailable, err)
	}

	payloadID := ""
	if inputPayloadID.Valid {
		payloadID = inputPayloadID.String
	}
	metadataOnly := len(metaInput) > 0

	receipt := codec.Receipt{
		CommandID:      cmdID,
		Kind:           kind,
		Principal:      principal,
		Target:         target,
		CodecVersion:   codecVer,
		InputPayloadID: payloadID,
		MetadataOnly:   metadataOnly,
		CanonicalInput: metaInput,
		Result: codec.ReceiptResult{
			FirstSequence: firstSeq,
			LastSequence:  lastSeq,
			RejectionCode: code,
			Message:       "",
		},
	}
	return storedReceipt{Receipt: receipt, SessionID: sessID, RunID: runID}, true, nil
}
