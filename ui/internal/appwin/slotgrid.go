package appwin

import (
	"image"
	"time"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
)

// The window's slot-grid state (desktop-app §5.3): the layout document plus
// everything the shell needs to draw and drive it — the rail selection, the
// drag, the drawer, the focus, the animations and the arrange popover. All
// of it lives and changes on the window goroutine; the document's mutations
// go through the pure ops of gridmodel.go and mark the document dirty for
// the persistence glue in slotpersist.go.

// railItem is one rail entry (§5.3): chat, team, then the registered
// modules in install order. Stage A registers none, so the rail is chat and
// team.
type railItem uint8

// The rail items.
const (
	railChat railItem = iota
	railTeam
	railModules
)

// mainKindOf maps a rail item to the slot kind its work view holds.
func mainKindOf(item railItem) SlotKind {
	switch item {
	case railChat:
		return KindSession
	case railTeam:
		return KindTeam
	case railModules:
		return KindModule
	}
	return KindModule
}

// railOfKind maps a main-slot kind onto its rail item; any other kind keeps
// the chat rail.
func railOfKind(kind SlotKind) railItem {
	switch kind {
	case KindTeam:
		return railTeam
	case KindModule:
		return railModules
	case KindSession, KindAgent, KindDiff, KindInbox, KindAccounts, KindSettings, KindSecrets:
		return railChat
	case SlotKind(""):
		return railChat
	}
	return railChat
}

// slotGrid is the app shell's grid.
type slotGrid struct {
	doc LayoutDoc
	// docGen counts document replacements so the screens re-track session
	// topics only when the document changed.
	docGen uint64
	// rail is the selected rail item and railLast each item's last opened
	// object (§5.3).
	rail     railItem
	railLast map[SlotKind]SlotRef
	// kindPos remembers where the user dragged each kind's panel, so its
	// next auto-open lands there (§5.3, DA-E11).
	kindPos map[SlotKind]Pos

	// Persistence side (slotpersist.go): rev is the stored document's CAS
	// revision, dirty a local edit waiting to save, higher a stored document
	// newer than this build understands (run on the default, save nothing
	// until a user edit), and loaded marks the first snapshot applied.
	windowID string
	rev      uint64
	dirty    bool
	saving   bool
	higher   bool
	loaded   bool
	cmdSeq   uint64
	saveAt   time.Time

	// View state.
	rects      map[string]image.Rectangle
	refRects   map[string]image.Rectangle
	anims      map[string]*slotAnim
	splitC     map[int]*kit.SplitPaneState
	splitR     map[int]*kit.SplitPaneState
	headerTags map[string]*headerTag
	drag       dragState
	drawer     string
	// drawerPresence slides the drawer; lastArea and lastVisible are the
	// frame's grid area and visible-column count the drag and drawer reuse.
	lastArea       image.Rectangle
	lastVisible    int
	drawerPresence motion.Presence
	focus          string
	// popover is the arrange popover and popFor the cell it opened from.
	popover *kit.PopoverState
	popFor  string
	// buttons of the chrome: per cell the arrange and close buttons, per
	// folded cell the edge tab, and the rail's and collapsed sidebar's own
	// buttons.
	arrangeBtns map[string]*kit.IconButtonState
	closeBtns   map[string]*kit.IconButtonState
	tabBtns     map[string]*kit.IconButtonState
	railBtns    map[railItem]*kit.IconButtonState
	sidebarBtn  *kit.IconButtonState
	arrPop      arrangeActions
	// newWindow is the last "open in new window" request (F10.21 consumes).
	newWindow *NewWindowRequest
	// applyGeometry applies a restored window size; the window owns the
	// option, the shell only knows the numbers. Nil in the tests.
	applyGeometry func(widthDp, heightDp int)
}

// arrangeCmd is one arrange-popover command (§5.3).
type arrangeCmd uint8

// The arrange commands: the four move directions, then fill, split beside
// the main slot, make main, and open in a new window.
const (
	arrangeMoveLeft arrangeCmd = iota
	arrangeMoveRight
	arrangeMoveUp
	arrangeMoveDown
	arrangeFill
	arrangeSplit
	arrangeMakeMain
	arrangeNewWindow
)

