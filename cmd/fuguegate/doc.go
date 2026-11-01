// Command fuguegate runs the quality gates that no off-the-shelf tool covers:
// the coverage floor and baseline, short fuzzing, mutation testing of changed
// packages and the dead-code check.
//
// Owner: quality gates (docs/quality-gates.md sections 6 and 8). It is a
// development tool: it is invoked by Taskfile.yml and CI, never imported, and
// never shipped in a release.
package main
