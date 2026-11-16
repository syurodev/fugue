package kit

import (
	"image"

	"gioui.org/io/pointer"
	"gioui.org/layout"
)

// The pointer side of Menu (§6.15). Events are read before the frame lays
// out, against the panels of the previous frame — the ones the pointer was
// actually over — and the state they change is what the frame then resolves,
// places and draws. A menu on its way out is not asked anything: it takes no
// input (§3.7).

// input reads the frame's pointer events of every open panel and of the
// absorbing area under them.
func (m *MenuState) input(gtx *layout.Context, res *MenuResult) {
	if !m.lv[0].open.Shown() {
		return
	}
	for k := range m.lv {
		if m.lv[k].open.Shown() {
			m.levelEvents(gtx, k, res)
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.abs, Kinds: pointer.Press})
		if !ok {
			break
		}
		if pe, is := ev.(pointer.Event); is && pe.Kind == pointer.Press {
			m.Close()
			res.Closed = true
		}
	}
}

// levelEvents applies panel k's events in order.
func (m *MenuState) levelEvents(gtx *layout.Context, k int, res *MenuResult) {
	lv := &m.lv[k]
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: lv, Kinds: pointer.Move | pointer.Enter | pointer.Leave | pointer.Press})
		if !ok {
			return
		}
		pe, is := ev.(pointer.Event)
		if !is {
			continue
		}
		at := image.Pt(int(pe.Position.X), int(pe.Position.Y))
		switch pe.Kind {
		case pointer.Move, pointer.Enter:
			m.pointerAt(k, at)
		case pointer.Leave:
			// The highlight stays on the parent of an open submenu while the
			// pointer travels to it.
			if lv.sub < 0 {
				lv.hi = -1
			}
		case pointer.Press:
			m.press(k, at, res)
		case pointer.Release, pointer.Cancel, pointer.Drag, pointer.Scroll:
		}
	}
}

// pointerAt highlights the item under the pointer and opens its submenu, or
// closes the open one — unless the pointer is on its way to that submenu.
func (m *MenuState) pointerAt(k int, p image.Point) {
	lv := &m.lv[k]
	prev := m.last
	m.last = p
	if m.towardSub(k, p, prev) {
		return
	}
	idx := lv.itemAt(p)
	if idx >= 0 && (lv.items[idx].Disabled || lv.items[idx].separator) {
		idx = -1
	}
	lv.hi, lv.sub = idx, -1
	if idx >= 0 && len(lv.items[idx].Sub) > 0 {
		lv.sub = idx
	}
}

// press chooses the item under the pointer, or opens its submenu; a press on
// padding, a separator or a disabled item changes nothing and goes no further
// than the panel.
func (m *MenuState) press(k int, p image.Point, res *MenuResult) {
	lv := &m.lv[k]
	idx := lv.itemAt(p)
	if idx < 0 || lv.items[idx].Disabled || lv.items[idx].separator {
		return
	}
	if len(lv.items[idx].Sub) > 0 {
		lv.hi, lv.sub = idx, idx
		return
	}
	res.Chosen, res.Closed = lv.items[idx].ID, true
	m.Close()
}

// itemAt is the row under p, or -1 over the padding.
func (lv *menuLevel) itemAt(p image.Point) int {
	inner := lv.rect.Inset(lv.pad)
	if !p.In(inner) {
		return -1
	}
	for i, r := range lv.rows {
		if p.Y-inner.Min.Y < r.y+r.h {
			return i
		}
	}
	return -1
}

// towardSub reports whether p lies on the way from prev to the open submenu
// of level k: inside the triangle of prev and the submenu's near edge. A
// pointer crossing the items below the parent on a diagonal toward the
// submenu then keeps it open instead of closing it (§6.15).
func (m *MenuState) towardSub(k int, p, prev image.Point) bool {
	if k+1 >= menuDepth || m.lv[k].sub < 0 || !m.lv[k+1].open.Shown() {
		return false
	}
	sub := &m.lv[k+1]
	x := sub.rect.Min.X
	if sub.dir.X > 0 {
		x = sub.rect.Max.X
	}
	return inTriangle(p, prev, image.Pt(x, sub.rect.Min.Y), image.Pt(x, sub.rect.Max.Y))
}

// inTriangle reports whether p is inside or on the triangle abc, by the sign
// of p against each edge.
func inTriangle(p, a, b, c image.Point) bool {
	d1, d2, d3 := edgeSide(p, a, b), edgeSide(p, b, c), edgeSide(p, c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !neg || !pos
}

// edgeSide is the sign of the cross product that tells which side of the edge
// ab the point p is on.
func edgeSide(p, a, b image.Point) int {
	return (p.X-b.X)*(a.Y-b.Y) - (a.X-b.X)*(p.Y-b.Y)
}