// arrangeActions are the arrange popover's buttons (§5.3: move, fill, split,
// make main, open in new window).
type arrangeActions struct {
	move   [4]*kit.ButtonState
	fill   *kit.ButtonState
	split  *kit.ButtonState
	main   *kit.ButtonState
	newWin *kit.ButtonState
}

func newArrangeActions() arrangeActions {
	var a arrangeActions
	for i := range a.move {
		a.move[i] = kit.NewButtonState()
	}
	a.fill, a.split, a.main, a.newWin = kit.NewButtonState(), kit.NewButtonState(), kit.NewButtonState(), kit.NewButtonState()
	return a
}

// newSlotGrid builds the shell over the default layout.
func newSlotGrid() *slotGrid {
	return &slotGrid{
		doc:            DefaultLayout(),
		railLast:       map[SlotKind]SlotRef{},
		kindPos:        map[SlotKind]Pos{},
		rects:          map[string]image.Rectangle{},
		refRects:       map[string]image.Rectangle{},
		anims:          map[string]*slotAnim{},
		splitC:         map[int]*kit.SplitPaneState{},
		splitR:         map[int]*kit.SplitPaneState{},
		headerTags:     map[string]*headerTag{},
		popover:        kit.NewPopoverState(),
		arrangeBtns:    map[string]*kit.IconButtonState{},
		closeBtns:      map[string]*kit.IconButtonState{},
		tabBtns:        map[string]*kit.IconButtonState{},
		railBtns:       map[railItem]*kit.IconButtonState{},
		sidebarBtn:     kit.NewIconButtonState(),
		arrPop:         newArrangeActions(),
		drawerPresence: motion.NewPresence(motion.Smooth(), motion.Exit()),
	}
}

// syncFromDoc re-seeds the rail memory after the document changes from
// restore or a make-main: the chat rail remembers the main slot's session.
func (g *slotGrid) syncFromDoc() {
	main := g.doc.Columns[0].Slots[0].Ref
	if main.ID != "" {
		g.railLast[main.Kind] = main
	}
	g.rail = railOfKind(main.Kind)
}

// edited records a user-driven layout change: it arms the slide animations
// of every ref that changed cell (§5.3: slots slide on Smooth), clears a
// higher-schema guard (the user's own edit may overwrite, host-api §5.2),
// and marks the document for a save.
func (g *slotGrid) edited(doc LayoutDoc) {
	for _, p := range allCells(doc) {
		ref := refAt(doc, p)
		if ref.Kind == "" {
			continue
		}
		if old, moved := findCell(g.doc, ref); moved && old != p {
			if rect := g.rects[old.key()]; !rect.Empty() {
				g.startAnim(keyOfRef(ref), rect)
			}
		}
	}
	g.doc = doc
	g.docGen++
	g.higher = false
	g.dirty = g.loaded
	g.saveAt = time.Time{}
}

// allCells lists every cell of the document, main included.
func allCells(doc LayoutDoc) []Pos {
	out := make([]Pos, 0, len(doc.Columns)*maxRowsPerColumn)
	for c, col := range doc.Columns {
		for r := range col.Slots {
			out = append(out, Pos{Col: c, Row: r})
		}
	}
	return out
}

// findCell locates the cell holding ref, main included.
func findCell(doc LayoutDoc, ref SlotRef) (Pos, bool) {
	for _, p := range allCells(doc) {
		if slot, ok := slotAt(doc, p); ok && slot.Ref == ref {
			return p, true
		}
	}
	return Pos{}, false
}

// markGeometryDirty arms a geometry-only save, keeping the higher-schema
// guard: the window's size is not a layout edit.
func (g *slotGrid) markGeometryDirty(w, h int) {
	if !g.loaded || g.higher {
		return
	}
	if g.doc.Window.WidthDp == w && g.doc.Window.HeightDp == h {
		return
	}
	g.doc.Window = WindowGeometry{WidthDp: w, HeightDp: h}
	g.dirty = true
	g.saveAt = time.Time{}
}

// mainRef is the main slot's current object.
func (g *slotGrid) mainRef() SlotRef { return g.doc.Columns[0].Slots[0].Ref }

