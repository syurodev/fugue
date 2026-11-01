// Package config builds the typed configuration of the host from the
// environment and the operating system's user directories.
//
// Owner: configuration port (docs/quality-gates.md section 3). It is the only
// package that reads environment variables. Allowed callers: cmd/* and the
// composition root; other packages receive the values they need as parameters.
package config
