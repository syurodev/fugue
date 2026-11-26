package appwin

// The slot grid's model (desktop-app §5.3, DA-10): pure operations over a
// LayoutDoc — arrange moves, fill, split, make main, close, the companion
// placement order, drag-and-drop targets, and the narrow-window fold plan.
// Everything here is derivable from the document; the view state in
// slotgrid.go applies the ops and marks the document dirty for persistence.

// The grid's shape limits (§5.3): three columns of two rows, the main slot
// pinned at the top of the first column.
const (
	maxGridColumns   = 3
	maxRowsPerColumn = 2
)

// Pos is one grid cell. The main slot is always (0,0).
type Pos struct {
	Col int
	Row int
}

// key names the cell the way the document's FilledSlot does.
func (p Pos) key() string { return slotKey(p.Col, p.Row) }

// Direction is one step of the arrange popover's Move command.
type Direction uint8

// The four move directions.
const (
	DirLeft Direction = iota
	DirRight
	DirUp
	DirDown
)

// slotAt returns the slot at p, ok false when the grid has no such cell.
func slotAt(doc LayoutDoc, p Pos) (Slot, bool) {
	if p.Col < 0 || p.Col >= len(doc.Columns) {
		return Slot{}, false
	}
	col := doc.Columns[p.Col]
	if p.Row < 0 || p.Row >= len(col.Slots) {
		return Slot{}, false
	}
	return col.Slots[p.Row], true
}

// isMainPos reports whether p is the main slot's fixed cell.
func isMainPos(p Pos) bool { return p.Col == 0 && p.Row == 0 }

// regularCells lists the grid's non-main cells in column-then-row order, the
// focus and slot numbering order (§5.3: Ctrl/Cmd+1 is the main slot).
func regularCells(doc LayoutDoc) []Pos {
	out := make([]Pos, 0, len(doc.Columns)*maxRowsPerColumn)
	for c, col := range doc.Columns {
		for r := range col.Slots {
			if c == 0 && r == 0 {
				continue
			}
			out = append(out, Pos{Col: c, Row: r})
		}
	}
	return out
}

// cloneLayout deep-copies the document so an op never shares slice backing
// with its input.
func cloneLayout(doc LayoutDoc) LayoutDoc {
	out := doc
	out.Columns = make([]Column, len(doc.Columns))
	for i, col := range doc.Columns {
		out.Columns[i] = Column{WidthWeight: col.WidthWeight, Slots: append([]Slot(nil), col.Slots...)}
	}
	return out
}

// normalizeLayout re-establishes the invariants after a mutation: no empty
// column except the first (which keeps the main slot), weights positive, and
// a filled slot that lost its cell clears.
func normalizeLayout(doc *LayoutDoc) {
	kept := doc.Columns[:0]
	for i, col := range doc.Columns {
		if len(col.Slots) == 0 && i > 0 {
			continue
		}
		kept = append(kept, col)
	}
	doc.Columns = kept
	if doc.FilledSlot != "" {
		c, r, ok := parseSlotKey(doc.FilledSlot)
		if !ok {
			doc.FilledSlot = ""
		} else if _, still := slotAt(*doc, Pos{Col: c, Row: r}); !still {
			doc.FilledSlot = ""
		}
	}
}

// removeSlotAt lifts the slot at p out of its cell. Columns keep their
// indices — the empty column prunes in normalizeLayout — so a caller can
// insert using positions taken before the removal.
func removeSlotAt(doc *LayoutDoc, p Pos) Slot {
	col := &doc.Columns[p.Col]
	slot := col.Slots[p.Row]
	col.Slots = append(col.Slots[:p.Row], col.Slots[p.Row+1:]...)
	return slot
}

