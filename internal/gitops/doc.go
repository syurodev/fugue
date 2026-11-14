// Package gitops is the git manager for multi-agent teams (orchestration §9):
// task worktrees under the data directory, team and task branch lifecycle, the
// result commit, the reconciliation query of the restart note on F11.4, and
// pruning of expired worktrees (OR-11, OR-15, OR-E06).
//
// Every git invocation goes through one runner that disables automatic gc and
// repository hooks and runs with a scrubbed environment and LC_ALL=C. Repo
// level operations serialise on a mutex keyed by the repository's canonical
// common git dir, and ref-lock failures retry with backoff on the injected
// clock. The merge queue, the result gates and the layered integration belong
// to F11.5; this package holds the primitives only.
//
// Owner: orchestration (stage A, F11.4).
// Allowed callers: internal/app composes it for the delegation and result-gate
// services (F11.3, F11.5).
package gitops
