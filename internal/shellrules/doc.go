// Package shellrules provides static shell command parsing, default risk rules,
// and workspace policy evaluation for shell commands Fugue runs (roadmap F6.3c,
// interaction §3.3, I-E04).
//
// Commands are parsed into simple commands across pipelines, and-or lists,
// sequential separators, subshells, and command substitutions. Unparsable constructs
// (heredocs, eval, source/., dynamic command names, sh/bash -c, unclosed delimiters,
// or inputs exceeding bounds) fail closed by requiring confirmation in Auto mode.
//
// Default rules detect remote push operations, destructive writes/removals outside
// workspace roots, download-and-execute pipelines, deploy/infrastructure operations,
// and execution targets requiring confirmation. Workspace allow prefixes never
// supersede default risk rules or hard confirmation requirements.
//
// Owner: interaction (docs/adr/0002-vendor-domain.md, I-03, §3.3). Allowed callers:
// internal/interaction, internal/workspace, internal/tools, internal/app, and test callers.
package shellrules
