// Package markdown parses untrusted vendor markdown into an immutable
// document model (F10.3a): blocks, style-tagged spans and, for fenced code
// blocks whose language is known, syntax-role-tagged token spans. It is the
// data half of the renderer described by desktop-app DA-03; the Gio layout
// half is F10.3b and lives elsewhere. The package imports no GUI toolkit —
// in particular never gioui.org — so the model stays testable and drawable
// by any backend, and syntax roles carry no colors.
//
// Input is vendor output and therefore hostile: raw HTML becomes literal
// text, only http, https and mailto links are openable, bidi overrides and
// zero-width characters never survive into text or URLs, images are never
// fetched, and every resource limit is a named constant with truncation
// marked on the document.
//
// Owner: F10.3a. Allowed callers: the layout layer (F10.3b) and tests;
// nothing outside ui/.
package markdown
