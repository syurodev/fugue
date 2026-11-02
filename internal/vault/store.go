package vault

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

// KeyringStore persists the vault keyring: wrapped data key and KDF parameters,
// never the plaintext key or the Master Password.
type KeyringStore interface {
	Exists(ctx context.Context) (bool, error)
	// Load returns errcode.NotFound when no keyring exists.
	Load(ctx context.Context) (*crypto.Keyring, error)
	Save(ctx context.Context, keyring *crypto.Keyring) error
}

// MemoryKeyringStore keeps the keyring in memory. It is the store for tests and
// ephemeral runs and does not survive a restart.
type MemoryKeyringStore struct {
	mu      sync.RWMutex
	keyring *crypto.Keyring
}

// NewMemoryKeyringStore returns an empty store, or one holding keyring when it
// is not nil.
func NewMemoryKeyringStore(keyring *crypto.Keyring) *MemoryKeyringStore {
	return &MemoryKeyringStore{mu: sync.RWMutex{}, keyring: keyring}
}

// Exists reports whether a keyring has been saved.
func (s *MemoryKeyringStore) Exists(context.Context) (bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.keyring != nil, nil
}

// Load returns the saved keyring.
func (s *MemoryKeyringStore) Load(context.Context) (*crypto.Keyring, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.keyring == nil {
		return nil, errcode.New(errcode.NotFound, "keyring not found")
	}
	return s.keyring, nil
}

// Save replaces the saved keyring.
func (s *MemoryKeyringStore) Save(_ context.Context, keyring *crypto.Keyring) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keyring = keyring
	return nil
}
