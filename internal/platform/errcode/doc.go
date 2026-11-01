// Package errcode defines the typed errors of the repository: an Error carries a
// stable Code, codes are classified as public or hidden, and Expose turns any
// error into what a client may see.
//
// Owner: error contract (docs/error-codes.md, quality-gates section 5
// TestErrorCodesPublished). Allowed callers: every package that returns an
// error across a package boundary.
package errcode
