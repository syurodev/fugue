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
	if err := reservePayload(ctx, tx, session, int64(len(data))); err != nil {
		return "", err
	}
	var err error
	var wrapped []byte
	err = tx.QueryRowContext(ctx,
		"SELECT wrapped_key FROM vendor_session_keys WHERE session_id=?", session).Scan(&wrapped)
	var key []byte
	defer func() { clear(key) }()
	if errors.Is(err, sql.ErrNoRows) {
		var candidate *sealing.SessionKey
		key, candidate, err = sealing.GenerateSessionKey(s.vault, s.random, "vendor.session_key", session, "v1")
		if err == nil {
			_, err = tx.ExecContext(ctx,
				"INSERT INTO vendor_session_keys(session_id,key_id,wrapped_key,created_at) VALUES (?,'v1',?,?)",
				session, candidate.WrappedKey, now)
		}
	} else if err == nil {
		key, err = sealing.UnwrapSessionKey(s.vault, "vendor.session_key", session, "v1", wrapped)
	}
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	idBytes := make([]byte, 16)
	if err = s.random.Read(idBytes); err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	id := hex.EncodeToString(idBytes)
	envelope, err := sealing.SealPayload(randomReader{s.random}, "vendor.payload", session, id, kind, "v1", key, data)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	_, err = tx.ExecContext(ctx,
		"INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) VALUES (?,?,?,'v1',?,?)",
		id, session, kind, len(data), envelope)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return id, nil
}

func reservePayload(ctx context.Context, tx *sql.Tx, session string, size int64) error {
	if size < 0 || size > sealing.MaxPayloadPlaintext {
		return errcode.New(errcode.InvalidArgument, "payload limit exceeded")
	}
	// Subtraction keeps the guarded addition within the signed integer range.
	result, err := tx.ExecContext(ctx, `UPDATE vendor_sessions SET payload_bytes=payload_bytes+?
  WHERE session_id=? AND payload_bytes <= ?`, size, session, int64(sealing.MaxSessionPayloadTotal)-size)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if count == 0 {
		var exists int
		err = tx.QueryRowContext(ctx, `SELECT 1 FROM vendor_sessions WHERE session_id=?`, session).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrSessionNotFound
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		return errcode.New(errcode.FailedPrecondition, "session payload limit exceeded")
	}
	return nil
}

type randomReader struct{ random ids.Random }

func (r randomReader) Read(p []byte) (int, error) {
	if err := r.random.Read(p); err != nil {
		return 0, fmt.Errorf("%w: random: %w", ErrUnavailable, err)
	}
	return len(p), nil
}
