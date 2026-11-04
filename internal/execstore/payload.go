package execstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	// SessionKeyRecordType is the vault record binding type for execution session keys.
	SessionKeyRecordType = "exec.session_key"

	// SessionKeyDefaultKeyID is the default key ID for v1 session keys.
	SessionKeyDefaultKeyID = "v1"

	// PayloadRecordType is the associated data record type for sealed execution payloads.
	PayloadRecordType = "exec.payload"
)

type sessionKeyCache struct {
	mu     sync.RWMutex
	keys   map[string][]byte
	purged map[string]bool
}

func newSessionKeyCache() *sessionKeyCache {
	return &sessionKeyCache{
		mu:     sync.RWMutex{},
		keys:   make(map[string][]byte),
		purged: make(map[string]bool),
	}
}

func (c *sessionKeyCache) get(sessionID string) ([]byte, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.purged[sessionID] {
		return nil, false
	}
	k, ok := c.keys[sessionID]
	if !ok {
		return nil, false
	}
	cp := make([]byte, len(k))
	copy(cp, k)
	return cp, true
}

func (c *sessionKeyCache) purge(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.purged[sessionID] = true
	if k, ok := c.keys[sessionID]; ok {
		for i := range k {
			k[i] = 0
		}
		delete(c.keys, sessionID)
	}
}

func (c *sessionKeyCache) isPurged(sessionID string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.purged[sessionID]
}

func (c *sessionKeyCache) clearAndZero() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, k := range c.keys {
		for i := range k {
			k[i] = 0
		}
		delete(c.keys, id)
	}
}

type randomReader struct {
	r ids.Random
}

func (rr randomReader) Read(p []byte) (int, error) {
	if err := rr.r.Read(p); err != nil {
		return 0, fmt.Errorf("read random bytes: %w", err)
	}
	return len(p), nil
}

func payloadAssociatedData(sessionID, payloadID, kind, keyID string) crypto.AssociatedData {
	// Canonical unambiguous format: len(sessionID):sessionID:len(payloadID):payloadID:kind
	recordID := fmt.Sprintf("%d:%s:%d:%s:%s", len(sessionID), sessionID, len(payloadID), payloadID, kind)
	return crypto.AssociatedData{
		RecordType: PayloadRecordType,
		RecordID:   recordID,
		KeyID:      keyID,
	}
}

func isValidKind(kind string) bool {
	for _, r := range kind {
		if (r < 'a' || r > 'z') && r != '_' {
			return false
		}
	}
	return true
}

func (s *Store) loadCommittedSessionKey(ctx context.Context, sessionID string) ([]byte, bool, error) {
	var (
		keyID      string
		wrappedKey []byte
	)
	err := s.db.QueryRowContext(ctx, "SELECT key_id, wrapped_key FROM exec_session_keys WHERE session_id = ?", sessionID).Scan(&keyID, &wrappedKey)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("%w: query exec_session_keys: %w", ErrStoreUnavailable, err)
	}
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return nil, false, vault.ErrLocked
	}
	binding := vault.RecordBinding{
		RecordType: SessionKeyRecordType,
		RecordID:   sessionID,
		KeyID:      keyID,
	}
	rawKey, openErr := s.vault.OpenRecord(binding, wrappedKey)
	if openErr != nil {
		return nil, false, fmt.Errorf("%w: unwrap session key: %w", ErrStoreUnavailable, openErr)
	}
	s.keyCache.keys[sessionID] = rawKey
	cp := make([]byte, len(rawKey))
	copy(cp, rawKey)
	return cp, true, nil
}

func (s *Store) generateFreshSessionKey(sessionID string) ([]byte, *SealedSessionKey, error) {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return nil, nil, vault.ErrLocked
	}

	rawKey := make([]byte, 32)
	if err := s.random.Read(rawKey); err != nil {
		return nil, nil, fmt.Errorf("%w: generate session key bytes: %w", ErrStoreUnavailable, err)
	}

	binding := vault.RecordBinding{
		RecordType: SessionKeyRecordType,
		RecordID:   sessionID,
		KeyID:      SessionKeyDefaultKeyID,
	}
	wrapped, sealErr := s.vault.SealRecord(binding, rawKey)
	if sealErr != nil {
		return nil, nil, fmt.Errorf("%w: wrap session key: %w", ErrStoreUnavailable, sealErr)
	}

	s.keyCache.keys[sessionID] = rawKey
	cp := make([]byte, len(rawKey))
	copy(cp, rawKey)

	sealedKey := &SealedSessionKey{
		KeyID:      SessionKeyDefaultKeyID,
		WrappedKey: wrapped,
	}
	return cp, sealedKey, nil
}

