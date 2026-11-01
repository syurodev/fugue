// Package secretlog reports secrets that reach slog, fmt, errors or test output
// without going through the redact package.
//
// Owner: quality gates (docs/quality-gates.md section 4). Allowed callers:
// tools/fuguevet.
package secretlog
