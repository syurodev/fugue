package vendorstore

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/sealing"
)

// ErrUnavailable classifies vendor storage and sealing failures.
var ErrUnavailable = errcode.New(errcode.Unavailable, "vendor store unavailable")

// ErrPurged prevents resurrection after crypto-shred.
var ErrPurged = errcode.New(errcode.FailedPrecondition, "vendor session purged")

func (s *Store) writePayload(ctx context.Context, tx *sql.Tx, session, kind string, data []byte, now string) (string, error) {
	if data == nil {
		return "", nil
	}
	if len(data) > sealing.MaxPayloadPlaintext {
		return "", errcode.New(errcode.InvalidArgument, "payload limit exceeded")
	}
	var total int64
	if operationErr1 := tx.QueryRowContext(ctx, "SELECT COALESCE(SUM(size),0) FROM vendor_payloads WHERE session_id=?", session).Scan(&total); operationErr1 != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, operationErr1)
	}
	if sealing.ExceedsLimit(total, int64(len(data)), sealing.MaxSessionPayloadTotal) {
		return "", errcode.New(errcode.FailedPrecondition, "session payload limit exceeded")
	}
	var wrapped []byte
	err := tx.QueryRowContext(ctx, "SELECT wrapped_key FROM vendor_session_keys WHERE session_id=?", session).Scan(&wrapped)
	var key []byte
	defer func() { clear(key) }()
	if errors.Is(err, sql.ErrNoRows) {
		var candidate *sealing.SessionKey
		key, candidate, err = sealing.GenerateSessionKey(s.vault, s.random, "vendor.session_key", session, "v1")
		if err == nil {
			_, err = tx.ExecContext(ctx, "INSERT INTO vendor_session_keys(session_id,key_id,wrapped_key,created_at) VALUES (?,'v1',?,?)", session, candidate.WrappedKey, now)
		}
	} else if err == nil {
		key, err = sealing.UnwrapSessionKey(s.vault, "vendor.session_key", session, "v1", wrapped)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	idBytes := make([]byte, 16)
	if operationErr2 := s.random.Read(idBytes); operationErr2 != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, operationErr2)
	}
	id := hex.EncodeToString(idBytes)
	envelope, err := sealing.SealPayload(randomReader{s.random}, "vendor.payload", session, id, kind, "v1", key, data)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) VALUES (?,?,?,'v1',?,?)", id, session, kind, len(data), envelope)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return id, nil
}

type randomReader struct{ random ids.Random }

func (r randomReader) Read(p []byte) (int, error) {
	if operationErr3 := r.random.Read(p); operationErr3 != nil {
		return 0, fmt.Errorf("%w: random: %w", ErrUnavailable, operationErr3)
	}
	return len(p), nil
}
