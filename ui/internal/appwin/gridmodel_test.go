package appwin

import (
	"bytes"
	"image"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// The slot grid model's evidence (DA-E09, DA-E10 one window, DA-E11): the
// main slot never leaves (0,0) and refuses drag, close and fold; a drag is
// offered only the valid zones — no fourth column, no third row, nothing
// left of the main slot, never the main slot's cell; the companion panel
// opens col 1 row 0 → col 1 row 1 → col 2 row 0 → col 2 row 1 → col 0 row 1
// and remembers where the user dragged it; the layout document round-trips
// through fugue.appstate.v1 with only kinds and ids; a newer schema runs on
// the default and is not overwritten (host-api §5.2).

// Test fixture constants shared by the grid model tests.
const (
	testGridMainSess = "main"
	testGridDiff     = "a.diff"
	fillKeyCol1Row0  = "1,0"
)

// gridFixture is a three-column document: the main slot, a session under it
// in column 0, and one slot in each of columns 1 and 2.
func gridFixture() LayoutDoc {
	return LayoutDoc{
		SchemaVersion: LayoutSchemaVersion,
		Columns: []Column{
			{WidthWeight: 2, Slots: []Slot{
				{HeightWeight: 2, Ref: SlotRef{Kind: KindSession, ID: testGridMainSess}},
				{HeightWeight: 1, Ref: SlotRef{Kind: KindDiff, ID: testGridDiff}},
			}},
			{WidthWeight: 1, Slots: []Slot{
				{HeightWeight: 1, Ref: SlotRef{Kind: KindInbox}},
			}},
			{WidthWeight: 1, Slots: []Slot{
				{HeightWeight: 1, Ref: SlotRef{Kind: KindAccounts}},
			}},
		},
	}
}

// kindsFixture builds the default grid with one regular column per kind.
func kindsFixture(kinds ...SlotKind) LayoutDoc {
	doc := DefaultLayout()
	for _, kind := range kinds {
		doc.Columns = append(doc.Columns, Column{WidthWeight: 1, Slots: []Slot{{HeightWeight: 1, Ref: SlotRef{Kind: kind}}}})
	}
	return doc
}

func posList(ps ...Pos) []Pos { return ps }

// hasKind reports whether a regular slot of the kind is open.
func hasKind(doc LayoutDoc, kind SlotKind) bool {
	for _, p := range regularCells(doc) {
		if slot, _ := slotAt(doc, p); slot.Ref.Kind == kind {
			return true
		}
	}
	return false
}

// image100 is a 100×100 grid area for the drag tests.
func image100() image.Rectangle { return image.Rectangle{Max: image.Pt(100, 100)} }

// TestMainSlotIsImmovable pins DA-E09's main-slot rule: (0,0) always holds
// the main slot, every arrange and drag op refuses it, closing it does
// nothing, and no fold plan ever folds its column.
func TestMainSlotIsImmovable(t *testing.T) {
	t.Parallel()
	doc := gridFixture()
	for _, dir := range []Direction{DirLeft, DirRight, DirUp, DirDown} {
		if _, ok := MoveSlot(doc, Pos{}, dir); ok {
			t.Fatalf("MoveSlot accepted the main slot in direction %v", dir)
		}
	}
	if closed := CloseSlot(doc, Pos{}); !cmp.Equal(closed, doc) {
		t.Fatal("CloseSlot mutated the grid with the main slot")
	}
	if _, ok := MakeMain(doc, Pos{}); ok {
		t.Fatal("MakeMain accepted the main slot as the argument")
	}
	if _, ok := SplitBesideMain(doc, Pos{}); ok {
		t.Fatal("SplitBesideMain accepted the main slot as the argument")
	}
	if len(ValidDropTargets(doc, Pos{})) != 0 {
		t.Fatal("the main slot was offered drop targets for a drag")
	}
	plan := PlanFold(0, false, len(doc.Columns))
	if first := len(doc.Columns) - plan.FoldedCols; first < 1 {
		t.Fatalf("the fold plan folded the main column: %+v", plan)
	}
	// MakeMain swaps contents and the main slot stays at (0,0) (§5.3).
	swapped, ok := MakeMain(doc, Pos{Col: 1, Row: 0})
	if !ok {
		t.Fatal("MakeMain refused a regular slot")
	}
	if got := swapped.Columns[0].Slots[0].Ref; got.Kind != KindInbox {
		t.Fatalf("main slot ref after the swap = %v, want the inbox", got)
	}
	if got := swapped.Columns[1].Slots[0].Ref; got.Kind != KindSession || got.ID != testGridMainSess {
		t.Fatalf("regular slot ref after the swap = %v, want the session", got)
	}
}

// TestDropZonesAreOnlyValidOnes pins DA-E09's zone rule on the full
// three-column grid: no fourth column, no third row, nothing left of column
// 0, and never the main slot's cell.
func TestDropZonesAreOnlyValidOnes(t *testing.T) {
	t.Parallel()
	doc := gridFixture()
	zones := ValidDropTargets(doc, Pos{Col: 1, Row: 0})
	if len(zones) == 0 {
		t.Fatal("a regular slot was offered no drop zones")
	}
	assertZonesInsideGrid(t, doc, zones)
	assertNoZoneLeftOfMain(t, zones)
	assertNoZoneAboveMain(t, zones)
	// A full grid — every column at two rows — offers no cell halves and no
	// new columns.
	full := LayoutDoc{Columns: []Column{
		{Slots: []Slot{{Ref: SlotRef{Kind: KindSession}}, {Ref: SlotRef{Kind: KindDiff, ID: testGridDiff}}}},
		{Slots: []Slot{{Ref: SlotRef{Kind: KindInbox}}, {Ref: SlotRef{Kind: KindAccounts}}}},
		{Slots: []Slot{{Ref: SlotRef{Kind: KindSettings}}, {Ref: SlotRef{Kind: KindTeam}}}},
	}}
	for _, z := range ValidDropTargets(full, Pos{Col: 1, Row: 0}) {
		if z.Kind == DropCellTop || z.Kind == DropCellBottom {
			t.Fatalf("zone %v on a two-row column", z.Kind)
		}
		if z.Kind == DropColumnLeft || z.Kind == DropColumnRight {
			t.Fatalf("zone %v on a full grid", z.Kind)
		}
	}
	// A single-column grid offers the bottom of the main cell, never its top.
	solo := DefaultLayout()
	solo.Columns[0].Slots = append(solo.Columns[0].Slots, Slot{Ref: SlotRef{Kind: KindInbox}})
	assertNoZoneAboveMain(t, ValidDropTargets(solo, Pos{Col: 0, Row: 1}))
}

// assertZonesInsideGrid checks every zone against the grid's bounds.
func assertZonesInsideGrid(t *testing.T, doc LayoutDoc, zones []DropTarget) {
	t.Helper()
	for _, z := range zones {
		switch z.Kind {
		case DropColumnLeft, DropColumnRight:
			if z.Pos.Col < 0 || z.Pos.Col >= maxGridColumns {
				t.Fatalf("zone %v names column %d, outside the grid", z.Kind, z.Pos.Col)
			}
		case DropCellTop, DropCellBottom:
			if z.Pos.Col < 0 || z.Pos.Col >= len(doc.Columns) || z.Pos.Row < 0 || z.Pos.Row >= maxRowsPerColumn {
				t.Fatalf("zone %v names cell %v, outside the grid", z.Kind, z.Pos)
			}
		case DropSwap:
		}
	}
}

// assertNoZoneLeftOfMain checks that no zone creates a column before 0 or
// moves onto the main slot from its right.
func assertNoZoneLeftOfMain(t *testing.T, zones []DropTarget) {
	t.Helper()
	for _, z := range zones {
		if z.Kind == DropColumnLeft && z.Pos.Col == 0 {
			t.Fatal("a new column was offered left of the main slot")
		}
	}
}

// assertNoZoneAboveMain checks the main cell only ever takes a slot under
// itself, never above.
func assertNoZoneAboveMain(t *testing.T, zones []DropTarget) {
	t.Helper()
	for _, z := range zones {
		if !isMainPos(z.Pos) {
			continue
		}
		if z.Kind == DropCellTop {
			t.Fatal("a zone offered displacing the main slot upward")
		}
		if z.Kind == DropSwap {
			t.Fatal("a swap zone on the main slot")
		}
	}
}

// TestDropApplies pins the shapes: an edge creates a column, a cell half
// splits the column into two rows, a center swaps, and the grid stays
// compact afterwards.
func TestDropApplies(t *testing.T) {
	t.Parallel()
	solo := DefaultLayout()
	solo.Columns[0].Slots = append(solo.Columns[0].Slots, Slot{Ref: SlotRef{Kind: KindInbox}})
	out := ApplyDrop(solo, Pos{Col: 0, Row: 1}, DropTarget{Kind: DropColumnRight, Pos: Pos{Col: 0}})
	if len(out.Columns) != 2 || out.Columns[1].Slots[0].Ref.Kind != KindInbox {
		t.Fatalf("edge drop = %+v, want the inbox in a new column", out)
	}

	doc := gridFixture()
	// Drop the diff slot (column 0 row 1) onto the top half of the inbox
	// cell: the column splits into two rows with the diff on top (§5.3).
	out = ApplyDrop(doc, Pos{Col: 0, Row: 1}, DropTarget{Kind: DropCellTop, Pos: Pos{Col: 1, Row: 0}})
	if len(out.Columns[1].Slots) != 2 {
		t.Fatalf("cell-half drop left %d rows in column 1, want 2", len(out.Columns[1].Slots))
	}
	if got := out.Columns[1].Slots[0].Ref; got.Kind != KindDiff {
		t.Fatalf("top-half drop landed %+v, want the diff on top", got)
	}
	if got := out.Columns[1].Slots[1].Ref; got.Kind != KindInbox {
		t.Fatalf("top-half drop pushed %+v down, want the inbox", got)
	}
	// Swap exchanges the two regular slots in place.
	out = ApplyDrop(doc, Pos{Col: 1, Row: 0}, DropTarget{Kind: DropSwap, Pos: Pos{Col: 2, Row: 0}})
	if got := out.Columns[1].Slots[0].Ref; got.Kind != KindAccounts {
		t.Fatalf("swap left %+v in column 1, want the accounts", got)
	}
	if got := out.Columns[2].Slots[0].Ref; got.Kind != KindInbox {
		t.Fatalf("swap left %+v in column 2, want the inbox", got)
	}
	// A slot leaving its only column compacts the grid.
	two := kindsFixture(KindInbox)
	out = ApplyDrop(two, Pos{Col: 1, Row: 0}, DropTarget{Kind: DropCellBottom, Pos: Pos{Col: 0, Row: 0}})
	if len(out.Columns) != 1 {
		t.Fatalf("grid kept %d columns after the source column emptied, want 1", len(out.Columns))
	}
	if got := out.Columns[0].Slots[1].Ref; got.Kind != KindInbox {
		t.Fatalf("bottom-of-main drop landed %+v, want the inbox under the main slot", got)
	}
}

// TestEscapeCancelReturnsTheSlot pins the cancel path: cancelDrag clears the
// drag and arms the slide-back animation from wherever the pointer was, so
// the slot glides home on Smooth (§5.3, DA-E09).
func TestEscapeCancelReturnsTheSlot(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.doc = gridFixture()
	g.lastArea = image100()
	g.drag = dragState{
		active: true, pos: Pos{Col: 1, Row: 0},
		origin: image.Pt(600, 0), pressAt: image.Pt(610, 10), grab: image.Pt(10, 10),
		pointer: image.Pt(500, 200),
	}
	ref := refAt(g.doc, Pos{Col: 1, Row: 0})
	g.cancelDrag()
	if g.drag.active || g.drag.armed {
		t.Fatal("cancelDrag left the drag running")
	}
	anim, ok := g.anims[keyOfRef(ref)]
	if !ok {
		t.Fatal("cancelDrag armed no slide-back animation")
	}
	if anim.spring.Target != 1 {
		t.Fatalf("slide-back spring target = %v, want 1", anim.spring.Target)
	}
}

// TestCompanionPlacementOrder pins DA-E11: the auto-open fills col 1 row 0,
// then col 1 row 1, then col 2 row 0, then col 2 row 1, then the bottom of
// column 0 — and a full grid opens nothing more.
func TestCompanionPlacementOrder(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	want := posList(Pos{Col: 1, Row: 0}, Pos{Col: 1, Row: 1}, Pos{Col: 2, Row: 0}, Pos{Col: 2, Row: 1}, Pos{Col: 0, Row: 1})
	for i, kind := range []SlotKind{KindInbox, KindAccounts, KindSettings, KindSecrets, KindTeam} {
		g.openCompanion(SlotRef{Kind: kind})
		if _, ok := slotAt(g.doc, want[i]); !ok || refAt(g.doc, want[i]).Kind != kind {
			t.Fatalf("companion %d landed outside %v: %+v", i+1, want[i], g.doc)
		}
	}
	// The grid is full: a sixth kind has nowhere to go.
	g.openCompanion(SlotRef{Kind: KindAgent})
	if got := len(allCells(g.doc)); got != 6 {
		t.Fatalf("a full grid grew to %d cells", got)
	}
}

// TestCompanionUpdatesInPlaceAndRemembersTheDrag pins the two memory rules
// of DA-E11: a second open of the kind updates the same slot instead of
// opening another, and the kind's next auto-open lands where the user last
// dragged it.
func TestCompanionUpdatesInPlaceAndRemembersTheDrag(t *testing.T) {
	t.Parallel()
	g := newSlotGrid()
	g.openCompanion(SlotRef{Kind: KindInbox})
	if _, ok := slotAt(g.doc, Pos{Col: 1, Row: 0}); !ok {
		t.Fatal("the first inbox open missed the nearest slot")
	}
	g.openCompanion(SlotRef{Kind: KindInbox})
	if got := len(regularCells(g.doc)); got != 1 {
		t.Fatalf("a second inbox open grew the grid to %d regular slots", got)
	}
	// The user drags the inbox to the bottom of column 0 (§5.3); the next
	// auto-open of the kind lands there, another kind's does not.
	g.drag = dragState{active: true, pos: Pos{Col: 1, Row: 0}}
	g.drop(Pos{Col: 1, Row: 0}, DropTarget{Kind: DropCellBottom, Pos: Pos{Col: 0, Row: 0}})
	g.edited(CloseSlot(g.doc, Pos{Col: 0, Row: 1}))
	if hasKind(g.doc, KindInbox) {
		t.Fatal("the inbox slot survived its own close")
	}
	g.openCompanion(SlotRef{Kind: KindInbox})
	if got := refAt(g.doc, Pos{Col: 0, Row: 1}); got.Kind != KindInbox {
		t.Fatalf("the remembered position ignored the drag: %+v", g.doc)
	}
	g.openCompanion(SlotRef{Kind: KindAccounts})
	if got := refAt(g.doc, Pos{Col: 1, Row: 0}); got.Kind != KindAccounts {
		t.Fatalf("the accounts open ignored the nearest order: %+v", g.doc)
	}
}

// TestLayoutDocumentRoundTrip pins DA-E10's storage: encode then decode
// keeps the structure, split weights, fill and sidebar flag; the document
// carries kinds and ids only — no content survives a decode.
func TestLayoutDocumentRoundTrip(t *testing.T) {
	t.Parallel()
	doc := gridFixture()
	doc.SidebarCollapsed = true
	doc.FilledSlot = fillKeyCol1Row0
	doc.Window = WindowGeometry{WidthDp: 900, HeightDp: 1000}
	raw, err := EncodeLayout(doc)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("transcript")) || bytes.Contains(raw, []byte("payload")) {
		t.Fatal("the layout document leaked content")
	}
	res, err := DecodeLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	if res.HigherSchema {
		t.Fatal("a current document decoded as a newer schema")
	}
	if !cmp.Equal(doc, res.Doc) {
		t.Fatalf("round trip changed the document: %s", cmp.Diff(doc, res.Doc))
	}
	// The envelope the host validates: schema_version first.
	if !bytes.Contains(raw, []byte(`{"schema_version":1`)) {
		t.Fatalf("envelope = %s", raw)
	}
}

