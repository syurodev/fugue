// Package teams defines the pure domain models, state machines, task
// specifications, hierarchy rules, and role catalog validation for orchestrated
// teams (orchestration v0.2).
//
// It performs no I/O, reads no clocks, and spawns no goroutines.
//
// Owner: orchestration (stage A, F11.1).
// Allowed callers: internal/vendorstore and internal/app.
package teams
