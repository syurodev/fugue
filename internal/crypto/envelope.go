package crypto

import (
	"golang.org/x/crypto/chacha20poly1305"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// Algorithm and version names are persisted inside envelopes and never change.
const (
	AlgorithmXChaCha20Poly1305        = "xchacha20-poly1305"
	EnvelopeVersionV2          uint32 = 2
)

// MaxCiphertextBytes bounds one envelope's ciphertext (1 MiB) so an untrusted
// envelope cannot force an unbounded allocation during Open. Sealing a
// plaintext whose ciphertext would exceed it fails.
const MaxCiphertextBytes = 1 << 20

// AssociatedData is the immutable record identity authenticated by SealV2. It
// is not stored in the envelope: the owning record supplies it when opening.
type AssociatedData struct {
	RecordType string
	RecordID   string
	KeyID      string
}

// Envelope is the persisted form of a sealed value.
type Envelope struct {
	Version    uint32 `json:"version"`
	Algorithm  string `json:"algorithm"`
	KeyID      string `json:"key_id"`
	Nonce      []byte `json:"nonce"`
	Ciphertext []byte `json:"ciphertext"`
}

// Validate rejects an envelope this package could not have produced. It runs
// before any decryption because XChaCha20-Poly1305 panics on a nonce of the
// wrong length, and an envelope read from storage is untrusted.
func (e *Envelope) Validate() error {
	// Plain ifs rather than a tagless switch: the coverage profile does not
	// cover the case expressions of a switch, which hides them from mutation testing.
	if e == nil {
		return errcode.New(errcode.InvalidArgument, "envelope is required")
	}
	if e.Version != EnvelopeVersionV2 {
		return errcode.New(errcode.InvalidArgument, "envelope version is unsupported")
	}
	if e.Algorithm != AlgorithmXChaCha20Poly1305 {
		return errcode.New(errcode.InvalidArgument, "envelope algorithm is unsupported")
	}
	if e.KeyID == "" {
		return errcode.New(errcode.InvalidArgument, "envelope key ID is required")
	}
	if len(e.Nonce) != chacha20poly1305.NonceSizeX {
		return errcode.New(errcode.InvalidArgument, "envelope nonce has the wrong length")
	}
	// Even an empty plaintext seals to the authentication tag.
	if len(e.Ciphertext) < chacha20poly1305.Overhead {
		return errcode.New(errcode.InvalidArgument, "envelope ciphertext is shorter than its tag")
	}
	if len(e.Ciphertext) > MaxCiphertextBytes {
		return errcode.New(errcode.InvalidArgument, "envelope ciphertext is too large")
	}
	return nil
}
