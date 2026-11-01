// Command fuguegate runs the quality gates that no off-the-shelf tool covers:
// the coverage floor and baseline, short fuzzing, mutation testing of changed
// packages, the dead-code check and the proto gate (buf lint, format, breaking
// changes, generated code up to date).
//
// Owner: quality gates (docs/quality-gates.md sections 6 and 8). It is a
// development tool, never shipped in a release. Allowed callers: Taskfile.yml
// and CI; nothing imports it.
package main
