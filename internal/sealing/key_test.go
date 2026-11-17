package sealing

import (
	"bytes"
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/platform/ids"

	"github.com/syurodev/fugue/internal/testutil/cheapvault"
	"github.com/syurodev/fugue/internal/vault"
)

func TestSessionKeyOperationsRejectLockedVault(t *testing.T) {
	t.Parallel()
	if _, err := UnwrapSessionKey(nil, "exec.session_key", "s", "v1", []byte("wrapped")); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("unwrap: %v", err)
	}
	if _, _, err := GenerateSessionKey(nil, nil, "exec.session_key", "s", "v1"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("generate: %v", err)
	}
}

func TestSessionKeyWrapRoundTripAndBinding(t *testing.T) {
	t.Parallel()
	v := cheapvault.Unlocked(t)
	raw, wrapped, err := GenerateSessionKey(v, ids.CryptoRandom{}, "exec.session_key", "s", "v1")
	if err != nil {
		t.Fatal(err)
	}
	defer clear(raw)
	got, err := UnwrapSessionKey(v, "exec.session_key", "s", "v1", wrapped.WrappedKey)
	if err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("unwrap: %v", err)
	}
	clear(got)
	if _, err := UnwrapSessionKey(v, "exec.session_key", "other", "v1", wrapped.WrappedKey); err == nil {
		t.Fatal("wrong binding accepted")
	}
}
