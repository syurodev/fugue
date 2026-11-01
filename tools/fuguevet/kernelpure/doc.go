// Package kernelpure keeps internal/kernel pure: allowlisted standard-library
// imports only, no writable package variables and no go statements.
//
// Owner: quality gates (docs/quality-gates.md section 4). Allowed callers:
// tools/fuguevet.
package kernelpure
