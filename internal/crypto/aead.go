package crypto

import (
	"crypto/rand"
	"io"
	"strings"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// SealV2 encrypts plaintext under key, authenticating the record identity in
// associatedData. The record ID must be allocated before sealing.
func SealV2(associatedData AssociatedData, key, plaintext []byte) (Envelope, error) {
	return sealV2(rand.Reader, associatedData, key, plaintext)
}

// sealV2 takes the nonce source as a parameter so vector tests can fix it.
func sealV2(random io.Reader, associatedData AssociatedData, key, plaintext []byte) (Envelope, error) {
	if err := validateAssociatedData(associatedData); err != nil {
		return Envelope{}, err
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return Envelope{}, errcode.Wrap(errcode.InvalidArgument, "aead key is invalid", err)
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(random, nonce); err != nil {
		return Envelope{}, errcode.Wrap(errcode.Internal, "random source failed", err)
	}
	envelope := Envelope{
		Version:    EnvelopeVersionV2,
		Algorithm:  AlgorithmXChaCha20Poly1305,
		KeyID:      associatedData.KeyID,
		Nonce:      nonce,
		Ciphertext: aead.Seal(nil, nonce, plaintext, canonicalAssociatedData(associatedData)),
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// OpenWithAD verifies and decrypts an envelope against its record identity.
// A wrong key, a changed identity and a tampered envelope all fail the same
// way with errcode.PermissionDenied.
func OpenWithAD(key []byte, envelope *Envelope, associatedData AssociatedData) ([]byte, error) {
	if err := validateAssociatedData(associatedData); err != nil {
		return nil, err
	}
	if err := envelope.Validate(); err != nil {
		return nil, err
	}
	if associatedData.KeyID != envelope.KeyID {
		return nil, errcode.New(errcode.InvalidArgument, "associated data key ID does not match envelope")
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, errcode.Wrap(errcode.InvalidArgument, "aead key is invalid", err)
	}
	plaintext, err := aead.Open(nil, envelope.Nonce, envelope.Ciphertext, canonicalAssociatedData(associatedData))
	if err != nil {
		return nil, errcode.Wrap(errcode.PermissionDenied, "envelope failed authentication", err)
	}
	return plaintext, nil
}

func validateAssociatedData(associatedData AssociatedData) error {
	fields := []struct{ name, value string }{
		{"record type", associatedData.RecordType},
		{"record ID", associatedData.RecordID},
		{"key ID", associatedData.KeyID},
	}
	for _, f := range fields {
		if f.value == "" {
			return errcode.New(errcode.InvalidArgument, "associated data "+f.name+" is required")
		}
		// NUL is the field separator, so a NUL inside a field could make two
		// different identities canonicalize to the same bytes.
		if strings.ContainsRune(f.value, '\x00') {
			return errcode.New(errcode.InvalidArgument, "associated data "+f.name+" must not contain NUL")
		}
	}
	return nil
}

func canonicalAssociatedData(associatedData AssociatedData) []byte {
	return []byte(associatedData.RecordType + "\x00" + associatedData.RecordID + "\x00" + associatedData.KeyID)
}
