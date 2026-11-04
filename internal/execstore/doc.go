// Package execstore provides the SQL transaction adapter and persistent store
// for execution events, projections, receipts, admissions and sealed payloads.
// It persists transitions atomically with single-writer fencing and enforces
// append-only integrity with SQLite triggers.
//
// Owner: storage (docs/adr/0001-package-layout.md, G-03). Allowed callers:
// internal/coordinator and internal/app.
package execstore