func (s *Store) getOrCreateSessionKey(ctx context.Context, sessionID string) ([]byte, *SealedSessionKey, error) {
	s.keyCache.mu.Lock()
	defer s.keyCache.mu.Unlock()

	if s.keyCache.purged[sessionID] {
		return nil, nil, ErrSessionPurging
	}
	if k, ok := s.keyCache.keys[sessionID]; ok {
		cp := make([]byte, len(k))
		copy(cp, k)
		return cp, nil, nil
	}

	rawKey, found, err := s.loadCommittedSessionKey(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if found {
		return rawKey, nil, nil
	}
	return s.generateFreshSessionKey(sessionID)
}

func (s *Store) validateSealArgs(sessionID, payloadID, kind string, size int64) (string, error) {
	if s == nil || s.db == nil {
		return "", ErrStoreUnavailable
	}
	if sessionID == "" || len(sessionID) > 128 {
		return "", errcode.New(errcode.InvalidArgument, "session ID is invalid")
	}
	resolvedID := payloadID
	if resolvedID == "" {
		id, err := s.ids.NewID()
		if err != nil {
			return "", fmt.Errorf("%w: generate payload ID: %w", ErrStoreUnavailable, err)
		}
		resolvedID = id
	}
	if len(resolvedID) > 128 {
		return "", errcode.New(errcode.InvalidArgument, "payload ID is too long")
	}
	if kind == "" || len(kind) > 64 || !isValidKind(kind) {
		return "", errcode.New(errcode.InvalidArgument, "payload kind is invalid")
	}
	if size > MaxPayloadPlaintext {
		return "", ErrPayloadTooLarge
	}
	return resolvedID, nil
}

// SealPayload encrypts plaintext under the session's data key.
// If the session does not yet have a key, a fresh 256-bit key is generated,
// wrapped via vault.SealRecord, and returned in SealedSessionKey.
func (s *Store) SealPayload(
	ctx context.Context,
	sessionID string,
	payloadID string,
	kind string,
	plaintext []byte,
) (SealedPayload, *SealedSessionKey, error) {
	resolvedID, err := s.validateSealArgs(sessionID, payloadID, kind, int64(len(plaintext)))
	if err != nil {
		return SealedPayload{}, nil, err
	}

	rawKey, sealedKey, err := s.getOrCreateSessionKey(ctx, sessionID)
	if err != nil {
		return SealedPayload{}, nil, err
	}

	ad := payloadAssociatedData(sessionID, resolvedID, kind, SessionKeyDefaultKeyID)
	env, err := crypto.SealWithRandom(s.randomReader, ad, rawKey, plaintext)
	if err != nil {
		return SealedPayload{}, nil, fmt.Errorf("%w: seal payload: %w", ErrStoreUnavailable, err)
	}

	envBytes, err := json.Marshal(env)
	if err != nil {
		return SealedPayload{}, nil, fmt.Errorf("%w: marshal envelope: %w", ErrStoreUnavailable, err)
	}

	return SealedPayload{
		PayloadID: resolvedID,
		Kind:      kind,
		KeyID:     SessionKeyDefaultKeyID,
		Size:      int64(len(plaintext)),
		Envelope:  envBytes,
	}, sealedKey, nil
}

func (s *Store) validateOpenArgs(sessionID, payloadID string) error {
	if s == nil || s.db == nil {
		return ErrStoreUnavailable
	}
	if sessionID == "" || payloadID == "" {
		return errcode.New(errcode.InvalidArgument, "session ID and payload ID are required")
	}
	if s.keyCache != nil && s.keyCache.isPurged(sessionID) {
		return ErrSessionPurging
	}
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return vault.ErrLocked
	}
	return nil
}

func (s *Store) readPayloadRow(ctx context.Context, sessionID, payloadID string) (string, string, []byte, error) {
	var kind, keyID string
	var envelope []byte
	err := s.db.QueryRowContext(
		ctx,
		"SELECT kind, key_id, envelope FROM exec_payloads WHERE session_id = ? AND payload_id = ?",
		sessionID, payloadID,
	).Scan(&kind, &keyID, &envelope)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil, errcode.New(errcode.NotFound, "payload not found")
	}
	if err != nil {
		return "", "", nil, fmt.Errorf("%w: query exec_payloads: %w", ErrStoreUnavailable, err)
	}
	return kind, keyID, envelope, nil
}

// OpenPayload authenticates and decrypts payloadID belonging to sessionID.
func (s *Store) OpenPayload(ctx context.Context, sessionID, payloadID string) ([]byte, error) {
	if err := s.validateOpenArgs(sessionID, payloadID); err != nil {
		return nil, err
	}

	kind, keyID, envelope, err := s.readPayloadRow(ctx, sessionID, payloadID)
	if err != nil {
		return nil, err
	}

	rawKey, _, err := s.getOrCreateSessionKey(ctx, sessionID)
	if err != nil {
		return nil, err
	}

	var env crypto.Envelope
	if unmarshalErr := json.Unmarshal(envelope, &env); unmarshalErr != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "invalid payload envelope", unmarshalErr)
	}

	ad := payloadAssociatedData(sessionID, payloadID, kind, keyID)
	plaintext, openErr := crypto.OpenWithAD(rawKey, &env, ad)
	if openErr != nil {
		return nil, errcode.Wrap(errcode.CodeOf(openErr), "open payload envelope", openErr)
	}
	return plaintext, nil
}
