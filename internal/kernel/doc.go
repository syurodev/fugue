// Package kernel provides the pure deterministic state machine of the native
// agent harness: command and event types, total state evolution, derived
// projections (lifecycle, phase, wait reasons), and stable rejection types.
//
// The kernel is pure and side-effect free: it imports only pure standard
// library packages, performs no I/O, spawns no goroutines, reads no clocks,
// and mutates no package-level state.
//
// Owner: state machine (docs/adr/0001-package-layout.md, G-02).
// Allowed callers: internal/codec, internal/execstore, internal/coordinator,
// internal/contextengine, internal/modeladapter, internal/tools, internal/app,
// internal/hostapi.
package kernel
