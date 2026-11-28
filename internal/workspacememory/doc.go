// Package workspacememory owns persistent plaintext workspace memory entries,
// immutable write history, revision-based compare-and-swap, verified-flag rules,
// anchor freshness, and pre-write safety checks (workspace-memory.mdx §1-§3).
//
// Owner: workspacememory (WM-01).
// Allowed callers: internal/app and this package's tests.
package workspacememory
