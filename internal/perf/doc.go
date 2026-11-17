// Package perf holds the speed budgets of the core: tests that assert how fast
// something runs. They carry the perf build tag, so no PR job runs them; the
// nightly job runs them with `task perf`, takes the median of several
// measurements against each budget and prints every number to the log.
//
// Owner: quality gates (docs/quality-gates.md section 6). Allowed callers:
// none; the package has no exported API.
package perf
