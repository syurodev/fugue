// Package snapshot captures and restores workspace file trees in a per-project
// shadow Git repository outside the user's own .git directory, with separate
// retention and session quotas for turn snapshots and per-tool before/after
// snapshots.
//
// Owner: snapshot (docs/adr/0001-package-layout.md, tool-system §9). Allowed
// callers: internal/tools and internal/app.
package snapshot
