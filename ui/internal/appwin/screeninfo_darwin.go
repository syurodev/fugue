//go:build darwin

package appwin

// WorkArea returns the main screen's visible work area in pixels and the
// backing scale, so the initial window size can be clamped before the window
// opens (F10.1: 900x1000 dp overflows a 1080p screen at scale 1.25). A screen
// it cannot query reports zeros, which leaves the request unclamped.
//
// AppKit constrains a window's frame itself, but the app still asks for a
// sane initial size; NSScreen is the one place the numbers are exact.
//
// Owner: desktop app (ui module). Allowed callers: this package only.
func WorkArea() (screenW, screenH, scale float64) {
	return cgoWorkArea()
}
