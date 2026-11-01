package crypto

import (
	"bytes"
	"crypto/rand"
	"io"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const cheapRecoveryKey = "recovery-secret"

// cheapKeyring builds a keyring like NewKeyring does but at the lowest accepted
// KDF cost, so tests that only need a working keyring stay fast: the suite runs
// under the race detector and once per mutant.
func cheapKeyring(t *testing.T) *Keyring {
	t.Helper()
	random := newStream()
	dek := make([]byte, dekLength)
	if _, err := io.ReadFull(random, dek); err != nil {
		t.Fatal(err)
	}
	wrap := func(associatedData AssociatedData, secret string) (KDFParams, Envelope) {
		params, err := newKDFParams(random)
		if err != nil {
			t.Fatal(err)
		}
		params.Memory, params.Iterations, params.Parallelism = kdfMinMemoryKiB, kdfMinIterations, kdfMinParallelism
		key, err := DeriveKey(secret, params)
		if err != nil {
			t.Fatal(err)
		}
		wrapped, err := sealV2(random, associatedData, key, dek)
		if err != nil {
			t.Fatal(err)
		}
		return params, wrapped
	}
	keyring := &Keyring{Version: keyringVersion}
	keyring.MasterKDF, keyring.MasterWrapped = wrap(keyringMasterAssociatedData(), primaryPhrase)
	keyring.RecoveryKDF, keyring.RecoveryWrapped = wrap(keyringRecoveryAssociatedData(), cheapRecoveryKey)
	return keyring
}

func TestExportedEntryPointsBuildChangeAndUnlockAKeyring(t *testing.T) {
	t.Parallel()
	keyring, recovery, err := NewKeyring(primaryPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if err = keyring.ChangePassword(primaryPhrase, replacementPhrase); err != nil {
		t.Fatal(err)
	}
	byPassword, err := keyring.UnlockWithPassword(replacementPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if len(byPassword) != dekLength || recovery == "" {
		t.Error("expected a 32-byte data key and a recovery key")
	}
}

func TestKeyringRejectsAWrongSecret(t *testing.T) {
	t.Parallel()
	keyring := cheapKeyring(t)
	tests := map[string]func() error{
		"wrong password":               func() error { _, err := keyring.UnlockWithPassword("wrong"); return err },
		"wrong recovery key":           func() error { _, err := keyring.UnlockWithRecovery("wrong"); return err },
		"recovery key as the password": func() error { _, err := keyring.UnlockWithPassword(cheapRecoveryKey); return err },
		"password as the recovery key": func() error { _, err := keyring.UnlockWithRecovery(primaryPhrase); return err },
	}
	for name, unlock := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			wantCode(t, unlock(), errcode.PermissionDenied)
		})
	}
}

func TestUnlockRequiresASecret(t *testing.T) {
	t.Parallel()
	keyring := cheapKeyring(t)
	_, err := keyring.UnlockWithPassword("")
	wantCode(t, err, errcode.InvalidArgument)
	_, err = keyring.UnlockWithRecovery("")
	wantCode(t, err, errcode.InvalidArgument)
}

func TestKeyringRejectsATamperedWrap(t *testing.T) {
	t.Parallel()
	keyring := cheapKeyring(t)
	keyring.MasterWrapped.Ciphertext[0] ^= 0xFF
	_, err := keyring.UnlockWithPassword(primaryPhrase)
	wantCode(t, err, errcode.PermissionDenied)
}

func TestKeyringRejectsTamperedKDFParameters(t *testing.T) {
	t.Parallel()
	keyring := cheapKeyring(t)
	keyring.MasterKDF.Memory = 1 << 30
	_, err := keyring.UnlockWithPassword(primaryPhrase)
	wantCode(t, err, errcode.InvalidArgument)
}

func TestKeyringRequiresAMasterPassword(t *testing.T) {
	t.Parallel()
	_, _, err := NewKeyring("")
	wantCode(t, err, errcode.InvalidArgument)
}

func TestRecoveryKeysAreFreshAndURLSafe(t *testing.T) {
	t.Parallel()
	first, err := newRecoveryKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newRecoveryKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if first == second || len(first) != 32 {
		t.Errorf("recovery keys must differ and be 32 characters long, got lengths %d and %d", len(first), len(second))
	}
}

func TestNewKeyringFailsWhenTheRandomSourceFailsAtAnyStage(t *testing.T) {
	t.Parallel()
	// Bytes read before each stage: data key 32, then salt 16, nonce 24, recovery
	// key 24, salt 16, nonce 24. A source that dies one byte short fails the data
	// key, the first wrap's salt and nonce, the recovery key and the second wrap's
	// salt. The second nonce shares the first one's error path through wrapDEK.
	for _, limit := range []int64{31, 47, 71, 95, 111} {
		_, _, err := newKeyring(io.LimitReader(newStream(), limit), primaryPhrase)
		wantCode(t, err, errcode.Internal)
	}
}

func TestWrapDEKRejectsAnEmptySecret(t *testing.T) {
	t.Parallel()
	_, _, err := wrapDEK(newStream(), keyringMasterAssociatedData(), "", make([]byte, dekLength))
	wantCode(t, err, errcode.InvalidArgument)
}

func TestChangePasswordKeepsTheDataKeyAndRetiresTheOldPassword(t *testing.T) {
	t.Parallel()
	keyring := cheapKeyring(t)
	before, err := keyring.UnlockWithPassword(primaryPhrase)
	if err != nil {
		t.Fatal(err)
	}
	oldSalt := bytes.Clone(keyring.MasterKDF.Salt)

	if err = keyring.changePassword(newStream(), primaryPhrase, replacementPhrase); err != nil {
		t.Fatal(err)
	}
	_, err = keyring.UnlockWithPassword(primaryPhrase)
	wantCode(t, err, errcode.PermissionDenied)
	after, err := keyring.UnlockWithPassword(replacementPhrase)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Error("the data key changed with the password")
	}
	if bytes.Equal(oldSalt, keyring.MasterKDF.Salt) {
		t.Error("the salt was not renewed")
	}
	if _, err := keyring.UnlockWithRecovery(cheapRecoveryKey); err != nil {
		t.Errorf("recovery key stopped working: %v", err)
	}
}

func TestChangePasswordFailsWithoutChangingTheKeyring(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		random   io.Reader
		old, new string
		want     errcode.Code
	}{
		"wrong old password":       {newStream(), "wrong", replacementPhrase, errcode.PermissionDenied},
		"empty new password":       {newStream(), primaryPhrase, "", errcode.InvalidArgument},
		"random fails at the salt": {io.LimitReader(newStream(), 3), primaryPhrase, replacementPhrase, errcode.Internal},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			keyring := cheapKeyring(t)
			wantCode(t, keyring.changePassword(tc.random, tc.old, tc.new), tc.want)
			if _, err := keyring.UnlockWithPassword(primaryPhrase); err != nil {
				t.Errorf("the original password stopped working: %v", err)
			}
		})
	}
}
