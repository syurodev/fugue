package vault

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

var errStore = errors.New("store is down")

func TestEveryProcessStartIsLocked(t *testing.T) {
	t.Parallel()
	store := NewMemoryKeyringStore(cheapKeyring(t))
	first, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	// A second Service over the same keyring is a new process start.
	second, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if got := second.Snapshot().State; got != StateLocked {
		t.Errorf("state after a restart = %q, want %q", got, StateLocked)
	}
	_, err = second.SealRecord(testBinding(), []byte("x"))
	if !errors.Is(err, ErrLocked) {
		t.Errorf("sealing while locked: %v, want ErrLocked", err)
	}
}

func TestANewInstallIsNotConfigured(t *testing.T) {
	t.Parallel()
	s, err := NewService(t.Context(), NewMemoryKeyringStore(nil), ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().State; got != StateNotConfigured {
		t.Errorf("state = %q, want %q", got, StateNotConfigured)
	}
	wantCode(t, s.Unlock(t.Context(), secret(cheapPassword)), errcode.FailedPrecondition)
	_, err = s.SealRecord(testBinding(), []byte("x"))
	if !errors.Is(err, ErrLocked) {
		t.Errorf("sealing while not configured: %v, want ErrLocked", err)
	}
}

func TestAnUnknownStoreStateFailsClosed(t *testing.T) {
	t.Parallel()
	store := &flakyStore{inner: NewMemoryKeyringStore(nil), existsErr: errStore}
	s, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().State; got != StateLocked {
		t.Errorf("state = %q, want %q", got, StateLocked)
	}
}

func TestNewServiceReportsAFailingIDGenerator(t *testing.T) {
	t.Parallel()
	_, err := NewService(t.Context(), NewMemoryKeyringStore(nil), failingIDs{})
	wantCode(t, err, errcode.Internal)
}

type failingIDs struct{}

func (failingIDs) NewID() (string, error) { return "", errStore }

func TestSetupCreatesTheKeyringAndUnlocks(t *testing.T) {
	t.Parallel()
	store := NewMemoryKeyringStore(nil)
	s, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	_, sub := s.Observe()
	defer sub.Close()
	// Exactly the minimum length is accepted.
	recovery, err := s.Setup(t.Context(), secret(strings.Repeat("x", MinMasterPasswordLength)))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.IsZero() {
		t.Fatal("no recovery key returned")
	}
	if got := s.Snapshot(); got.State != StateUnlocked || got.Revision != 1 {
		t.Errorf("snapshot = %+v, want unlocked at revision 1", got)
	}
	if got := recv(t, sub); got.State != StateUnlocked || got.Revision != 1 {
		t.Errorf("Setup event = %+v, want unlocked at revision 1", got)
	}
	sealed, err := s.SealRecord(testBinding(), []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	requireRecoverable(t, store, recovery, sealed)
}

// requireRecoverable restarts over store and checks that the recovery key opens
// the same data key, so a record sealed before the restart still opens.
func requireRecoverable(t *testing.T, store KeyringStore, recovery redact.Secret, sealed []byte) {
	t.Helper()
	restarted, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	if err = restarted.UnlockWithRecovery(t.Context(), recovery); err != nil {
		t.Fatal(err)
	}
	if exists, existsErr := store.Exists(t.Context()); existsErr != nil || !exists {
		t.Errorf("the keyring was not saved (exists=%v, err=%v)", exists, existsErr)
	}
	opened, err := restarted.OpenRecord(testBinding(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, []byte("body")) {
		t.Error("the recovery key did not open the same data key")
	}
}

func TestSetupRefusesWhatItMust(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		store    KeyringStore
		password string
		want     errcode.Code
	}{
		"password too short":  {NewMemoryKeyringStore(nil), strings.Repeat("x", MinMasterPasswordLength-1), errcode.InvalidArgument},
		"already configured":  {NewMemoryKeyringStore(cheapKeyring(t)), longPassword, errcode.AlreadyExists},
		"store cannot answer": {&flakyStore{inner: NewMemoryKeyringStore(nil), existsErr: errStore}, longPassword, errcode.Internal},
		"store cannot save":   {&flakyStore{inner: NewMemoryKeyringStore(nil), saveErr: errStore}, longPassword, errcode.Internal},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s, err := NewService(t.Context(), tc.store, ids.NewSequence("inc-"))
			if err != nil {
				t.Fatal(err)
			}
			before := s.Snapshot()
			_, err = s.Setup(t.Context(), secret(tc.password))
			wantCode(t, err, tc.want)
			if after := s.Snapshot(); after.State == StateUnlocked || after.Revision != before.Revision {
				t.Errorf("a refused Setup moved the vault: %+v -> %+v", before, after)
			}
		})
	}
}

