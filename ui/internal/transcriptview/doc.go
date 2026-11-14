// Package transcriptview lays a session's transcript out with Gio (F10.4a):
// a virtualized list that lays out only the visible item cards plus fifty
// above and fifty below, a layout cache keyed by item and revision bounded at
// two thousand entries, and the item cards of transcript §3. Unknown kinds
// render as generic cards: an item is never dropped and a bad payload never
// crashes the app.
//
// The view owns layout only: colors come from the theme tokens and the state
// it mutates changes on the window goroutine, which also applies the session
// model's updates (ui/internal/session). It reads, never writes: the composer
// and the write path are F10.4b.
//
// Owner: F10.4a. Allowed callers: appwin; nothing outside ui/.
package transcriptview
