// Package testutil holds the shared helpers that keep tests deterministic: the
// hang guard, the real-timer clock for tests that drive real processes, and
// the polling wait for state outside the process.
//
// Owner: quality gates (docs/quality-gates.md section 6). Allowed callers:
// test files only; the arch tests reject production imports.
package testutil
