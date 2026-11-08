// Package vendorstore persists vendor sessions, their contiguous event journals,
// turns, transcript versions and protected payloads. Owner: vendor runtime and
// storage (ADR 0002). Allowed callers: internal/app through composition ports.
//
// CreateSession commits VendorSessionStarted at sequence 1. Append atomically
// checks the expected last sequence and commits the next event, protected
// payloads and projections. The vendor runtime owns state transitions; this
// package validates enums and persistence invariants only. Snapshot reads the
// session and last sequence in one SQLite statement/read transaction.
//
// Content and raw vendor events are sealed under session keys; metadata uses
// neutral allowlisted records. Vault epochs authorize protected commits, while
// metadata-only waiting states remain writable when the vault is locked.
package vendorstore
