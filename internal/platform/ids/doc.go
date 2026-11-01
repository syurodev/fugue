// Package ids is the identifier and randomness port: a Generator of
// time-ordered identifiers, a Random source of bytes, real implementations on
// crypto/rand and deterministic ones for tests.
//
// Owner: ID port (docs/quality-gates.md section 3). It is the only place outside
// internal/crypto that reads crypto/rand. Allowed callers: every package that
// needs an identifier or random bytes, through an injected Generator or Random.
package ids
