package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
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

// floatingWindowRect is the window inset by the §3.1 edge margin (SpaceMD) so
// floating menus and popovers never touch or clip against the window border.
func (e *Env) floatingWindowRect(gtx *layout.Context) image.Rectangle {
	win := e.windowRect()
	margin := gtx.Dp(theme.SpaceMD)
	if win.Dx() > 2*margin && win.Dy() > 2*margin {
		return win.Inset(margin)
	}
	return win
}

// The floating layers (UK-07): at most two at once — a menu open from a
// popover — one slot per kind, and the window's one dialog layer above them
// (§6.17: never a dialog on a dialog). A claim of a kind replaces that
// kind's holder, which closes itself when it finds itself out; a third
// floating layer can only be a dialog.

// claimMenu gives owner the window's one menu layer.
func (e *Env) claimMenu(owner event.Tag) {
	if e.menuLayer == owner {
		return
	}
	e.menuLayer = owner
	e.menuSeq++
}

// holdsMenu reports whether owner still holds the menu layer.
func (e *Env) holdsMenu(owner event.Tag) bool {
	return owner != nil && e.menuLayer == owner
}

// releaseMenu frees the menu layer if owner holds it.
func (e *Env) releaseMenu(owner event.Tag) {
	if e.menuLayer == owner {
		e.menuLayer = nil
	}
}

// claimPopover gives owner the window's one popover layer.
func (e *Env) claimPopover(owner event.Tag) {
	if e.popLayer == owner {
		return
	}
	e.popLayer = owner
	e.popSeq++
}

// holdsPopover reports whether owner still holds the popover layer.
func (e *Env) holdsPopover(owner event.Tag) bool {
	return owner != nil && e.popLayer == owner
}

// releasePopover frees the popover layer if owner holds it.
func (e *Env) releasePopover(owner event.Tag) {
	if e.popLayer == owner {
		e.popLayer = nil
	}
}

// topPopover reports whether the popover owns Escape (§7): no dialog above
// it and no newer menu over it. A menu owns Escape through its own focused
// filter, so only the popover asks.
func (e *Env) topPopover(owner event.Tag) bool {
	if e.dialog != nil || e.menuLayer != nil && e.menuSeq > e.popSeq {
		return false
	}
	return e.popLayer == owner
}

// claimDialog gives owner the window's one dialog layer (§6.17): it reports
// false while another dialog holds it — a dialog never opens over a dialog.
func (e *Env) claimDialog(owner event.Tag) bool {
	if e.dialog != nil && e.dialog != owner {
		return false
	}
	e.dialog = owner
	return true
}

// releaseDialog frees the dialog layer if owner holds it.
func (e *Env) releaseDialog(owner event.Tag) {
	if e.dialog == owner {
		e.dialog = nil
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
