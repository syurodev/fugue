// Package quality holds the data and self-tests of the quality gates:
// coverage.json (per-package coverage baselines), and the fixtures under
// testdata that deliberately violate each gate so a test can prove the gate
// still fires.
//
// Owner: quality gates (docs/quality-gates.md). The package has no production
// code and no callers; it is run only by `go test`.
package quality
