// Package cmdid mints Host API idempotency command IDs that stay unique across
// restarts and concurrent desktop app processes.
//
// Owner: desktop app (ui module). Allowed callers: ui internal packages; the
// core module never imports this module.
package cmdid