// MoveSlot moves the regular slot at p one step. Left and right go to the
// neighboring column's same row, up and down within the column; an occupied
// target swaps, an existing empty cell receives. The main slot's cell is
// never a target, so moving toward it is not offered (§5.3).
func MoveSlot(doc LayoutDoc, p Pos, dir Direction) (LayoutDoc, bool) {
	if isMainPos(p) {
		return doc, false
	}
	to, ok := neighborCell(doc, p, dir)
	if !ok {
		return doc, false
	}
	out := cloneLayout(doc)
	src := out.Columns[p.Col].Slots[p.Row]
	if dst, occupied := slotAt(out, to); occupied {
		src.Ref, dst.Ref = dst.Ref, src.Ref
		out.Columns[p.Col].Slots[p.Row] = src
		out.Columns[to.Col].Slots[to.Row] = dst
		return out, true
	}
	_ = removeSlotAt(&out, p)
	col := &out.Columns[to.Col]
	col.Slots = append(col.Slots, Slot{})
	copy(col.Slots[to.Row+1:], col.Slots[to.Row:])
	col.Slots[to.Row] = src
	normalizeLayout(&out)
	return out, true
}

// neighborCell returns the cell one step in dir: it must exist inside the
// grid and must not be the main slot's cell.
func neighborCell(doc LayoutDoc, p Pos, dir Direction) (Pos, bool) {
	to := p
	switch dir {
	case DirLeft:
		to.Col--
	case DirRight:
		to.Col++
	case DirUp:
		to.Row--
	case DirDown:
		to.Row++
	default:
		return Pos{}, false
	}
	if _, ok := slotAt(doc, to); !ok {
		return Pos{}, false
	}
	if isMainPos(to) {
		return Pos{}, false
	}
	return to, true
}

// ToggleFill fills the slot at p across the whole grid, or returns it to the
// grid when it is the filled one (§5.3). The other slots stay open underneath.
func ToggleFill(doc LayoutDoc, p Pos) (LayoutDoc, bool) {
	if _, ok := slotAt(doc, p); !ok {
		return doc, false
	}
	out := cloneLayout(doc)
	if out.FilledSlot == p.key() {
		out.FilledSlot = ""
	} else {
		out.FilledSlot = p.key()
	}
	return out, true
}

// SplitBesideMain places the slot at p in the bottom cell of column 0, right
// under the main slot (§5.3 "Chia đôi"); ok is false when column 0 already
// has two rows.
func SplitBesideMain(doc LayoutDoc, p Pos) (LayoutDoc, bool) {
	if isMainPos(p) || len(doc.Columns[0].Slots) >= maxRowsPerColumn {
		return doc, false
	}
	out := cloneLayout(doc)
	slot := removeSlotAt(&out, p)
	main := &out.Columns[0]
	main.Slots = append(main.Slots, slot)
	normalizeLayout(&out)
	return out, true
}

// MakeMain exchanges the slot at p with the main slot's content; the main
// slot itself never leaves (0,0) (§5.3 "Đặt làm chính").
func MakeMain(doc LayoutDoc, p Pos) (LayoutDoc, bool) {
	if isMainPos(p) {
		return doc, false
	}
	if _, ok := slotAt(doc, p); !ok {
		return doc, false
	}
	out := cloneLayout(doc)
	a := out.Columns[0].Slots[0]
	b := out.Columns[p.Col].Slots[p.Row]
	a.Ref, b.Ref = b.Ref, a.Ref
	out.Columns[0].Slots[0] = a
	out.Columns[p.Col].Slots[p.Row] = b
	normalizeLayout(&out)
	return out, true
}

// CloseSlot removes the regular slot at p; a column left empty folds away,
// column 0 keeps the main slot.
func CloseSlot(doc LayoutDoc, p Pos) LayoutDoc {
	if isMainPos(p) {
		return doc
	}
	if _, ok := slotAt(doc, p); !ok {
		return doc
	}
	out := cloneLayout(doc)
	_ = removeSlotAt(&out, p)
	normalizeLayout(&out)
	return out
}

