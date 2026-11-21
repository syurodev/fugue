package vendorstore

// Sealing and vault-epoch helpers for the team store. Protected payloads are
// sealed under a per-team key; raw key material is never cached, so a vault
// lock cannot leave it behind.

import (
	"context"
	"database/sql"
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

func (s *Store) commitFault() error {
	s.faultMu.Lock()
	fault := s.beforeCommit
	s.faultMu.Unlock()
	return fault
}

// SetTeamAppendedObserver binds the channel topic publisher (F11.10b): the
// observer runs after every committed team event so the `team:<id>` topic can
// publish the channel lines the event projects. Nil until the host wires it;
// a nil observer makes appends silent, and catch-up still covers every
// subscriber (the rows are committed before the observer runs).
func (s *Store) SetTeamAppendedObserver(fn func(teamID string, seq int64)) {
	s.teamAppendedMu.Lock()
	defer s.teamAppendedMu.Unlock()
	s.teamAppended = fn
}

func (s *Store) notifyTeamAppended(teamID string, seq int64) {
	s.teamAppendedMu.Lock()
	fn := s.teamAppended
	s.teamAppendedMu.Unlock()
	if fn != nil {
		fn(teamID, seq)
	}
}

func (s *Store) commitTeamTx(teamID string, seq int64, snap vault.Snapshot, needVault bool, commitFn func() error) error {
	if fault := s.commitFault(); fault != nil {
		return fmt.Errorf("%w: %w", ErrUnavailable, fault)
	}
	if needVault {
		if err := s.vault.WithUnlocked(snap, commitFn); err != nil {
			return fmt.Errorf("commit sealed team tx: %w", err)
		}
		s.notifyTeamAppended(teamID, seq)
		return nil
	}
	if err := commitFn(); err != nil {
		return fmt.Errorf("commit team tx: %w", err)
	}
	s.notifyTeamAppended(teamID, seq)
	return nil
}

func insertAppendedTeamEvent(
	ctx context.Context,
	tx *sql.Tx,
	teamID string,
	seq int64,
	ev *AppendTeamEventInput,
	fromState, toState string,
	envelope []byte,
	now int64,
) error {
	var envParam any
	if len(envelope) > 0 {
		envParam = envelope
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO team_task_events (
			team_id, sequence, event_id, task_id, event_type, outcome,
			from_state, to_state, failure_code, payload_envelope, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		teamID, seq, ev.EventID, ev.TaskID, string(ev.EventType), ev.Outcome,
		fromState, toState, ev.FailureCode, envParam, now,
	)
	if err != nil {
		return fmt.Errorf("insert team_task_event: %w", err)
	}
	return nil
}
