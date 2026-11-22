// Package secretstore owns the sealed shared secret store (secret_records)
// and its metadata-only change journal (secret_events) for fugue.secrets.v1
// (host-api §5.5) and content-safety KnownValues (Q-34). Unsealed metadata is
// readable while the vault is locked (INV-09); secret values are sealed via
// vault.SealRecord and never returned over the Host API.
//
// Owner: secret store (F10.15a). Allowed callers: internal/app, which composes
// the store behind the Host API and KnownValues redactor, and this package's
// tests.
package secretstore
