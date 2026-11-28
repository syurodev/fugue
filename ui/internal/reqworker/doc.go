// Package reqworker provides a bounded, generic off-UI-thread request/result
// worker for Host API callers such as secrets and workspace memory.
//
// Owner: FUG-113. Allowed callers: ui/internal/secrets, ui/internal/memory.
package reqworker