// selectRail switches the rail item: the main slot takes the item's last
// opened object, the regular slots stay put (§5.3).
func (g *slotGrid) selectRail(item railItem) {
	kind := mainKindOf(item)
	last, ok := g.railLast[kind]
	if !ok {
		last = SlotRef{Kind: kind}
	}
	doc := cloneLayout(g.doc)
	doc.Columns[0].Slots[0].Ref = last
	g.rail = item
	g.syncFromDoc()
	g.edited(doc)
}

// openMain opens a session in the main slot: the content swaps in place,
// the slot does not move (§5.3).
func (g *slotGrid) openMain(ref SlotRef) {
	doc := cloneLayout(g.doc)
	doc.Columns[0].Slots[0].Ref = ref
	g.railLast[ref.Kind] = ref
	g.rail = railOfKind(ref.Kind)
	g.edited(doc)
}

// openCompanion opens ref in the nearest empty slot (§5.3, DA-E11): a slot
// of the kind already open updates in place, the kind's remembered dragged
// position wins when its cell is empty, and otherwise the placement order
// (1,0)(1,1)(2,0)(2,1)(0,1) applies. Only the viewed session's own use opens
// a companion, and only on the module's first use in a turn — the callers
// own that gate.
func (g *slotGrid) openCompanion(ref SlotRef) {
	if g.mainRef().Kind == ref.Kind && ref.Kind == KindModule {
		// The main slot is that module's workbench; the panel stays closed
		// (§5.3).
		return
	}
	for _, p := range regularCells(g.doc) {
		if slot, _ := slotAt(g.doc, p); slot.Ref.Kind == ref.Kind {
			doc := cloneLayout(g.doc)
			doc.Columns[p.Col].Slots[p.Row].Ref = ref
			g.edited(doc)
			return
		}
	}
	if pos, known := g.kindPos[ref.Kind]; known && pos.Col < len(g.doc.Columns) &&
		pos.Row >= len(g.doc.Columns[pos.Col].Slots) {
		doc := cloneLayout(g.doc)
		doc.Columns[pos.Col].Slots = append(doc.Columns[pos.Col].Slots, Slot{})
		copy(doc.Columns[pos.Col].Slots[pos.Row+1:], doc.Columns[pos.Col].Slots[pos.Row:])
		doc.Columns[pos.Col].Slots[pos.Row] = Slot{HeightWeight: 1, Ref: ref}
		g.edited(doc)
		return
	}
	doc, _, _ := PlaceNearest(g.doc, ref)
	g.edited(doc)
}

// closeSlotOf closes the first regular slot of kind, for the views'
// back-to-chat actions.
func (g *slotGrid) closeSlotOf(kind SlotKind) {
	for _, p := range regularCells(g.doc) {
		if slot, _ := slotAt(g.doc, p); slot.Ref.Kind == kind {
			g.edited(CloseSlot(g.doc, p))
			return
		}
	}
}

// arrange runs one arrange-popover command on the slot at p (§5.3).
func (g *slotGrid) arrange(p Pos, cmd arrangeCmd) {
	if dir, move := directionOf(cmd); move {
		g.applyMove(p, dir)
		return
	}
	g.applyArrange(p, cmd)
}

// directionOf maps the four move commands onto their direction; ok is false
// for every other command.
func directionOf(cmd arrangeCmd) (Direction, bool) {
	switch cmd {
	case arrangeMoveLeft:
		return DirLeft, true
	case arrangeMoveRight:
		return DirRight, true
	case arrangeMoveUp:
		return DirUp, true
	case arrangeMoveDown:
		return DirDown, true
	case arrangeFill, arrangeSplit, arrangeMakeMain, arrangeNewWindow:
		return DirLeft, false
	}
	return DirLeft, false
}