// TestHigherSchemaFallsBackAndKeepsTheStoredDoc pins host-api §5.2: a
// schema_version above the build's decodes as the default with the guard
// set, and saves stay suppressed until a user edit clears the guard.
func TestHigherSchemaFallsBackAndKeepsTheStoredDoc(t *testing.T) {
	t.Parallel()
	res, err := DecodeLayout([]byte(`{"schema_version":99,"columns":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !res.HigherSchema {
		t.Fatal("a newer document decoded without the guard")
	}
	g := newSlotGrid()
	g.loaded = true
	g.applyStored(res, 7)
	if !g.higher {
		t.Fatal("the shell dropped the higher-schema guard")
	}
	if !cmp.Equal(g.doc, DefaultLayout()) {
		t.Fatal("the shell did not fall back to the default layout")
	}
	// A geometry save is suppressed; a user edit clears the guard.
	g.markGeometryDirty(1200, 800)
	if g.dirty {
		t.Fatal("geometry wrote into a newer document")
	}
	g.openCompanion(SlotRef{Kind: KindInbox})
	if g.higher {
		t.Fatal("the user's edit did not clear the higher-schema guard")
	}
	if !g.dirty {
		t.Fatal("the user's edit did not arm a save")
	}
}

// TestDecodeRepairsBrokenDocuments pins the conversion rule (host-api §5.2):
// a document with junk cells decodes by dropping the unknowns and repairing
// the main slot, never by failing the window.
func TestDecodeRepairsBrokenDocuments(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"schema_version":1,"columns":[
		{"width_weight":0,"slots":[{"height_weight":0,"kind":"session"},{"height_weight":1,"kind":"telepathy"}]},
		{"width_weight":1,"slots":[{"height_weight":1,"kind":"inbox"}]}
	]}`)
	res, err := DecodeLayout(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Doc.Columns[0].Slots[0].Ref.Kind; got != KindSession {
		t.Fatalf("main slot = %v, want the session kept", got)
	}
	if got := len(res.Doc.Columns[0].Slots); got != 1 {
		t.Fatalf("unknown kind survived: %d slots", got)
	}
	if got := res.Doc.Columns[0].WidthWeight; got != 1 {
		t.Fatalf("zero weight survived: %v", got)
	}
	// No columns at all: the default.
	res, err = DecodeLayout([]byte(`{"schema_version":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if !cmp.Equal(res.Doc, DefaultLayout()) {
		t.Fatal("an empty document did not default")
	}
}

// TestFoldPlanFoldsRightToLeftThenSidebar pins DA-E09's narrow-window rule:
// regular columns fold from the right, then the sidebar folds, and the main
// slot's column always stays.
func TestFoldPlanFoldsRightToLeftThenSidebar(t *testing.T) {
	t.Parallel()
	const (
		railDp    = 48
		sidebarDp = 240
		mainDp    = 520
		regDp     = 300
	)
	cols := 3
	plan := PlanFold(railDp+sidebarDp+mainDp+2*regDp, true, cols)
	if plan.FoldedCols != 0 || plan.TempSidebarFolded {
		t.Fatalf("a wide window folded: %+v", plan)
	}
	// Exactly the two-column minimum: fold the rightmost only.
	plan = PlanFold(railDp+sidebarDp+mainDp+regDp, true, cols)
	if plan.FoldedCols != 1 {
		t.Fatalf("folded = %d, want the rightmost column folded first", plan.FoldedCols)
	}
	if plan.TempSidebarFolded {
		t.Fatal("the sidebar folded while the columns still fit")
	}
	// Only the main column left and still too narrow: fold the sidebar.
	plan = PlanFold(railDp+sidebarDp+mainDp-1, true, cols)
	if plan.FoldedCols != 2 || !plan.TempSidebarFolded {
		t.Fatalf("plan = %+v, want both regular columns and then the sidebar folded", plan)
	}
	// The user's own collapse is not the width-driven fold.
	plan = PlanFold(railDp+mainDp-1, false, 1)
	if plan.TempSidebarFolded {
		t.Fatal("the width fold fired while the sidebar was already collapsed")
	}
	// The main slot never folds.
	plan = PlanFold(railDp+mainDp+regDp, true, 2)
	if plan.FoldedCols != 1 {
		t.Fatalf("folded = %d, want the regular column folded and the main kept", plan.FoldedCols)
	}
}

// TestArrangeMoveRefusesTheMainSlot pins the popover's move rules: a move
// toward the main slot is not offered, and a move within reach swaps or
// lands.
func TestArrangeMoveRefusesTheMainSlot(t *testing.T) {
	t.Parallel()
	doc := gridFixture()
	// The diff slot under the main slot cannot move up (the main is there).
	if _, ok := MoveSlot(doc, Pos{Col: 0, Row: 1}, DirUp); ok {
		t.Fatal("a move up into the main slot was offered")
	}
	// The inbox in column 1 row 0 cannot move left (the main slot is there).
	if _, ok := MoveSlot(doc, Pos{Col: 1, Row: 0}, DirLeft); ok {
		t.Fatal("a move left onto the main slot was offered")
	}
	// The inbox can move right: the accounts swap into column 1.
	out, ok := MoveSlot(doc, Pos{Col: 1, Row: 0}, DirRight)
	if !ok {
		t.Fatal("a move right into column 2 was refused")
	}
	if got := refAt(out, Pos{Col: 2, Row: 0}); got.Kind != KindInbox {
		t.Fatalf("move right landed %+v at column 2, want the inbox", got)
	}
	if got := refAt(out, Pos{Col: 1, Row: 0}); got.Kind != KindAccounts {
		t.Fatalf("move right left %+v in column 1, want the accounts swapped in", got)
	}
}
