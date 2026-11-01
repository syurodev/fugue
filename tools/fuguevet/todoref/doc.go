// Package todoref allows a to-do marker only when it names a live roadmap item,
// in the form AGENTS.md prescribes; the other marker words (fix-me, hack, triple
// x) are never allowed.
//
// Owner: quality gates (docs/quality-gates.md section 4). Allowed callers:
// tools/fuguevet.
package todoref
