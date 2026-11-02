// Package interaction owns the in-memory approval and selection requests for
// the current host incarnation, the session-scoped confirmation grants and the
// one-use receipts claimed at the tool execution boundary.
//
// Requests are keyed by operation ID so a retried check reads back the same
// request without asking twice, and every request may be answered at most once.
// Process restart or vault lock invalidates all pending requests, session
// grants and unconsumed receipts.
//
// Owner: interaction (docs/adr/0001-package-layout.md, G-05). Allowed callers:
// internal/tools and internal/app.
package interaction
