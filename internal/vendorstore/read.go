package vendorstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
)

// RecordedEvent identifies an event in the contiguous session journal.
type RecordedEvent struct {
	Sequence              int64
	Type                  string
	PayloadID, RecordedAt string
	Metadata              EventMetadata
}

// EventsSince returns events oldest first, exclusively after sequence.
func (s *Store) EventsSince(ctx context.Context, session string, sequence int64, limit int) ([]RecordedEvent, error) {
	if limit < 1 || limit > 1000 {
		return nil, errcode.New(errcode.InvalidArgument, "invalid page limit")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT sequence,event_type,COALESCE(payload_id,''),recorded_at,metadata
		FROM vendor_events WHERE session_id=? AND sequence>? ORDER BY sequence LIMIT ?`,
		session, sequence, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	result := []RecordedEvent{}
	for rows.Next() {
		var event RecordedEvent
		var metadata string
		if err = rows.Scan(&event.Sequence, &event.Type, &event.PayloadID, &event.RecordedAt, &metadata); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if decodeErr := json.Unmarshal([]byte(metadata), &event.Metadata); decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, decodeErr)
		}
		result = append(result, event)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}

// OpenPayload authenticates the payload and rejects revoked vault epochs.
func (s *Store) OpenPayload(ctx context.Context, session, id string) ([]byte, error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	if s.vault == nil {
		return nil, vault.ErrLocked
	}
	epoch := s.vault.Snapshot()
	if epoch.State != vault.StateUnlocked {
		return nil, vault.ErrLocked
	}
	var kind, keyID string
	var envelope, wrapped []byte
	err := s.db.QueryRowContext(ctx,
		`SELECT p.kind,p.key_id,p.envelope,k.wrapped_key
		FROM vendor_payloads p
		JOIN vendor_session_keys k ON k.session_id=p.session_id AND k.key_id=p.key_id
		WHERE p.session_id=? AND p.payload_id=?
			AND NOT EXISTS (SELECT 1 FROM vendor_purges WHERE session_id=p.session_id)`,
		session, id).Scan(&kind, &keyID, &envelope, &wrapped)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	key, err := sealing.UnwrapSessionKey(s.vault, "vendor.session_key", session, keyID, wrapped)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer clear(key)
	plaintext, err := sealing.OpenPayload("vendor.payload", session, id, kind, keyID, key, envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	err = s.vault.WithUnlocked(epoch, func() error {
		if s.cache.IsPurged(session) {
			return ErrPurged
		}
		return nil
	})
	if err != nil {
		clear(plaintext)
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return plaintext, nil
}

// StoredItem is the transcript projection with immutable creation sequence.
type StoredItem struct {
	CreatedAt, UpdatedAt                      string
	Metadata                                  ItemMetadata
	ID, TurnID, Kind, PayloadID, RawPayloadID string
	Sequence, Revision                        int64
}

const itemColumns = `item_id,turn_id,kind,sequence,revision,COALESCE(payload_id,''),
	COALESCE(raw_payload_id,''),created_at,updated_at,metadata`

// rowScanner is satisfied by *sql.Row and *sql.Rows, which share the item
// column list.
type rowScanner interface{ Scan(dest ...any) error }

func scanItem(row rowScanner, item *StoredItem, metadata *string) error {
	err := row.Scan(&item.ID, &item.TurnID, &item.Kind, &item.Sequence, &item.Revision,
		&item.PayloadID, &item.RawPayloadID, &item.CreatedAt, &item.UpdatedAt, metadata)
	if err != nil {
		return fmt.Errorf("scan transcript item: %w", err)
	}
	return nil
}

// Transcript returns items before the cursor, oldest first.
func (s *Store) Transcript(ctx context.Context, session string, before int64, limit int) ([]StoredItem, error) {
	if limit < 1 || limit > 1000 {
		return nil, errcode.New(errcode.InvalidArgument, "invalid page limit")
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+itemColumns+`
		FROM (SELECT * FROM vendor_transcript_items
			WHERE session_id=? AND (?=0 OR sequence<?) ORDER BY sequence DESC LIMIT ?)
		ORDER BY sequence`,
		session, before, before, limit)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer closeRows(ctx, rows)
	result := []StoredItem{}
	for rows.Next() {
		var item StoredItem
		var metadata string
		if err = scanItem(rows, &item, &metadata); err != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
		if decodeErr := json.Unmarshal([]byte(metadata), &item.Metadata); decodeErr != nil {
			return nil, fmt.Errorf("%w: %w", ErrUnavailable, decodeErr)
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	return result, nil
}

// ReadItem returns one item and its protected content while the vault is open.
func (s *Store) ReadItem(ctx context.Context, session, id string) (StoredItem, []byte, error) {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return StoredItem{}, nil, vault.ErrLocked
	}
	var item StoredItem
	var metadata string
	err := scanItem(s.db.QueryRowContext(ctx,
		`SELECT `+itemColumns+` FROM vendor_transcript_items WHERE session_id=? AND item_id=?`,
		session, id), &item, &metadata)
	if errors.Is(err, sql.ErrNoRows) {
		return StoredItem{}, nil, ErrItemNotFound
	}
	if err != nil {
		return StoredItem{}, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if err = json.Unmarshal([]byte(metadata), &item.Metadata); err != nil {
		return StoredItem{}, nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if item.PayloadID == "" {
		return item, nil, nil
	}
	content, err := s.OpenPayload(ctx, session, item.PayloadID)
	return item, content, err
}
