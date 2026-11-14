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

// ErrSequenceConflict reports a real expected-sequence or revision mismatch: a
// concurrent writer advanced the session, policy or item first.
var ErrSequenceConflict = errcode.New(errcode.FailedPrecondition, "vendor event sequence conflict")

// ErrSessionClosed reports a write to a session that is already closed; closed
// is terminal, so retrying cannot succeed.
var ErrSessionClosed = errcode.New(errcode.FailedPrecondition, "vendor session closed")

// ErrSessionNotFound reports an operation on a session that does not exist.
var ErrSessionNotFound = errcode.New(errcode.NotFound, "vendor session not found")

// ErrTurnNotOpen reports a turn end for a turn that never started or already ended.
var ErrTurnNotOpen = errcode.New(errcode.NotFound, "vendor turn not open")

// ErrItemNotFound reports an operation on a transcript item that does not exist.
var ErrItemNotFound = errcode.New(errcode.NotFound, "transcript item not found")

// Store owns persistence and cached session keys for vendor sessions.
type Store struct {
	db         *sql.DB
	vault      *vault.Service
	clock      clock.Clock
	cache      *sealing.KeyCache
	unregister func()
	prepare    sync.Mutex
	random     ids.Random
	// beforeCommit is a test seam set through export_test.go; production code
	// never sets it.
	beforeCommit error
	// changeRetention bounds the cross-session change journal (F9.9e);
	// ChangeJournalRetention unless a test shrank it.
	changeRetention int
}

// Option configures the store beyond its defaults.
type Option func(*Store)

// WithChangeJournalRetention overrides ChangeJournalRetention (tests shrink
// the journal; production always uses the default).
func WithChangeJournalRetention(n int) Option {
	return func(s *Store) { s.changeRetention = n }
}

// New verifies database durability and registers vault lock invalidation.
func New(ctx context.Context, db *sql.DB, v *vault.Service, clk clock.Clock, random ids.Random, opts ...Option) (*Store, error) {
	if db == nil || clk == nil {
		return nil, errcode.New(errcode.InvalidArgument, "database and clock required")
	}
	if err := storage.VerifyPragmas(ctx, db); err != nil {
		return nil, fmt.Errorf("%w: verify storage: %w", ErrUnavailable, err)
	}
	if random == nil {
		random = ids.CryptoRandom{}
	}
	s := &Store{
		db:              db,
		vault:           v,
		clock:           clk,
		cache:           sealing.NewKeyCache(),
		unregister:      nil,
		prepare:         sync.Mutex{},
		random:          random,
		beforeCommit:    nil,
		changeRetention: ChangeJournalRetention,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
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
