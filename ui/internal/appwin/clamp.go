// Package appwin owns the app's one window: the Gio event loop, the light and
// dark themes (DA-08), the connection status bar, and the initial window size
// clamped to the screen's work area. The screens of F10.3–F10.5 build inside
// the body this skeleton draws.
//
// Owner: desktop app (ui module). Allowed callers: ui/cmd/fugue-app only.
package appwin

// requestedW and requestedH are the window's preferred initial size in dp
// (FUG-83): landscape 1280x800 dp for the multi-column shell, clamped to the
// work area where known (macOS) before the window opens.
const (
	requestedW = 1280
	requestedH = 800
)

// minW and minH keep the window usable on very small work areas: below them
// the window keeps the minimum rather than shrinking to nothing.
const (
	minW = 360
	minH = 240
)

// ClampDp fits a requested window size to the work area: the work area is
// known in pixels, the window is sized in dp, and dp are pixels divided by
// the scale, so the dp budget is the pixel size floored through the scale.
// The result never goes below the usable minimum. A scale or work area that
// is not known (zero on platforms where the compositor owns the size) leaves
// the request untouched: on Wayland and XWayland the compositor clamps the
// granted size itself (F10.2a scope note).
func ClampDp(w, h int, scale, screenW, screenH float64) (int, int) {
	if scale <= 0 || screenW <= 0 || screenH <= 0 {
		return w, h
	}
	maxW := int(screenW / scale)
	maxH := int(screenH / scale)
	if maxW < minW {
		maxW = minW
	}
	if maxH < minH {
		maxH = minH
	}
	if w > maxW {
		w = maxW
	}
	if h > maxH {
		h = maxH
	}
	return w, h
}