// PlaceNearest puts ref in the empty cell nearest the main slot — column 1
// top, column 1 bottom, column 2 top, column 2 bottom, then the bottom of
// column 0 (§5.3, DA-E11) — creating a column when one of the first two
// orders names one the grid does not have yet. ok is false when the grid is
// full. This is the companion panel's auto-open placement.
func PlaceNearest(doc LayoutDoc, ref SlotRef) (LayoutDoc, Pos, bool) {
	for _, p := range nearestOrder() {
		if p.Col >= len(doc.Columns) {
			if p.Col >= maxGridColumns {
				continue
			}
			out := cloneLayout(doc)
			out.Columns = append(out.Columns, Column{WidthWeight: 1, Slots: []Slot{{HeightWeight: 1, Ref: ref}}})
			return out, p, true
		}
		if p.Row >= len(doc.Columns[p.Col].Slots) {
			out := cloneLayout(doc)
			col := &out.Columns[p.Col]
			col.Slots = append(col.Slots, Slot{HeightWeight: 1, Ref: ref})
			return out, p, true
		}
	}
	return doc, Pos{}, false
}

// nearestOrder returns the companion placement order: nearest to the main
// slot first (§5.3).
func nearestOrder() [5]Pos {
	return [5]Pos{{Col: 1, Row: 0}, {Col: 1, Row: 1}, {Col: 2, Row: 0}, {Col: 2, Row: 1}, {Col: 0, Row: 1}}
}

// DropKind names one drop-zone shape of a slot drag (§5.3).
type DropKind uint8

// The drop-zone shapes: a column edge creates a column there, a cell half
// splits its column into two rows, and a slot's center swaps.
const (
	DropColumnLeft DropKind = iota
	DropColumnRight
	DropCellTop
	DropCellBottom
	DropSwap
)

// DropTarget is one valid drop zone: its shape and the column/cell it
// attaches to. The renderer gives each target its rectangle.
type DropTarget struct {
	Kind DropKind
	Pos  Pos
}

// ValidDropTargets lists the zones a drag from dragged may end at. Never
// offered: a fourth column, a third row, anything left of the main slot
// (a new column before column 0), and the main slot's own cell (§5.3, DA-E09).
func ValidDropTargets(doc LayoutDoc, dragged Pos) []DropTarget {
	if isMainPos(dragged) {
		return nil
	}
	out := make([]DropTarget, 0, 2*len(doc.Columns)+len(doc.Columns)*maxRowsPerColumn)
	out = append(out, columnEdgeTargets(doc)...)
	out = append(out, cellHalfTargets(doc)...)
	out = append(out, swapTargets(doc, dragged)...)
	return out
}

// columnEdgeTargets offers a new column at every column's edges while the
// grid has room; a new column never goes before column 0 (§5.3).
func columnEdgeTargets(doc LayoutDoc) []DropTarget {
	if len(doc.Columns) >= maxGridColumns {
		return nil
	}
	out := make([]DropTarget, 0, 2*len(doc.Columns))
	for c := range doc.Columns {
		if c > 0 {
			out = append(out, DropTarget{Kind: DropColumnLeft, Pos: Pos{Col: c}})
		}
		out = append(out, DropTarget{Kind: DropColumnRight, Pos: Pos{Col: c}})
	}
	return out
}

// cellHalfTargets offers splitting a one-row column into two rows; above
// the main slot would displace it, under it is the optional bottom slot
// (§5.3).
func cellHalfTargets(doc LayoutDoc) []DropTarget {
	out := make([]DropTarget, 0, len(doc.Columns)*2)
	for c, col := range doc.Columns {
		if len(col.Slots) >= maxRowsPerColumn {
			continue
		}
		for r := range col.Slots {
			if c == 0 && r == 0 {
				out = append(out, DropTarget{Kind: DropCellBottom, Pos: Pos{Col: 0, Row: 0}})
				continue
			}
			out = append(out, DropTarget{Kind: DropCellTop, Pos: Pos{Col: c, Row: r}})
			out = append(out, DropTarget{Kind: DropCellBottom, Pos: Pos{Col: c, Row: r}})
		}
	}
	return out
}

