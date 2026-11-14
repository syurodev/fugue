// Package kitspike is the throwaway UI kit spike (ui-kit §9): four hard
// components (Button, TextField, Menu, SplitPane) built on Gio v0.10.3 to
// answer the spike questions, plus the tokens of ui-kit §3 as constants.
// The code is thrown away when the spike reports; what carries over is the
// numbers, the screenshots and the Gio guide draft in docs/ui/gio-guide.md.
//
// Nothing here is production code: no gate compliance beyond tests passing
// with -race, no Host API awareness (UK-10).
//
// Owner: UI kit spike. Allowed callers: ui/cmd/kitspike only.
package kitspike
