package crypto

import (
	"crypto/rand"
	"encoding/base64"
	"io"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	keyringVersion        = 1
	keyringDEKKeyID       = "dek"
	keyringRecordType     = "keyring"
	keyringMasterWrapID   = "master"
	keyringRecoveryWrapID = "recovery"
	dekLength             = 32
	recoveryKeyLength     = 24
)

func keyringMasterAssociatedData() AssociatedData {
	return AssociatedData{RecordType: keyringRecordType, RecordID: keyringMasterWrapID, KeyID: keyringDEKKeyID}
}

func keyringRecoveryAssociatedData() AssociatedData {
	return AssociatedData{RecordType: keyringRecordType, RecordID: keyringRecoveryWrapID, KeyID: keyringDEKKeyID}
}

// Keyring is the persisted key material of a vault. It never holds the
// plaintext data key (DEK): the DEK is wrapped once by a key derived from the
// Master Password and once by one derived from the recovery key.
type Keyring struct {
	Version         int       `json:"version"`
	MasterKDF       KDFParams `json:"master_kdf"`
	MasterWrapped   Envelope  `json:"master_wrapped"`
	RecoveryKDF     KDFParams `json:"recovery_kdf"`
	RecoveryWrapped Envelope  `json:"recovery_wrapped"`
}

// NewKeyring generates a random DEK and wraps it under masterPassword and under
// a freshly generated recovery key. The recovery key is returned once and is
// never stored.
func NewKeyring(masterPassword string) (*Keyring, string, error) {
	return newKeyring(rand.Reader, masterPassword)
}

// NewKeyringBytes generates a random DEK and wraps it under masterPassword bytes
// and under a freshly generated recovery key.
func NewKeyringBytes(masterPassword []byte) (*Keyring, []byte, error) {
	return newKeyringBytes(rand.Reader, masterPassword)
}

func newKeyring(random io.Reader, masterPassword string) (*Keyring, string, error) {
	buf := []byte(masterPassword)
	defer clear(buf)
	kr, rec, err := newKeyringBytes(random, buf)
	if err != nil {
		return nil, "", err
	}
	defer clear(rec)
	return kr, string(rec), nil
}

func newKeyringBytes(random io.Reader, masterPassword []byte) (*Keyring, []byte, error) {
	if len(masterPassword) == 0 {
		return nil, nil, errcode.New(errcode.InvalidArgument, "master password is required")
	}
	dek := make([]byte, dekLength)
	defer clear(dek)
	if _, err := io.ReadFull(random, dek); err != nil {
		return nil, nil, errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	masterKDF, masterWrapped, err := wrapDEKBytes(random, keyringMasterAssociatedData(), masterPassword, dek)
	if err != nil {
		return nil, nil, err
	}
	recoveryKey, err := newRecoveryKeyBytes(random)
	if err != nil {
		return nil, nil, err
	}
	recoveryKDF, recoveryWrapped, err := wrapDEKBytes(random, keyringRecoveryAssociatedData(), recoveryKey, dek)
	if err != nil {
		clear(recoveryKey)
		return nil, nil, err
	}
	return &Keyring{
		Version: keyringVersion, MasterKDF: masterKDF, MasterWrapped: masterWrapped,
		RecoveryKDF: recoveryKDF, RecoveryWrapped: recoveryWrapped,
	}, recoveryKey, nil
}

// UnlockWithPassword returns the plaintext DEK when masterPassword is correct.
func (k *Keyring) UnlockWithPassword(masterPassword string) ([]byte, error) {
	buf := []byte(masterPassword)
	defer clear(buf)
	return k.UnlockWithPasswordBytes(buf)
}

// UnlockWithPasswordBytes returns the plaintext DEK when masterPassword bytes are correct.
func (k *Keyring) UnlockWithPasswordBytes(masterPassword []byte) ([]byte, error) {
	return unwrapDEKBytes(k.MasterKDF, &k.MasterWrapped, keyringMasterAssociatedData(), masterPassword, "invalid master password")
}

// UnlockWithRecovery returns the plaintext DEK when recoveryKey is correct.
func (k *Keyring) UnlockWithRecovery(recoveryKey string) ([]byte, error) {
	buf := []byte(recoveryKey)
	defer clear(buf)
	return k.UnlockWithRecoveryBytes(buf)
}

// UnlockWithRecoveryBytes returns the plaintext DEK when recoveryKey bytes are correct.
func (k *Keyring) UnlockWithRecoveryBytes(recoveryKey []byte) ([]byte, error) {
	return unwrapDEKBytes(k.RecoveryKDF, &k.RecoveryWrapped, keyringRecoveryAssociatedData(), recoveryKey, "invalid recovery key")
}

// ChangePassword rewraps the existing DEK under a new master password, with a
// new salt. The DEK is unchanged, so existing records stay readable.
func (k *Keyring) ChangePassword(oldPassword, newPassword string) error {
	return k.changePassword(rand.Reader, oldPassword, newPassword)
}

func (k *Keyring) changePassword(random io.Reader, oldPassword, newPassword string) error {
	if newPassword == "" {
		return errcode.New(errcode.InvalidArgument, "new master password is required")
	}
	dek, err := k.UnlockWithPassword(oldPassword)
	if err != nil {
		return err
	}
	kdf, wrapped, err := wrapDEK(random, keyringMasterAssociatedData(), newPassword, dek)
	if err != nil {
		return err
	}
	k.MasterKDF = kdf
	k.MasterWrapped = wrapped
	return nil
}

// wrapDEKBytes draws a salt, derives a key from secret bytes and seals the DEK under it.
func wrapDEKBytes(random io.Reader, associatedData AssociatedData, secret, dek []byte) (KDFParams, Envelope, error) {
	params, err := newKDFParams(random)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	key, err := DeriveKeyBytes(secret, params)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	defer clear(key)
	wrapped, err := sealV2(random, associatedData, key, dek)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	return params, wrapped, nil
}

// wrapDEK draws a salt, derives a key from secret and seals the DEK under it.
func wrapDEK(random io.Reader, associatedData AssociatedData, secret string, dek []byte) (KDFParams, Envelope, error) {
	buf := []byte(secret)
	defer clear(buf)
	return wrapDEKBytes(random, associatedData, buf, dek)
}

func unwrapDEKBytes(params KDFParams, wrapped *Envelope, associatedData AssociatedData, secret []byte, failure string) ([]byte, error) {
	key, err := DeriveKeyBytes(secret, params)
	if err != nil {
		return nil, err
	}
	defer clear(key)
	dek, err := OpenWithAD(key, wrapped, associatedData)
	if err != nil {
		// One message for every cause: the caller must not learn whether the
		// secret or the stored keyring was wrong.
		return nil, errcode.Wrap(errcode.PermissionDenied, failure, err)
	}
	return dek, nil
}

func newRecoveryKeyBytes(random io.Reader) ([]byte, error) {
	raw := make([]byte, recoveryKeyLength)
	defer clear(raw)
	if _, err := io.ReadFull(random, raw); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	out := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(out, raw)
	return out, nil
}
