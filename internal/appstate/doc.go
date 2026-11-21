// Package appstate stores the app's window layouts and preferences
// (host-api §5.2): versioned JSON documents the app defines, keyed by a
// UUID window_id, outside the vault so the app rebuilds every window while
// the vault is still locked. Documents are validated in their envelope only
// — valid JSON, an integer schema_version >= 1, and the size limits — never
// interpreted: the host does not know what a slot contains.
//
// Owner: application layer (F10.20a). Allowed callers: internal/app, which
// composes the store behind the Host API, and this package's tests.
package appstate
