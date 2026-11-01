// Package arch holds the architecture tests of docs/quality-gates.md section 5
// and the ratchets of section 8. It has no production code.
//
// Each rule is a function over a repository root, so the same code checks this
// repository and the deliberately violating mini-repositories under testdata.
//
// Owner: quality gates. Allowed callers: none; it is run only by `go test`.
package arch
