// Command fuguegate runs the quality gates that no off-the-shelf tool covers:
// the coverage floor and baseline, short fuzzing, mutation testing of changed
// packages, the dead-code check and the proto gate (buf lint, format, breaking
// changes, generated code up to date), the commit and PR title rules, the
// guard on gate-defining files, and the machine-wide heavy gate lock that
// serializes heavy gate work across worktrees (exclusive). It also owns the
// stage A release gate (docs/release.md): release-evidence checks the registry
// and the live records against the release candidate and writes
// release-evidence.md, and verify-live guides the owner through a live check
// and writes its record. The product binary has no verification command in
// stage A (activation A-06), so that command lives here.
//
// Owner: quality gates (docs/quality-gates.md sections 6 and 8). It is a
// development tool, never shipped in a release. Allowed callers: Taskfile.yml
// and CI; nothing imports it. Its only internal import is the time port
// (internal/platform/clock), per docs/adr/0001-package-layout.md.
package main
