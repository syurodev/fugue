package sealing

import (
	"fmt"

	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/vault"
)

// UnwrapSessionKey opens a session key bound to its session and key identifiers.
// Contextual error wraps do not add a store error code; callers must wrap them
// with their store's typed error, preserving existing dependency codes.
func UnwrapSessionKey(v *vault.Service, recordType, sessionID, keyID string, wrapped []byte) ([]byte, error) {
	if v == nil || v.Snapshot().State != vault.StateUnlocked {
		return nil, vault.ErrLocked
	}
	key, err := v.OpenRecord(vault.RecordBinding{RecordType: recordType, RecordID: sessionID, KeyID: keyID}, wrapped)
	if err != nil {
		return nil, fmt.Errorf("unwrap session key: %w", err)
	}
	return key, nil
}

// GenerateSessionKey creates a key and wraps it under the vault.
// Contextual error wraps do not add a store error code; callers must wrap them
// with their store's typed error, preserving existing dependency codes.
func GenerateSessionKey(v *vault.Service, random ids.Random, recordType, sessionID, keyID string) ([]byte, *SessionKey, error) {
	if v == nil || v.Snapshot().State != vault.StateUnlocked {
		return nil, nil, vault.ErrLocked
	}
	raw := make([]byte, 32)
	success := false
	defer func() {
		if !success {
			clear(raw)
		}
	}()
	if err := random.Read(raw); err != nil {
		return nil, nil, fmt.Errorf("generate session key bytes: %w", err)
	}
	wrapped, err := v.SealRecord(vault.RecordBinding{RecordType: recordType, RecordID: sessionID, KeyID: keyID}, raw)
	if err != nil {
		return nil, nil, fmt.Errorf("wrap session key: %w", err)
	}
	success = true
	return raw, &SessionKey{KeyID: keyID, WrappedKey: wrapped}, nil
}
