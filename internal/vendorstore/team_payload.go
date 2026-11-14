package vendorstore

// Sealing and vault-epoch helpers for the team store. Protected payloads are
// sealed under a per-team key; raw key material is never cached, so a vault
// lock cannot leave it behind.

import (
	"fmt"

	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/vault"
)

func (s *Store) sealEventPayloadIfPresent(teamID, keyID string, wrappedKey []byte, ev *AppendTeamEventInput) ([]byte, error) {
	if len(ev.ProtectedPayload) == 0 {
		return nil, nil
	}
	rawKey, err := s.unwrapTeamKey(teamID, keyID, wrappedKey)
	if err != nil {
		return nil, err
	}
	defer clear(rawKey)
	env, err := sealing.SealPayload(randomReader{s.random}, teamEventRecordType, teamID, ev.EventID, string(ev.EventType), keyID, rawKey, ev.ProtectedPayload)
	if err != nil {
		return nil, fmt.Errorf("seal event payload: %w", err)
	}
	return env, nil
}

// unwrapTeamKey unwraps the per-team key for one use. The raw key is never
// cached: a vault lock clears the cache through its guard, but an install that
// races that guard would retain key material while the vault is locked. The
// vendor payload path unwraps per use for the same reason.
func (s *Store) unwrapTeamKey(teamID, keyID string, wrappedKey []byte) ([]byte, error) {
	raw, err := sealing.UnwrapSessionKey(s.vault, teamKeyRecordType, teamID, keyID, wrappedKey)
	if err != nil {
		return nil, fmt.Errorf("%w: unwrap team key: %w", ErrUnavailable, err)
	}
	return raw, nil
}

func (s *Store) requireUnlockedVault() (vault.Snapshot, error) {
	if s.vault == nil {
		return vault.Snapshot{}, fmt.Errorf("%w: %w", ErrUnavailable, vault.ErrLocked)
	}
	snap := s.vault.Snapshot()
	if snap.State != vault.StateUnlocked {
		return snap, fmt.Errorf("%w: %w", ErrUnavailable, vault.ErrLocked)
	}
	return snap, nil
}

func (s *Store) teamPayloadGate(payload []byte) (vault.Snapshot, bool, error) {
	if len(payload) == 0 {
		return vault.Snapshot{}, false, nil
	}
	snap, err := s.requireUnlockedVault()
	return snap, true, err
}

func (s *Store) commitTeamTx(snap vault.Snapshot, needVault bool, commitFn func() error) error {
	s.prepare.Lock()
	fault := s.beforeCommit
	s.prepare.Unlock()
	if fault != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, fault)
	}
	if needVault {
		if err := s.vault.WithUnlocked(snap, commitFn); err != nil {
			return fmt.Errorf("commit sealed team tx: %w", err)
		}
		return nil
	}
	if err := commitFn(); err != nil {
		return fmt.Errorf("commit team tx: %w", err)
	}
	return nil
}
