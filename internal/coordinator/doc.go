// Package coordinator provides the per-session actor, command serialization,
// worker capacity reservation, dispatch protocol, unknown-commit read-back,
// and worker observation fencing.
//
// Owner: execution (docs/adr/0001-package-layout.md, G-02). Allowed callers:
// internal/app.
package coordinator
