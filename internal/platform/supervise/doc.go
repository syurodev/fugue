// Package supervise starts goroutines. Every goroutine in the repository starts
// through Go, so it has an owner (the returned Task), a name and a stop path.
//
// Owner: concurrency rules (AGENTS.md; fuguevet nobarego). It is the only
// package where a bare go statement is legal. Allowed callers: every package
// that runs work in the background.
package supervise
