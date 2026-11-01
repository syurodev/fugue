// Package redact keeps secrets out of logs, errors and output: Secret hides its
// value from every formatter, and the handlers it builds mask slog attributes
// named like secrets.
//
// Owner: security invariants (AGENTS.md; fuguevet secretlog). Allowed callers:
// every package that holds a secret or builds a logger.
package redact
