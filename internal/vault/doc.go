// Package vault owns the Master Password, the lock state and the in-memory data
// key. Every process start is locked: the data key exists only after a
// successful Setup, Unlock or UnlockWithRecovery and is zeroed by Lock. Other
// packages seal and open records through SealRecord and OpenRecord, and watch
// the lifecycle through Observe and RegisterLockGuard.
//
// Owner: storage security (docs/adr/0001-package-layout.md, G-03). Allowed
// callers: internal/credentials, internal/execstore and internal/app. The
// keyring is persisted through the KeyringStore port; the SQL implementation
// over the vault_meta table arrives with the storage package (F1.3).
package vault