// swapTargets offers trading places with every other regular slot.
func swapTargets(doc LayoutDoc, dragged Pos) []DropTarget {
	cells := regularCells(doc)
	out := make([]DropTarget, 0, len(cells))
	for _, p := range cells {
		if p == dragged {
			continue
		}
		out = append(out, DropTarget{Kind: DropSwap, Pos: p})
	}
	return out
}

// ApplyDrop moves the regular slot at dragged per target, which ValidDropTargets
// must have offered: a swap exchanges the two cells in place, every other
// shape vacates the dragged cell first. A user drop of kind kind is the
// caller's cue to remember the position per kind (DA-E11).
func ApplyDrop(doc LayoutDoc, dragged Pos, t DropTarget) LayoutDoc {
	if isMainPos(dragged) {
		return doc
	}
	if _, ok := slotAt(doc, dragged); !ok {
		return doc
	}
	if t.Kind == DropSwap {
		if _, ok := slotAt(doc, t.Pos); !ok {
			return doc
		}
		out := cloneLayout(doc)
		a := out.Columns[dragged.Col].Slots[dragged.Row]
		b := out.Columns[t.Pos.Col].Slots[t.Pos.Row]
		a.Ref, b.Ref = b.Ref, a.Ref
		out.Columns[dragged.Col].Slots[dragged.Row] = a
		out.Columns[t.Pos.Col].Slots[t.Pos.Row] = b
		return out
	}
	out := cloneLayout(doc)
	// The dragged cell empties without collapsing its column, so the
	// target's positions stay the ones ValidDropTargets computed.
	slot := removeSlotAt(&out, dragged)
	switch t.Kind {
	case DropColumnLeft:
		out.Columns = append(out.Columns, Column{})
		copy(out.Columns[t.Pos.Col+1:], out.Columns[t.Pos.Col:])
		out.Columns[t.Pos.Col] = Column{WidthWeight: 1, Slots: []Slot{slot}}
	case DropColumnRight:
		at := t.Pos.Col + 1
		out.Columns = append(out.Columns, Column{})
		if at < len(out.Columns)-1 {
			copy(out.Columns[at+1:], out.Columns[at:])
		}
		out.Columns[at] = Column{WidthWeight: 1, Slots: []Slot{slot}}
	case DropCellTop, DropCellBottom:
		col := &out.Columns[t.Pos.Col]
		at := t.Pos.Row
		if t.Kind == DropCellBottom {
			at++
		}
		col.Slots = append(col.Slots, Slot{})
		copy(col.Slots[at+1:], col.Slots[at:])
		col.Slots[at] = slot
	case DropSwap:
	}
	normalizeLayout(&out)
	return out
}

// PlanFoldResult is the temporary narrow-window fold's plan (§5.3): while
// the window cannot fit every column at its minimum width, regular columns
// fold from the right into the permanent Right Stripe; still too narrow, the
// sidebar folds to the rail. The main slot never folds. The plan is derived
// state — widening the window unfolds in reverse — and never mutates the
// stored layout.
type PlanFoldResult struct {
	// FoldedCols is how many regular columns are folded from the right.
	FoldedCols int
	// TempSidebarFolded is the width-driven sidebar fold, separate from the
	// user's persisted SidebarCollapsed.
	TempSidebarFolded bool
}

// PlanFold computes the fold for a shell availDp wide (permanent Left Rail,
// Right Stripe, Sidebar Island, IslandGap gutters, and grid together, window
// insets excluded, matching layoutMinWidthDp) holding cols columns;
// sidebarOpen is the sidebar's on-screen state before this plan.
func PlanFold(availDp int, sidebarOpen bool, cols int) PlanFoldResult {
	regular := max(cols-1, 0)
	folded := 0
	for folded < regular && availDp < layoutMinWidthDp(cols-folded, sidebarOpen) {
		folded++
	}
	tempSidebar := sidebarOpen && availDp < layoutMinWidthDp(cols-folded, true)
	return PlanFoldResult{FoldedCols: folded, TempSidebarFolded: tempSidebar}
}
