// Package sandbox isolates untrusted agent and tool processes using Seatbelt
// on macOS and bubblewrap plus Landlock on Linux.
//
// Owner: tool system (docs/adr/0001-package-layout.md, tool-system §6).
// Allowed callers: internal/tools and internal/app.
package sandbox
