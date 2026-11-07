package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
)

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
	rawKey, openErr := sealing.UnwrapSessionKey(s.vault, SessionKeyRecordType, sessionID, keyID, wrappedKey)
	if openErr != nil {
		return nil, false, fmt.Errorf("%w: %w", ErrStoreUnavailable, openErr)
	}
	return rawKey, true, nil
}

func (s *Store) generateFreshSessionKey(sessionID string) ([]byte, *SealedSessionKey, error) {
	if s.vault == nil || s.vault.Snapshot().State != vault.StateUnlocked {
		return nil, nil, vault.ErrLocked
	}

	rawKey, sealedKey, err := sealing.GenerateSessionKey(s.vault, s.random, SessionKeyRecordType, sessionID, SessionKeyDefaultKeyID)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrStoreUnavailable, err)
	}
	return rawKey, sealedKey, nil
}

func (s *Store) getOrCreateSessionKey(ctx context.Context, sessionID string) ([]byte, *SealedSessionKey, error) {
	s.keyCache.Prepare.Lock()
	defer s.keyCache.Prepare.Unlock()
	if s.keyCache.IsPurged(sessionID) {
		return nil, nil, ErrSessionPurging
	}
	if s.vault == nil {
		return nil, nil, vault.ErrLocked
	}
	epoch := s.vault.Snapshot()
	if epoch.State != vault.StateUnlocked {
		return nil, nil, vault.ErrLocked
	}
	if raw, pending, found := s.keyCache.Prepared(sessionID); found {
		if err := s.validatePayloadEpoch(epoch, sessionID); err != nil {
			clear(raw)
			return nil, nil, err
		}
		return raw, pending, nil
	}
	var purging int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", sessionID).Scan(&purging); err != nil {
		return nil, nil, fmt.Errorf("%w: query session purge state: %w", ErrStoreUnavailable, err)
	}
	if purging != 0 {
		return nil, nil, ErrSessionPurging
	}
	raw, found, err := s.loadCommittedSessionKey(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	var pending *SealedSessionKey
	if !found {
		raw, pending, err = s.generateFreshSessionKey(sessionID)
		if err != nil {
			return nil, nil, err
		}
	}
	err = s.vault.WithUnlocked(epoch, func() error { return s.installKey(sessionID, raw, pending) })
	if err != nil {
		clear(raw)
		return nil, nil, fmt.Errorf("cache key authorization: %w", err)
	}
	return raw, pending, nil
}

func (s *Store) installKey(sessionID string, raw []byte, pending *SealedSessionKey) error {
	if !s.keyCache.Install(sessionID, raw, pending) {
		return ErrSessionPurging
	}
	return nil
}
