// Package crypto is the repository's only cryptography: XChaCha20-Poly1305
// sealed envelopes bound to a record identity, Argon2id key derivation, and the
// keyring that wraps the data key under a Master Password and a recovery key.
//
// Owner: storage security (docs/adr/0001-package-layout.md, G-03). Allowed
// callers: internal/vault, internal/execstore and internal/app. Key material
// crosses this package as byte slices that callers must zero when done; nothing
// here logs, and no error message contains key material or plaintext.
package crypto
