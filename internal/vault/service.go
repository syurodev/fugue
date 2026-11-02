package vault

import (
	"context"
	"sync"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

// State is the vault lifecycle state.
type State string

// The states. A new Service is StateLocked unless the store confirms there is no
// keyring.
const (
	StateNotConfigured State = "not_configured"
	StateLocked        State = "locked"
	StateUnlocked      State = "unlocked"
)

// MinMasterPasswordLength is the minimum length, in bytes, enforced at setup.
const MinMasterPasswordLength = 8

// ErrLocked is returned by every operation that needs the data key while the
// vault is not unlocked. Gated operations fail closed on it.
var ErrLocked = errcode.New(errcode.FailedPrecondition, "vault is locked")

// Service owns the lock state and holds the decrypted data key (DEK) in memory
// only while unlocked. A Master Password is mandatory: a new install starts
// not configured and must call Setup before anything can be unlocked.
//
// lifecycle serializes Setup, Unlock, UnlockWithRecovery, Lock and guard
// changes, so slow key derivation never holds mu and guards run in order. mu
// protects every field below it; SealRecord and OpenRecord hold it for reading
// while they use the key, so Lock cannot zero the key under them.
type Service struct {
	store       KeyringStore
	incarnation string

	lifecycle sync.Mutex

	mu          sync.RWMutex
	state       State
	revision    uint64
	dek         []byte
	nextID      uint64
	guards      map[uint64]func(Snapshot)
	subscribers map[uint64]*Subscription
}

// NewService returns a locked Service, or a not-configured one when the store
// confirms that no keyring exists. A store error leaves it locked: the state is
// unknown, so gated operations stay closed.
func NewService(ctx context.Context, store KeyringStore, generator ids.Generator) (*Service, error) {
	incarnation, err := generator.NewID()
	if err != nil {
		return nil, fail("vault incarnation", err)
	}
	state := StateLocked
	if exists, err := store.Exists(ctx); err == nil && !exists {
		state = StateNotConfigured
	}
	return &Service{
		store:       store,
		incarnation: incarnation,
		lifecycle:   sync.Mutex{},
		mu:          sync.RWMutex{},
		state:       state,
		revision:    0,
		dek:         nil,
		nextID:      0,
		guards:      map[uint64]func(Snapshot){},
		subscribers: map[uint64]*Subscription{},
	}, nil
}

// Setup creates the vault on first run and leaves it unlocked. It fails when a
// keyring already exists, so a Master Password is chosen exactly once. The
// returned recovery key must be shown to the user and is never stored.
func (s *Service) Setup(ctx context.Context, masterPassword redact.Secret) (redact.Secret, error) {
	if len(masterPassword.Reveal()) < MinMasterPasswordLength {
		return redact.Secret{}, errcode.New(errcode.InvalidArgument, "master password is too short")
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	exists, err := s.store.Exists(ctx)
	if err != nil {
		return redact.Secret{}, fail("check the keyring", err)
	}
	if exists {
		return redact.Secret{}, errcode.New(errcode.AlreadyExists, "vault is already configured")
	}
	keyring, recoveryKey, err := crypto.NewKeyring(masterPassword.Reveal())
	if err != nil {
		return redact.Secret{}, fail("create the keyring", err)
	}
	if err = s.store.Save(ctx, keyring); err != nil {
		return redact.Secret{}, fail("save the keyring", err)
	}
	dek, err := keyring.UnlockWithPassword(masterPassword.Reveal())
	if err != nil {
		return redact.Secret{}, fail("unlock the new keyring", err)
	}
	s.install(dek)
	return redact.NewSecret(recoveryKey), nil
}

// Unlock decrypts the data key with the Master Password.
func (s *Service) Unlock(ctx context.Context, masterPassword redact.Secret) error {
	return s.unlock(ctx, func(keyring *crypto.Keyring) ([]byte, error) {
		return keyring.UnlockWithPassword(masterPassword.Reveal())
	})
}

// UnlockWithRecovery decrypts the data key with the recovery key. It opens the
// same data key as the Master Password, so nothing is re-wrapped.
func (s *Service) UnlockWithRecovery(ctx context.Context, recoveryKey redact.Secret) error {
	return s.unlock(ctx, func(keyring *crypto.Keyring) ([]byte, error) {
		return keyring.UnlockWithRecovery(recoveryKey.Reveal())
	})
}

// unlock holds lifecycle, not mu, while the slow derivation runs: a concurrent
// Lock cannot complete in the middle and then be undone by the key installed
// afterwards, and sealing is not blocked meanwhile.
func (s *Service) unlock(ctx context.Context, open func(*crypto.Keyring) ([]byte, error)) error {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.Snapshot().State == StateNotConfigured {
		return errcode.New(errcode.FailedPrecondition, "vault is not configured")
	}
	keyring, err := s.store.Load(ctx)
	if err != nil {
		return fail("load the keyring", err)
	}
	dek, err := open(keyring)
	if err != nil {
		return fail("unlock", err)
	}
	s.install(dek)
	return nil
}

// install replaces the key and moves to unlocked. The subscribers and the key
// change together under mu, so an unlocked event never precedes a usable key.
func (s *Service) install(dek []byte) {
	s.mu.Lock()
	zero(s.dek)
	s.dek = dek
	guards, snapshot := s.transitionLocked(StateUnlocked)
	s.mu.Unlock()
	runGuards(guards, snapshot)
}

// Lock moves to locked and then zeroes the data key. The state changes first,
// under mu, which waits for sealing and opening in flight; from then on they
// fail with ErrLocked. Guards see the locked snapshot before the key is zeroed.
func (s *Service) Lock() {
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	s.mu.Lock()
	if s.state != StateUnlocked {
		s.mu.Unlock()
		return
	}
	guards, snapshot := s.transitionLocked(StateLocked)
	s.mu.Unlock()
	runGuards(guards, snapshot)
	s.mu.Lock()
	zero(s.dek)
	s.dek = nil
	s.mu.Unlock()
}

// fail annotates err with what was being done, keeping its code so callers can
// still tell a wrong password from an unavailable store.
func fail(what string, err error) error {
	return errcode.Wrap(errcode.CodeOf(err), what+" failed", err)
}

func zero(b []byte) {
	clear(b)
}
