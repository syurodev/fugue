package execstore

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/vault"
)

type failingSealingID struct{}

func (failingSealingID) NewID() (string, error) { return "", errEntropy }

func TestSealPayloadFailsClosedOnPreparationErrors(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	s.ids = failingSealingID{}
	if _, _, err := s.SealPayload(t.Context(), "session", "", "message", []byte("data")); !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, errEntropy) {
		t.Fatalf("ID error: %v", err)
	}
	s.random = failingRandom{}
	if _, _, err := s.SealPayload(t.Context(), "session", "id", "message", []byte("data")); !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, errEntropy) {
		t.Fatalf("key entropy error: %v", err)
	}
	s.vault = nil
	if _, _, err := s.SealPayload(t.Context(), "session", "id", "message", []byte("data")); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("locked vault: %v", err)
	}
}

func TestSealPayloadRejectsNonceEntropyFailure(t *testing.T) {
	t.Parallel()
	s, _, _, _ := newVaultAndStore(t)
	s.randomReader = randomReader{r: failingRandom{}}
	if _, _, err := s.SealPayload(t.Context(), "session", "id", "message", []byte("data")); !errors.Is(err, ErrStoreUnavailable) || !errors.Is(err, errEntropy) {
		t.Fatalf("nonce entropy error: %v", err)
	}
}

func TestPurgeBarrierRejectsKeyInstallation(t *testing.T) {
	t.Parallel()
	s := &Store{keyCache: newSessionKeyCache()}
	s.keyCache.BeginPurge("session")
	if err := s.installKey("session", []byte("key"), nil); !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("purging key: %v", err)
	}
}