// applyArrange runs the non-move commands: fill, split, make main and open
// in a new window (§5.3).
func (g *slotGrid) applyArrange(p Pos, cmd arrangeCmd) {
	switch cmd {
	case arrangeFill:
		if doc, ok := ToggleFill(g.doc, p); ok {
			g.edited(doc)
		}
	case arrangeSplit:
		if doc, ok := SplitBesideMain(g.doc, p); ok {
			g.edited(doc)
		}
	case arrangeMakeMain:
		if doc, ok := MakeMain(g.doc, p); ok {
			g.edited(doc)
			g.syncFromDoc()
		}
	case arrangeNewWindow:
		g.requestNewWindow(p)
	case arrangeMoveLeft:
	case arrangeMoveRight:
	case arrangeMoveUp:
	case arrangeMoveDown:
	}
}

// requestNewWindow records the layout document a secondary window starts
// from: the ref as its main slot, the sidebar collapsed (§5.3). F10.21
// attaches the window creation.
func (g *slotGrid) requestNewWindow(p Pos) {
	if isMainPos(p) {
		return
	}
	slot, ok := slotAt(g.doc, p)
	if !ok {
		return
	}
	doc := DefaultLayout()
	doc.Columns[0].Slots[0] = Slot{HeightWeight: 1, Ref: slot.Ref}
	doc.SidebarCollapsed = true
	doc.FilledSlot = ""
	g.newWindow = &NewWindowRequest{Pos: p, Ref: slot.Ref, Doc: doc}
}

// applyMove applies one Move direction, keeping the popover's slot.
func (g *slotGrid) applyMove(p Pos, dir Direction) {
	if doc, ok := MoveSlot(g.doc, p, dir); ok {
		g.edited(doc)
	}
}

// openArrange opens the arrange popover for the slot at p (§5.3).
func (g *slotGrid) openArrange(p Pos) {
	g.popFor = p.key()
	g.popover.Open()
}

// drop applies a finished drag (§5.3): the slot lands per the target and
// the kind's position memory records where the user put it (DA-E11).
func (g *slotGrid) drop(dragged Pos, t DropTarget) {
	slot, ok := slotAt(g.doc, dragged)
	if !ok {
		return
	}
	doc := ApplyDrop(g.doc, dragged, t)
	g.edited(doc)
	for _, p := range regularCells(doc) {
		if after, _ := slotAt(doc, p); after.Ref == slot.Ref {
			g.kindPos[slot.Ref.Kind] = p
			return
		}
	}
}

// cancelDrag slides the dragged slot back to its cell (§5.3, DA-E09): the
// animation starts from the ghost's box, wherever the pointer left it.
func (g *slotGrid) cancelDrag() {
	if g.drag.active {
		if _, ok := slotAt(g.doc, g.drag.pos); ok {
			g.startAnim(keyOfRef(mustSlotAt(g.doc, g.drag.pos).Ref), g.previewRect(g.lastArea, g.lastArea.Size()))
		}
	}
	g.drag = dragState{}
}

// mustSlotAt is slotAt for callers that already hold a live position.
func mustSlotAt(doc LayoutDoc, p Pos) Slot {
	slot, _ := slotAt(doc, p)
	return slot
}

// startAnim arms a Smooth slide of refKey from from to wherever it lays out
// next (§5.3, §3.7).
func (g *slotGrid) startAnim(refKey string, from image.Rectangle) {
	if from.Empty() {
		return
	}
	g.anims[refKey] = &slotAnim{from: from, spring: motion.NewSpringSpec(0, motion.Smooth())}
	g.anims[refKey].spring.Target = 1
}

// advanceAnims steps every animation and drops the finished ones.
func (g *slotGrid) advanceAnims(env *kit.Env) {
	dt := env.DT()
	for key, anim := range g.anims {
		anim.spring.Advance(dt)
		if anim.spring.AtRest() {
			delete(g.anims, key)
			continue
		}
		env.Keep()
	}
}

// animOf returns the in-flight animation of a ref, or nil.
func (g *slotGrid) animOf(ref SlotRef) *slotAnim {
	return g.anims[keyOfRef(ref)]
}

// keyOfRef names a ref for the animation table: kind and id, the identity a
// moving slot keeps.
func keyOfRef(ref SlotRef) string { return string(ref.Kind) + ":" + ref.ID }

// refAt is the ref at p, empty outside the grid.
func refAt(doc LayoutDoc, p Pos) SlotRef {
	slot, ok := slotAt(doc, p)
	if !ok {
		return SlotRef{}
	}
	return slot.Ref
}
