package appwin

import (
	"gioui.org/io/key"
	"gioui.org/layout"
)

// The slot grid's keyboard side (desktop-app §5.3): focus order and the
// shortcut keys — Ctrl/Cmd+1..6 focus slots in column-then-row order with
// the main slot first, F6 and Shift+F6 cycle, Ctrl/Cmd+W closes the focused
// regular slot, Ctrl/Cmd+Shift+Enter toggles its fill, and plain Escape
// unwinds the shell's states one layer at a time: an active drag, then the
// drawer, then the fill (§7).

// focusOrder lists the slots in focus order (§5.3): the main slot first,
// then the regular slots column-then-row.
func (g *slotGrid) focusOrder() []Pos {
	out := make([]Pos, 0, len(g.doc.Columns)*maxRowsPerColumn)
	out = append(out, Pos{Col: 0, Row: 0})
	return append(out, regularCells(g.doc)...)
}

// setFocus focuses the slot at p.
func (g *slotGrid) setFocus(p Pos) {
	g.focus = p.key()
}

// focusCycle moves the focus one slot forward or backward (§5.3 F6).
func (g *slotGrid) focusCycle(back bool) {
	order := g.focusOrder()
	if len(order) == 0 {
		return
	}
	cur := 0
	for i, p := range order {
		if p.key() == g.focus {
			cur = i
			break
		}
	}
	step := 1
	if back {
		step = -1
	}
	g.focus = order[(cur+step+len(order))%len(order)].key()
}

// handleShortcuts consumes the shell's keyboard shortcuts (§5.3): all with
// modifiers, so a Vietnamese IME never swallows a plain letter. Ctrl/Cmd+1..6
// focus slots in column-then-row order, F6 and Shift+F6 cycle focus,
// Ctrl/Cmd+W closes the focused regular slot, and Ctrl/Cmd+Shift+Enter
// toggles its fill.
func (g *slotGrid) handleShortcuts(gtx *layout.Context) {
	g.drainKey(gtx, key.Filter{Required: key.ModShortcut, Name: key.Name("W")}, func(key.Event) { g.closeFocused() })
	g.drainKey(gtx, key.Filter{Required: key.ModShortcut | key.ModShift, Name: key.NameReturn}, func(key.Event) { g.toggleFillFocused() })
	g.drainKey(gtx, key.Filter{Name: key.NameF6, Optional: key.ModShift}, func(k key.Event) { g.focusCycle(k.Modifiers.Contain(key.ModShift)) })
	for n := range maxGridColumns * maxRowsPerColumn {
		slot := n + 1
		g.drainKey(gtx, key.Filter{Required: key.ModShortcut, Name: key.Name(string(rune('0' + slot)))}, func(key.Event) {
			if order := g.focusOrder(); slot <= len(order) {
				g.setFocus(order[slot-1])
			}
		})
	}
}

// drainKey consumes every event a filter delivers in this frame and runs
// act on each press.
func (g *slotGrid) drainKey(gtx *layout.Context, f key.Filter, act func(key.Event)) {
	for {
		ke, ok := gtx.Event(f)
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			act(k)
		}
	}
}

// handleEscape consumes plain Escape: it cancels an active drag first, then
// closes the drawer, then unfills (§5.3, §7).
func (g *slotGrid) handleEscape(gtx *layout.Context) {
	for {
		ke, ok := gtx.Event(key.Filter{Name: key.NameEscape})
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			switch {
			case g.drag.active:
				g.cancelDrag()
			case g.drawer != "":
				g.drawer = ""
			case g.doc.FilledSlot != "":
				c, r, _ := parseSlotKey(g.doc.FilledSlot)
				if doc, ok := ToggleFill(g.doc, Pos{Col: c, Row: r}); ok {
					g.edited(doc)
				}
			}
		}
	}
}

// closeFocused closes the focused regular slot; the main slot never closes
// (§5.3).
func (g *slotGrid) closeFocused() {
	c, r, ok := parseSlotKey(g.focus)
	if !ok || isMainPos(Pos{Col: c, Row: r}) {
		return
	}
	g.edited(CloseSlot(g.doc, Pos{Col: c, Row: r}))
	g.focus = ""
}

// toggleFillFocused toggles fill on the focused slot (§5.3).
func (g *slotGrid) toggleFillFocused() {
	c, r, ok := parseSlotKey(g.focus)
	if !ok {
		return
	}
	if doc, ok := ToggleFill(g.doc, Pos{Col: c, Row: r}); ok {
		g.edited(doc)
	}
}
