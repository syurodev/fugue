package execstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/sealing"
)

// Size limits for protected payloads.
const (
	// MaxPayloadPlaintext is the maximum plaintext size of a single payload: 1 MiB (1,048,576 bytes).
	MaxPayloadPlaintext = sealing.MaxPayloadPlaintext

	// MaxRunPayloadTotal is the maximum cumulative payload bytes allowed per execution run: 64 MiB.
	MaxRunPayloadTotal = sealing.MaxRunPayloadTotal

	// MaxSessionPayloadTotal is the maximum cumulative payload bytes allowed per session: 1 GiB.
	MaxSessionPayloadTotal = sealing.MaxSessionPayloadTotal
)

func validateBatchPayloads(payloads []SealedPayload) (int64, map[string]struct{}, error) {
	var total int64
	seen := make(map[string]struct{}, len(payloads))
	for _, p := range payloads {
		if p.Size < 0 || p.Size > MaxPayloadPlaintext {
			return 0, nil, ErrPayloadTooLarge
		}
		// Bound JSON decoding as well as plaintext accounting. Canonical base64
		// envelopes fit comfortably below twice their maximum ciphertext size.
		if len(p.Envelope) > 2*crypto.MaxCiphertextBytes {
			return 0, nil, ErrInvalidBatch
		}
		var envelope crypto.Envelope
		if err := json.Unmarshal(p.Envelope, &envelope); err != nil {
			return 0, nil, ErrInvalidBatch
		}
		size, err := envelope.PlaintextSize()
		if err != nil || int64(size) != p.Size || envelope.KeyID != p.KeyID {
			return 0, nil, ErrInvalidBatch
		}
		if _, ok := seen[p.PayloadID]; ok {
			continue
		}
		seen[p.PayloadID] = struct{}{}
		total += p.Size
	}
	return total, seen, nil
}

func checkSessionTotalLimit(ctx context.Context, conn *sql.Conn, sessionID string, newBytes int64) error {
	var currentTotal int64
	err := conn.QueryRowContext(
		ctx,
		"SELECT COALESCE(SUM(size), 0) FROM exec_payloads WHERE session_id = ?",
		sessionID,
	).Scan(&currentTotal)
	if err != nil {
		return fmt.Errorf("%w: query session payload total: %w", ErrStoreUnavailable, err)
	}
	if sealing.ExceedsLimit(currentTotal, newBytes, MaxSessionPayloadTotal) {
		return ErrSessionPayloadLimitExceeded
	}
	return nil
}

func getProtectedInputAddition(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	seenPayloads map[string]struct{},
) (int64, error) {
	if req.ProtectedInputID == "" {
		return 0, nil
	}
	if _, inBatch := seenPayloads[req.ProtectedInputID]; inBatch {
		return 0, nil
	}
	var inputSize int64
	scanErr := conn.QueryRowContext(
		ctx,
		`SELECT COALESCE(size, 0) FROM exec_payloads
		 WHERE session_id = ? AND payload_id = ?
		   AND payload_id NOT IN (
			SELECT payload_id FROM exec_events WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT payload_id FROM exec_transcript WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT input_payload_id FROM exec_receipts WHERE run_id = ? AND input_payload_id IS NOT NULL
		   )`,
		req.SessionID, req.ProtectedInputID, req.RunID, req.RunID, req.RunID,
	).Scan(&inputSize)
	if scanErr != nil {
		if errors.Is(scanErr, sql.ErrNoRows) {
			return 0, nil
		}
		return 0, fmt.Errorf("%w: query protected input size: %w", ErrStoreUnavailable, scanErr)
	}
	if inputSize <= 0 {
		return 0, nil
	}
	return inputSize, nil
}

func checkRunTotalLimit(
	ctx context.Context,
	conn *sql.Conn,
	req *AppendRequest,
	newBatchBytes int64,
	seenPayloads map[string]struct{},
) error {
	var currentTotal int64
	err := conn.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(size), 0) FROM exec_payloads
		 WHERE session_id = ? AND payload_id IN (
			SELECT payload_id FROM exec_events WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT payload_id FROM exec_transcript WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT input_payload_id FROM exec_receipts WHERE run_id = ? AND input_payload_id IS NOT NULL
		 )`,
		req.SessionID, req.RunID, req.RunID, req.RunID,
	).Scan(&currentTotal)
	if err != nil {
		return fmt.Errorf("%w: query run payload total: %w", ErrStoreUnavailable, err)
	}

	runAddition := newBatchBytes
	inputSize, err := getProtectedInputAddition(ctx, conn, req, seenPayloads)
	if err != nil {
		return err
	}
	if inputSize > 0 {
		if inputSize > math.MaxInt64-runAddition {
			return ErrRunPayloadLimitExceeded
		}
		runAddition += inputSize
	}

	if sealing.ExceedsLimit(currentTotal, runAddition, MaxRunPayloadTotal) {
		return ErrRunPayloadLimitExceeded
	}
	return nil
}

func (s *Store) checkPayloadLimits(ctx context.Context, conn *sql.Conn, req *AppendRequest) error {
	newBatchBytes, seenPayloads, err := validateBatchPayloads(req.SealedPayloads)
	if err != nil {
		return err
	}
	if newBatchBytes == 0 && req.ProtectedInputID == "" {
		return nil
	}
	if err := checkSessionTotalLimit(ctx, conn, req.SessionID, newBatchBytes); err != nil {
		return err
	}
	return checkRunTotalLimit(ctx, conn, req, newBatchBytes, seenPayloads)
}

// PayloadUsage returns the cumulative stored payload bytes for sessionID.
func (s *Store) PayloadUsage(ctx context.Context, sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreUnavailable
	}
	if sessionID == "" || len(sessionID) > 128 {
		return 0, errcode.New(errcode.InvalidArgument, "session ID is invalid")
	}
	var total int64
	err := s.db.QueryRowContext(
		ctx,
		"SELECT COALESCE(SUM(size), 0) FROM exec_payloads WHERE session_id = ?",
		sessionID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("%w: query payload usage: %w", ErrStoreUnavailable, err)
	}
	return total, nil
}

// RunPayloadUsage returns the cumulative stored payload bytes for runID in sessionID.
func (s *Store) RunPayloadUsage(ctx context.Context, sessionID, runID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, ErrStoreUnavailable
	}
	if sessionID == "" || len(sessionID) > 128 || runID == "" || len(runID) > 128 {
		return 0, errcode.New(errcode.InvalidArgument, "session ID or run ID is invalid")
	}
	var total int64
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COALESCE(SUM(size), 0) FROM exec_payloads
		 WHERE session_id = ? AND payload_id IN (
			SELECT payload_id FROM exec_events WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT payload_id FROM exec_transcript WHERE run_id = ? AND payload_id IS NOT NULL
			UNION
			SELECT input_payload_id FROM exec_receipts WHERE run_id = ? AND input_payload_id IS NOT NULL
		 )`,
		sessionID, runID, runID, runID,
	).Scan(&total)
	if err != nil {
		return 0, fmt.Errorf("%w: query run payload usage: %w", ErrStoreUnavailable, err)
	}
	return total, nil
}