func TestUnlockOpensTheKeyringsDataKey(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Service, context.Context) error{
		"password":     func(s *Service, c context.Context) error { return s.Unlock(c, secret(cheapPassword)) },
		"recovery key": func(s *Service, c context.Context) error { return s.UnlockWithRecovery(c, secret(cheapRecovery)) },
	}
	for name, unlock := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newService(t)
			if err := unlock(s, t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := s.Snapshot(); got.State != StateUnlocked || got.Revision != 1 {
				t.Errorf("snapshot = %+v, want unlocked at revision 1", got)
			}
			if hex.EncodeToString(s.dek) != cheapDEK {
				t.Error("unlocked with a different data key than the keyring wraps")
			}
		})
	}
}

func TestAWrongSecretLeavesTheVaultLocked(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Service, context.Context) error{
		"wrong password":           func(s *Service, c context.Context) error { return s.Unlock(c, secret("wrong")) },
		"wrong recovery key":       func(s *Service, c context.Context) error { return s.UnlockWithRecovery(c, secret("wrong")) },
		"recovery key as password": func(s *Service, c context.Context) error { return s.Unlock(c, secret(cheapRecovery)) },
	}
	for name, unlock := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			s := newService(t)
			wantCode(t, unlock(s, t.Context()), errcode.PermissionDenied)
			if got := s.Snapshot(); got.State != StateLocked || got.Revision != 0 {
				t.Errorf("a failed unlock moved the vault: %+v", got)
			}
		})
	}
}

func TestUnlockReportsAFailingStore(t *testing.T) {
	t.Parallel()
	store := &flakyStore{inner: NewMemoryKeyringStore(cheapKeyring(t)), loadErr: errStore}
	s, err := NewService(t.Context(), store, ids.NewSequence("inc-"))
	if err != nil {
		t.Fatal(err)
	}
	wantCode(t, s.Unlock(t.Context(), secret(cheapPassword)), errcode.Internal)
	wantCode(t, s.UnlockWithRecovery(t.Context(), secret(cheapRecovery)), errcode.Internal)
}

func TestUnlockingAnUnlockedVaultKeepsTheRevision(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	if err := s.Unlock(t.Context(), secret(cheapPassword)); err != nil {
		t.Fatal(err)
	}
	if got := s.Snapshot().Revision; got != 1 {
		t.Errorf("revision = %d, want 1", got)
	}
	wantCode(t, s.Unlock(t.Context(), secret("wrong")), errcode.PermissionDenied)
	if got := s.Snapshot().State; got != StateUnlocked {
		t.Errorf("a wrong password locked an unlocked vault: %q", got)
	}
}

func TestLockZeroesTheKeyAndClosesTheVault(t *testing.T) {
	t.Parallel()
	s := unlocked(t)
	sealed, err := s.SealRecord(testBinding(), []byte("body"))
	if err != nil {
		t.Fatal(err)
	}
	key := s.dek
	s.Lock()
	if got := s.Snapshot(); got.State != StateLocked || got.Revision != 2 {
		t.Errorf("snapshot = %+v, want locked at revision 2", got)
	}
	if !bytes.Equal(key, make([]byte, len(key))) || s.dek != nil {
		t.Error("the data key was not zeroed and dropped")
	}
	_, err = s.OpenRecord(testBinding(), sealed)
	if !errors.Is(err, ErrLocked) {
		t.Errorf("opening while locked: %v, want ErrLocked", err)
	}
	s.Lock() // locking twice is harmless
	if got := s.Snapshot().Revision; got != 2 {
		t.Errorf("a second Lock changed the revision to %d", got)
	}
}

func TestLockOnAVaultThatWasNeverUnlockedChangesNothing(t *testing.T) {
	t.Parallel()
	s := newService(t)
	s.Lock()
	if got := s.Snapshot(); got.State != StateLocked || got.Revision != 0 {
		t.Errorf("snapshot = %+v", got)
	}
}

func TestEachServiceHasItsOwnIncarnation(t *testing.T) {
	t.Parallel()
	generator := ids.NewSequence("inc-")
	store := NewMemoryKeyringStore(nil)
	first, err := NewService(t.Context(), store, generator)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewService(t.Context(), store, generator)
	if err != nil {
		t.Fatal(err)
	}
	if first.Snapshot().Incarnation == "" || first.Snapshot().Incarnation == second.Snapshot().Incarnation {
		t.Error("incarnations must be set and differ between services")
	}
}
