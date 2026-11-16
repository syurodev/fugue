package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"
)

// The window overlay (ui-kit UK-07). The window owns one overlay and draws
// it last: a floating surface records its ops in an op.Record and hands them
// to op.Defer, which runs them after every other op of the frame in the
// transform of the call site. Gio cannot read the current transform, so an
// anchor's place in the window is known only from the offsets the caller
// went through: Offset is the one helper that moves the cursor and keeps that
// record, and a floating surface reads the window's rectangle in the cursor's
// own coordinates from it, which is all it needs to flip at an edge.

// Offset runs fn with the layout cursor moved by p, and records the move so
// anchors laid out inside fn know where they are in the window. Use it
// instead of an op.Offset around anything that holds an anchor.
func (e *Env) Offset(gtx *layout.Context, p image.Point, fn func(*layout.Context)) {
	e.origin = e.origin.Add(p)
	defer func() { e.origin = e.origin.Sub(p) }()
	at(gtx, p, fn)
}

// windowRect is the window in the cursor's coordinates.
func (e *Env) windowRect() image.Rectangle {
	return image.Rectangle{Max: e.win}.Sub(e.origin)
}

// claimOverlay gives the window's one overlay to owner: whoever opened last
// holds it, and a holder that sees another one close itself (holdsOverlay).
func (e *Env) claimOverlay(owner event.Tag) { e.overlay = owner }

// holdsOverlay reports whether owner still has the overlay.
func (e *Env) holdsOverlay(owner event.Tag) bool { return e.overlay == owner }

// releaseOverlay frees the overlay if owner holds it.
func (e *Env) releaseOverlay(owner event.Tag) {
	if e.overlay == owner {
		e.overlay = nil
	}
}

// placeBelow puts a panel of size under anchor a, its left edge on the
// anchor's, and flips where it would leave the window: to the anchor's right
// edge when it runs out of room on the right, and above the anchor when it
// runs out below and fits above. Where neither side fits it stays below and
// is pushed back inside. up reports the vertical flip, so the panel can grow
// from the edge that touches the anchor.
func placeBelow(a image.Rectangle, size image.Point, win image.Rectangle, gap int) (pos image.Point, up bool) {
	pos = image.Pt(a.Min.X, a.Max.Y+gap)
	if pos.X+size.X > win.Max.X {
		pos.X = a.Max.X - size.X
	}
	if pos.Y+size.Y > win.Max.Y && a.Min.Y-gap-size.Y >= win.Min.Y {
		pos.Y, up = a.Min.Y-gap-size.Y, true
	}
	return clampInto(pos, size, win), up
}

// placeBeside puts a panel of size to the right of a, its top on the
// anchor's, and flips to the left of a when it would leave the window on the
// right and fits on the left: a submenu opens sideways (§6.15). left reports
// the flip.
func placeBeside(a image.Rectangle, size image.Point, win image.Rectangle, gap int) (pos image.Point, left bool) {
	pos = image.Pt(a.Max.X+gap, a.Min.Y)
	if pos.X+size.X > win.Max.X && a.Min.X-gap-size.X >= win.Min.X {
		pos.X, left = a.Min.X-gap-size.X, true
	}
	return clampInto(pos, size, win), left
}

// clampInto moves a panel's top-left the least it takes to bring the panel
// inside win; a panel larger than the window sticks to its top-left.
func clampInto(pos, size image.Point, win image.Rectangle) image.Point {
	pos.X = max(min(pos.X, win.Max.X-size.X), win.Min.X)
	pos.Y = max(min(pos.Y, win.Max.Y-size.Y), win.Min.Y)
	return pos
}
