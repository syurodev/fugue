package crypto

import (
	"testing"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func validEnvelope() *Envelope {
	return &Envelope{
		Version:    EnvelopeVersionV2,
		Algorithm:  AlgorithmXChaCha20Poly1305,
		KeyID:      "key-1",
		Nonce:      make([]byte, chacha20poly1305.NonceSizeX),
		Ciphertext: make([]byte, chacha20poly1305.Overhead),
	}
}

func TestValidateAcceptsASupportedEnvelope(t *testing.T) {
	t.Parallel()
	if err := validEnvelope().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsWhatThisPackageCouldNotHaveProduced(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Envelope){
		"version 1":                func(e *Envelope) { e.Version = 1 },
		"version 3":                func(e *Envelope) { e.Version = 3 },
		"version 0":                func(e *Envelope) { e.Version = 0 },
		"aes-256-gcm":              func(e *Envelope) { e.Algorithm = "aes-256-gcm" },
		"no algorithm":             func(e *Envelope) { e.Algorithm = "" },
		"no key ID":                func(e *Envelope) { e.KeyID = "" },
		"short nonce":              func(e *Envelope) { e.Nonce = make([]byte, chacha20poly1305.NonceSizeX-1) },
		"long nonce":               func(e *Envelope) { e.Nonce = make([]byte, chacha20poly1305.NonceSizeX+1) },
		"no nonce":                 func(e *Envelope) { e.Nonce = nil },
		"ciphertext below the tag": func(e *Envelope) { e.Ciphertext = make([]byte, chacha20poly1305.Overhead-1) },
		"no ciphertext":            func(e *Envelope) { e.Ciphertext = nil },
		"ciphertext above the cap": func(e *Envelope) { e.Ciphertext = make([]byte, MaxCiphertextBytes+1) },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			envelope := validEnvelope()
			mutate(envelope)
			wantCode(t, envelope.Validate(), errcode.InvalidArgument)
		})
	}
}

func TestValidateRejectsNoEnvelope(t *testing.T) {
	t.Parallel()
	var envelope *Envelope
	wantCode(t, envelope.Validate(), errcode.InvalidArgument)
	_, err := OpenWithAD(testKey(), envelope, testAssociatedData())
	wantCode(t, err, errcode.InvalidArgument)
}

func TestValidateAcceptsTheCiphertextBounds(t *testing.T) {
	t.Parallel()
	for _, size := range []int{chacha20poly1305.Overhead, MaxCiphertextBytes} {
		envelope := validEnvelope()
		envelope.Ciphertext = make([]byte, size)
		if err := envelope.Validate(); err != nil {
			t.Errorf("ciphertext of %d bytes: %v", size, err)
		}
	}
}
