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

func newKeyring(random io.Reader, masterPassword string) (*Keyring, string, error) {
	if masterPassword == "" {
		return nil, "", errcode.New(errcode.InvalidArgument, "master password is required")
	}
	dek := make([]byte, dekLength)
	if _, err := io.ReadFull(random, dek); err != nil {
		return nil, "", errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	masterKDF, masterWrapped, err := wrapDEK(random, keyringMasterAssociatedData(), masterPassword, dek)
	if err != nil {
		return nil, "", err
	}
	recoveryKey, err := newRecoveryKey(random)
	if err != nil {
		return nil, "", err
	}
	recoveryKDF, recoveryWrapped, err := wrapDEK(random, keyringRecoveryAssociatedData(), recoveryKey, dek)
	if err != nil {
		return nil, "", err
	}
	return &Keyring{
		Version:         keyringVersion,
		MasterKDF:       masterKDF,
		MasterWrapped:   masterWrapped,
		RecoveryKDF:     recoveryKDF,
		RecoveryWrapped: recoveryWrapped,
	}, recoveryKey, nil
}

// UnlockWithPassword returns the plaintext DEK when masterPassword is correct.
// A wrong password fails with errcode.PermissionDenied.
func (k *Keyring) UnlockWithPassword(masterPassword string) ([]byte, error) {
	return unwrapDEK(k.MasterKDF, &k.MasterWrapped, keyringMasterAssociatedData(), masterPassword, "invalid master password")
}

// UnlockWithRecovery returns the plaintext DEK when recoveryKey is correct.
// A wrong key fails with errcode.PermissionDenied.
func (k *Keyring) UnlockWithRecovery(recoveryKey string) ([]byte, error) {
	return unwrapDEK(k.RecoveryKDF, &k.RecoveryWrapped, keyringRecoveryAssociatedData(), recoveryKey, "invalid recovery key")
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

// wrapDEK draws a salt, derives a key from secret and seals the DEK under it.
func wrapDEK(random io.Reader, associatedData AssociatedData, secret string, dek []byte) (KDFParams, Envelope, error) {
	params, err := newKDFParams(random)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	key, err := DeriveKey(secret, params)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	wrapped, err := sealV2(random, associatedData, key, dek)
	if err != nil {
		return KDFParams{}, Envelope{}, err
	}
	return params, wrapped, nil
}

func unwrapDEK(params KDFParams, wrapped *Envelope, associatedData AssociatedData, secret, failure string) ([]byte, error) {
	key, err := DeriveKey(secret, params)
	if err != nil {
		return nil, err
	}
	dek, err := OpenWithAD(key, wrapped, associatedData)
	if err != nil {
		// One message for every cause: the caller must not learn whether the
		// secret or the stored keyring was wrong.
		return nil, errcode.Wrap(errcode.PermissionDenied, failure, err)
	}
	return dek, nil
}

func newRecoveryKey(random io.Reader) (string, error) {
	raw := make([]byte, recoveryKeyLength)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
