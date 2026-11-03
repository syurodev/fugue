// Package codec provides the canonical JSON codec v1 and receipt deduplication
// logic for command payloads and execution records. It enforces strict canonical
// representation: valid UTF-8, detection of duplicate JSON keys at any depth,
// disallowing unknown fields, rejecting floating-point numbers, requiring integers
// within int64 bounds, explicit field order without omitempty, and byte-exact
// deduplication against existing receipts.
//
// Owner: storage security (docs/adr/0001-package-layout.md, G-03). Allowed
// callers: internal/execstore, internal/coordinator and internal/app.
package codec
