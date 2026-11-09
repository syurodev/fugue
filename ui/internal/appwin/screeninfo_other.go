//go:build !darwin

package appwin

// WorkArea reports that the work area is unknown on Linux: the compositor is
// the authority there. On Wayland the granted size always fits, and on
// XWayland the window manager places the window (Q-32: stage A has no native
// X11 session), so the request is left unclamped and the granted size wins.
//
// Owner: desktop app (ui module). Allowed callers: this package only.
func WorkArea() (screenW, screenH, scale float64) {
	return 0, 0, 0
}
