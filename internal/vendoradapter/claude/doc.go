// Package claude adapts the Claude Code CLI: the -p stream-json surface, the
// probe with the tested version range, the normalizer from stream-json events
// into transcript items, the slash command list and rate limit windows
// (vendor-runtime §5.1, roadmap F9.5).
// Owner: vendor runtime (G-04). Allowed callers: internal/app (the session
// actor) and tests. F9.5b adds the process session on internal/process.
package claude
