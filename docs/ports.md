# Ports

Provenance of every package ported from `syurodev/dev-kit` (roadmap §3). One row per package,
one PR per package. The vector tests of a port are built from the original code's output; they
must not be regenerated from the ported code.

Source repository state: `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` (`feat(harness): enforce
exact and metadata closure contracts (#419)`).

## `internal/crypto` (F1.1)

| | |
| --- | --- |
| Original | `internal/crypto` (`aead.go`, `envelope.go`, `kdf.go`, `keyring.go` and their tests) |
| dev-kit commit | `b3c8ff5df42e6fc58c1cd8077dddd278fc7dd84e` |
| Wire and storage format | Unchanged: envelope v2 JSON, canonical associated data `type NUL id NUL key`, XChaCha20-Poly1305, Argon2id with the same bounds and default cost, keyring v1 JSON |
| Vector tests | `vectors_test.go` and `testdata/keyring.json`: envelopes, derived keys and a whole keyring produced by the original code from a fixed random stream and a fixed key. `TestSealMatchesTheOriginalVector`, `TestSealOfEmptyPlaintextMatchesTheOriginalVector`, `TestDeriveKeyMatchesTheOriginalVectors`, `TestNewKeyringMatchesTheOriginalVector`, `TestTheOriginalKeyringStillUnlocks` |

Changed, without changing what is accepted or produced:

- Errors are `errcode` errors with stable codes instead of ad hoc strings. A wrong password, a
  wrong recovery key, a wrong AEAD key, a changed record identity and a tampered envelope are
  `permission_denied`; malformed envelopes, associated data or KDF parameters are
  `invalid_argument`; a failing random source is `internal`.
- Randomness is read from an `io.Reader` parameter of unexported twins (`sealV2`,
  `newKDFParams`, `newKeyring`, `changePassword`); the exported functions pass `crypto/rand`.
  The original read `crypto/rand` directly, so its output could not be fixed in a test.
- `SealV2` returns a zero `Envelope` with the error when the ciphertext would exceed
  `MaxCiphertextBytes`; the original returned the envelope next to the error.
- Redundant checks were merged: the repeated version and algorithm checks in `OpenWithAD`,
  `open` and `Validate` are one `Validate`, and the empty-salt check is covered by the salt
  length bound. The same inputs are accepted and rejected.
- `OpenWithAD` and `Envelope.Validate` take a pointer (`gocritic` `hugeParam`: an `Envelope` is 88 bytes); a nil envelope is rejected as `invalid_argument`.
- `AlgorithmAES256GCM` is not carried over: the original defined it only to reject it.
- DevKit wording ("sync wire limit") is removed from comments.
