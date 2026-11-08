package vendorstore

import (
	"context"
	"database/sql"
	"fmt"
	"sync"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

// ErrSequenceConflict indicates that a concurrent append advanced the session.
var ErrSequenceConflict = errcode.New(errcode.FailedPrecondition, "vendor event sequence conflict")

// Store owns persistence and cached session keys for vendor sessions.
type Store struct {
	db           *sql.DB
	vault        *vault.Service
	clock        clock.Clock
	cache        *sealing.KeyCache
	unregister   func()
	prepare      sync.Mutex
	random       ids.Random
	beforeCommit error
}

// New verifies database durability and registers vault lock invalidation.
func New(ctx context.Context, db *sql.DB, v *vault.Service, clk clock.Clock, random ids.Random) (*Store, error) {
	if db == nil || clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "database and clock required")
	}
	if operationErr1 := storage.VerifyPragmas(ctx, db); operationErr1 != nil {
		return nil, fmt.Errorf("%w: verify storage: %w", ErrUnavailable, operationErr1)
	}
	if random == nil {
		random = ids.CryptoRandom{}
	}
	s := &Store{db: db, vault: v, clock: clk, cache: sealing.NewKeyCache(), unregister: nil, prepare: sync.Mutex{}, random: random, beforeCommit: nil}
	if v != nil {
		s.unregister = v.RegisterLockGuard(func(snap vault.Snapshot) {
			if snap.State != vault.StateUnlocked {
				s.cache.ClearAndZero()
			}
		})
	}
	return s, nil
}

// Close revokes cached keys and detaches the vault guard.
func (s *Store) Close() error {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	if s.unregister != nil {
		s.unregister()
		s.unregister = nil
	}
	s.cache.ClearAndZero()
	return nil
}

// SetCommitFault injects a pre-commit storage failure for atomicity testing.
func (s *Store) SetCommitFault(err error) {
	s.prepare.Lock()
	defer s.prepare.Unlock()
	s.beforeCommit = err
}
