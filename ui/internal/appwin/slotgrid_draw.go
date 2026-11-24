package appwin

import (
	"image"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The slot grid's drawing side (desktop-app §5.3, ui-kit §6.27–6.28): cells
// lay out from the document's weights with the same minimum clamping the
// nested SplitPanes apply, the contents draw at their rects, and the
// column/row boundaries ride kit.SplitPane handles in both directions —
// double-click resets the ratio, the arrows move it by keyboard (§6.27).

// slotAnim is one in-flight rectangle animation: the slot slides from
// `from` to its laid-out rect on Smooth (§5.3: slots changing places slide).
type slotAnim struct {
	from   image.Rectangle
	spring motion.Spring
}

// closingSlot is a closed slot fading out on Exit while its neighbours slide
// into the freed space on Smooth (FUG-65).
type closingSlot struct {
	ref      SlotRef
	rect     image.Rectangle
	presence motion.Presence
}

// NewWindowRequest is the "open in new window" product: the ref to move and
// the layout document the new window starts from — the ref as its main slot,
// the sidebar collapsed (§5.3). F10.21 attaches window creation; until then
// the request is recorded so the action is wired end to end.
type NewWindowRequest struct {
	Pos Pos
	Ref SlotRef
	Doc LayoutDoc
}

// headerTag is one slot header's input tag; the map keeps the pointers
// stable across frames.
type headerTag struct {
	key string
}

// headerTagFor returns the header's tag for p.
func (g *slotGrid) headerTagFor(p Pos) *headerTag {
	tag, ok := g.headerTags[p.key()]
	if !ok {
		tag = &headerTag{key: p.key()}
		g.headerTags[p.key()] = tag
	}
	return tag
}

// layoutArea draws the grid area: the split tree (or the filled slot), the
// narrow-window tab strip and drawer, and an active drag's zones and
// preview. Every input runs here, on the window goroutine.
func (g *slotGrid) layoutArea(gtx *layout.Context, env *kit.Env, sc *screens, area image.Rectangle, plan PlanFoldResult) {
	if !sc.anyFloatLayerOpen() {
		g.handleEscape(gtx)
	}
	g.handleShortcuts(gtx)
	g.advanceAnims(env)
	visible := len(g.doc.Columns) - plan.FoldedCols
	g.lastArea, g.lastVisible = area, visible
	geom := g.computeRects(gtx, area, visible)
	g.recordRects(geom)

	if key := g.doc.FilledSlot; key != "" {
		if c, r, ok := parseSlotKey(key); ok {
			g.drawSlot(gtx, env, sc, Pos{Col: c, Row: r}, area)
		}
	} else {
		for _, cs := range g.closing {
			g.drawClosingSlot(gtx, env, sc, cs)
		}
		for _, cg := range geom {
			g.drawSlot(gtx, env, sc, cg.pos, cg.Rect)
		}
		// A slot's close button, an EmptyState's close or a view's back to
		// chat replaces the document while the cells draw: the handles
		// follow the columns that are left, not the count this frame
		// planned with (FUG-64).
		visible = min(visible, len(g.doc.Columns))
		g.overlaySplits(gtx, env, geom, area, visible)
	}
	if plan.FoldedCols > 0 {
		g.layoutFolded(gtx, env, sc, area, plan)
	}
	if g.drag.active {
		g.layoutDrag(gtx, env, sc, geom, area)
	}
}

// overlaySplits lays the SplitPane handles over the boundaries: one per
// column boundary over the columns it divides, and one per two-row column
// over the column's rect. A moved handle writes the ratio back into the
// weights — the split ratios are the layout (§5.3, ui-kit §6.27). visible
// is the column count the frame planned with; a close inside the frame may
// have left fewer columns, so it never indexes past the document.
func (g *slotGrid) overlaySplits(gtx *layout.Context, env *kit.Env, geom []cellGeom, area image.Rectangle, visible int) {
	visible = min(visible, len(g.doc.Columns))
	if visible <= 0 {
		return
	}
	for i := range visible - 1 {
		state, ok := g.splitC[i]
		if !ok {
			state = kit.NewSplitPaneState()
			g.splitC[i] = state
		}
		left := g.rectOf(geom, Pos{Col: i, Row: 0})
		sub := image.Rect(left.Min.X, area.Min.Y, area.Max.X, area.Max.Y)
		local := *gtx
		local.Constraints = layout.Exact(sub.Size())
		atPoint(&local, sub.Min, func(gtx *layout.Context) {
			local := *gtx
			ratio, changed, _ := state.Layout(env, &local, kit.SplitPaneProps{
				Orientation:  kit.Horizontal,
				Ratio:        columnRatio(g.doc, i, visible),
				DefaultRatio: defaultColumnRatio(i, visible),
				MinFirst:     colMin(i),
				MinSecond:    sumDp(colMins(i+1, visible)),
			}, emptyPane, emptyPane)
			if changed {
				g.setColumnRatio(i, visible, ratio)
				g.edited(g.doc)
			}
		})
	}
	for c := range visible {
		g.overlayRowSplit(gtx, env, geom, visible, c)
	}
}

// overlayRowSplit lays one two-row column's vertical SplitPane over its
// cells and writes a moved ratio back into the row weights.
func (g *slotGrid) overlayRowSplit(gtx *layout.Context, env *kit.Env, geom []cellGeom, visible, c int) {
	// c may name a column a close inside the frame took away (FUG-64).
	if c < 0 || c >= visible || c >= len(g.doc.Columns) || len(g.doc.Columns[c].Slots) < maxRowsPerColumn {
		return
	}
	state, ok := g.splitR[c]
	if !ok {
		state = kit.NewSplitPaneState()
		g.splitR[c] = state
	}
	top := g.rectOf(geom, Pos{Col: c, Row: 0})
	bottom := g.rectOf(geom, Pos{Col: c, Row: 1})
	if top.Empty() || bottom.Empty() {
		return
	}
	colRect := image.Rect(top.Min.X, top.Min.Y, top.Max.X, bottom.Max.Y)
	local := *gtx
	local.Constraints = layout.Exact(colRect.Size())
	atPoint(&local, colRect.Min, func(gtx *layout.Context) {
		local := *gtx
		ratio, changed, _ := state.Layout(env, &local, kit.SplitPaneProps{
			Orientation:  kit.Vertical,
			Ratio:        rowRatio(g.doc.Columns[c]),
			DefaultRatio: 0.5,
			MinFirst:     theme.SlotMinHeight,
			MinSecond:    theme.SlotMinHeight,
		}, emptyPane, emptyPane)
		if changed {
			col := g.doc.Columns[c]
			col.Slots[0].HeightWeight = ratio
			col.Slots[1].HeightWeight = 1 - ratio
			g.edited(g.doc)
		}
	})
}

// emptyPane is SplitPane's pane widget: the handles overlay cells drawn
// elsewhere.
func emptyPane(layout.Context) layout.Dimensions { return layout.Dimensions{} }

// drawSlot draws one slot: the header band across the top — every slot,
// the main one included, so the band runs continuously over the grid (§5.3)
// — and the body in the rectangle under it. A slot mid-slide draws at the
// interpolated rect (§3.7).
func (g *slotGrid) drawSlot(gtx *layout.Context, env *kit.Env, sc *screens, p Pos, rect image.Rectangle) {
	if rect.Empty() {
		return
	}
	ref := refAt(g.doc, p)
	draw := rect
	if anim := g.animOf(ref); anim != nil && !anim.from.Empty() {
		draw = lerpRect(anim.from, rect, anim.spring.Value)
	}
	h := min(gtx.Dp(theme.SlotHeaderHeight), draw.Dy())
	head := image.Rect(draw.Min.X, draw.Min.Y, draw.Max.X, draw.Min.Y+h)
	body := image.Rect(draw.Min.X, draw.Min.Y+h, draw.Max.X, draw.Max.Y)
	if isMainPos(p) {
		g.drawHeaderBar(gtx, env, sc, p, ref, head, nil)
	} else {
		g.drawChrome(gtx, env, sc, p, head)
	}
	env.Offset(gtx, body.Min, func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: body.Size()}
		defer clip.Rect{Max: body.Size()}.Push(local.Ops).Pop()
		sc.layoutSlotBody(&local, env, p, ref)
	})
}

// drawClosingSlot paints a closing slot's header and surface fading out on
// Exit while remaining slots slide over or into the freed space.
func (g *slotGrid) drawClosingSlot(gtx *layout.Context, env *kit.Env, sc *screens, cs *closingSlot) {
	if cs.rect.Empty() {
		return
	}
	alpha := cs.presence.Progress()
	if alpha <= 0 {
		return
	}
	defer paint.PushOpacity(gtx.Ops, alpha).Pop()
	h := min(gtx.Dp(theme.SlotHeaderHeight), cs.rect.Dy())
	head := image.Rect(cs.rect.Min.X, cs.rect.Min.Y, cs.rect.Max.X, cs.rect.Min.Y+h)
	g.drawHeaderBar(gtx, env, sc, Pos{Col: 1}, cs.ref, head, nil)
	body := image.Rect(cs.rect.Min.X, cs.rect.Min.Y+h, cs.rect.Max.X, cs.rect.Max.Y)
	if !body.Empty() {
		paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect(body).Op())
	}
}

// drawChrome draws a regular slot's header bar — icon and title, the drag
// handle it all rides on, the arrange and close buttons — runs the header's
// input, and opens the arrange popover from the arrange button's anchor.
func (g *slotGrid) drawChrome(gtx *layout.Context, env *kit.Env, sc *screens, p Pos, rect image.Rectangle) {
	if rect.Dx() < gtx.Dp(theme.ControlSM)*3 {
		return
	}
	tag := g.headerTagFor(p)
	g.processHeaderInput(gtx, tag, rect.Min, p)
	arrange := ensureIconButton(&g.arrangeBtns, p.key())
	closeBtn := ensureIconButton(&g.closeBtns, p.key())
	g.drawHeaderBar(gtx, env, sc, p, refAt(g.doc, p), rect, &headerButtons{arrange: arrange, close: closeBtn})
}

// headerButtons are the regular header's right-side controls; the main
// header passes nil and draws none (§5.3: no close, drag or arrange).
type headerButtons struct {
	arrange, close *kit.IconButtonState
}

// drawHeaderBar draws one header band: the raised bar over the whole
// rectangle — it reads as the drag handle — a hairline under it, and the
// icon + title centered with the buttons at the right.
func (g *slotGrid) drawHeaderBar(gtx *layout.Context, env *kit.Env, sc *screens, p Pos, ref SlotRef, rect image.Rectangle, btns *headerButtons) {
	h := rect.Dy()
	env.Offset(gtx, rect.Min, func(gtx *layout.Context) {
		bar := *gtx
		bar.Constraints = layout.Constraints{Min: image.Pt(rect.Dx(), h), Max: image.Pt(rect.Dx(), h)}
		paint.FillShape(bar.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: image.Pt(rect.Dx(), h)}.Op())
		padL := gtx.Dp(theme.SpaceMD)
		padR := gtx.Dp(theme.SpaceXS)
		innerW := max(rect.Dx()-padL-padR, 0)
		env.Offset(&bar, image.Pt(padL, 0), func(gtx *layout.Context) {
			local := *gtx
			local.Constraints = layout.Constraints{Min: image.Pt(innerW, h), Max: image.Pt(innerW, h)}
			g.layoutHeaderRow(env, &local, sc, p, ref, btns)
		})
		if btns != nil {
			btnW := gtx.Dp(theme.ControlSM)
			anchor := image.Rect(rect.Dx()-2*btnW-padR, 0, rect.Dx()-btnW-padR, h)
			g.layoutArrange(env, gtx, sc, p, anchor)
		}
	})
	// The hairline under the band separates it from the body.
	line := max(gtx.Dp(theme.Hairline), 1)
	paint.FillShape(gtx.Ops, env.Th.Chrome().BorderSubtle,
		clip.Rect(image.Rect(rect.Min.X, rect.Max.Y-line, rect.Max.X, rect.Max.Y)).Op())
}

// layoutHeaderRow lays out the header row's children using env.Offset so every
// IconButton's tooltip anchors at its exact window coordinates.
func (g *slotGrid) layoutHeaderRow(env *kit.Env, gtx *layout.Context, sc *screens, p Pos, ref SlotRef, btns *headerButtons) {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	iconPx := gtx.Dp(theme.IconSM)
	gap := gtx.Dp(theme.SpaceXS)
	x := 0
	env.Offset(gtx, image.Pt(x, max((h-iconPx)/2, 0)), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: image.Pt(iconPx, iconPx)}
		_, err := env.Icon(&local, slotIcon(ref.Kind), theme.IconSM, env.Th.Chrome().FgMuted)
		sc.fail(err)
	})
	x += iconPx + gap

	actions := slotHeaderActions(sc, ref)
	btnSize := gtx.Dp(theme.ControlSM)
	rightCount := len(actions)
	if btns != nil {
		rightCount += 2
	}
	rightW := rightCount * btnSize
	titleMaxW := max(w-x-rightW-gap, 0)
	titleH := gtx.Sp(theme.TypeSmall().Line)
	env.Offset(gtx, image.Pt(x, max((h-titleH)/2, 0)), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: image.Pt(titleMaxW, titleH)}
		kit.Label(env, &local, theme.TypeSmall(), env.Th.Chrome().Fg, slotTitle(sc, ref))
	})

	rx := w - rightW
	g.layoutHeaderButtons(env, gtx, sc, p, btns, actions, rx, h, btnSize)
}

func (g *slotGrid) layoutHeaderButtons(env *kit.Env, gtx *layout.Context, sc *screens, p Pos, btns *headerButtons, actions []slotAction, rx, h, btnSize int) {
	y := max((h-btnSize)/2, 0)
	for i := range actions {
		act := actions[i]
		btn := ensureIconButton(&g.actionBtns, p.key()+":act:"+act.props.Label)
		env.Offset(gtx, image.Pt(rx, y), func(gtx *layout.Context) {
			local := *gtx
			clicked, err := btn.Layout(env, &local, &act.props)
			sc.fail(err)
			if clicked && act.onClick != nil {
				act.onClick()
			}
		})
		rx += btnSize
	}
	if btns == nil {
		return
	}
	env.Offset(gtx, image.Pt(rx, y), func(gtx *layout.Context) {
		local := *gtx
		props := kit.IconButtonProps{Icon: kit.IconEllipsis, Label: labelArrange, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
		clicked, err := btns.arrange.Layout(env, &local, &props)
		sc.fail(err)
		if clicked {
			g.openArrange(p)
		}
	})
	rx += btnSize
	env.Offset(gtx, image.Pt(rx, y), func(gtx *layout.Context) {
		local := *gtx
		props := kit.IconButtonProps{Icon: kit.IconClose, Label: "Close slot", Variant: kit.IconButtonGhost, Size: kit.SizeSM}
		clicked, err := btns.close.Layout(env, &local, &props)
		sc.fail(err)
		if clicked {
			g.edited(CloseSlot(g.doc, p))
		}
	})
}

// ensureIconButton returns the named button, building it on first use.
func ensureIconButton(m *map[string]*kit.IconButtonState, key string) *kit.IconButtonState {
	if btn := (*m)[key]; btn != nil {
		return btn
	}
	btn := kit.NewIconButtonState()
	(*m)[key] = btn
	return btn
}

// processHeaderInput drives the drag from one header's pointer events
// (§5.3, DA-E09): a press arms, DragSlop of travel activates, a release
// drops or slides back, Escape cancels. Coordinates stay header-local; the
// header's origin turns them into grid coordinates.
func (g *slotGrid) processHeaderInput(gtx *layout.Context, tag *headerTag, origin image.Point, p Pos) {
	for {
		e, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			return
		}
		pe, is := e.(pointer.Event)
		if !is {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			g.beginHeaderDrag(pe, origin, p)
		case pointer.Drag:
			g.moveHeaderDrag(gtx, pe, p)
		case pointer.Release:
			g.endHeaderDrag(gtx, pe, p)
		case pointer.Cancel:
			if g.drag.pressed && g.drag.pos == p {
				g.cancelDrag()
			}
		case pointer.Enter, pointer.Leave, pointer.Move, pointer.Scroll:
		}
	}
}

// beginHeaderDrag arms a drag on a primary press of the slot's header.
func (g *slotGrid) beginHeaderDrag(pe pointer.Event, origin image.Point, p Pos) {
	if !pe.Buttons.Contain(pointer.ButtonPrimary) || g.drag.active || g.drag.armed {
		return
	}
	g.drag = dragState{
		armed: true, pressed: true, pos: p, origin: origin,
		pressAt: pe.Position.Round(), pointer: pe.Position.Round(),
	}
}

// moveHeaderDrag tracks an armed drag: the pointer follows without a spring
// (§5.3), and DragSlop of travel turns armed into active (DA-E09).
func (g *slotGrid) moveHeaderDrag(gtx *layout.Context, pe pointer.Event, p Pos) {
	if !g.drag.pressed || g.drag.pos != p {
		return
	}
	pos := pe.Position.Round()
	g.drag.pointer = pos
	if !g.drag.armed {
		return
	}
	d := pos.Sub(g.drag.pressAt)
	slop := gtx.Dp(theme.DragSlop)
	if absInt(d.X) > slop || absInt(d.Y) > slop {
		g.drag.active, g.drag.armed = true, false
	}
}

// endHeaderDrag drops an active drag under the pointer, or clears an armed
// one that never left the header.
func (g *slotGrid) endHeaderDrag(gtx *layout.Context, pe pointer.Event, p Pos) {
	if !g.drag.pressed || g.drag.pos != p {
		return
	}
	if !g.drag.active {
		g.drag = dragState{}
		return
	}
	g.drag.pointer = pe.Position.Round()
	g.finishDrag(gtx)
}

// absInt is the integer absolute value.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// sumDp adds dp sizes.
func sumDp(vals []unit.Dp) unit.Dp {
	var total unit.Dp
	for _, v := range vals {
		total += v
	}
	return total
}

// colMins lists the visible columns' minimum widths from first to visible.
func colMins(first, visible int) []unit.Dp {
	out := make([]unit.Dp, 0, max(visible-first, 0))
	for i := first; i < visible; i++ {
		out = append(out, colMin(i))
	}
	return out
}

// colMin is one column's minimum width: the main column keeps the main
// slot's minimum, the rest the regular one (§5.3).
func colMin(i int) unit.Dp {
	if i == 0 {
		return theme.MainSlotMinWidth
	}
	return theme.SlotMinWidth
}

// lerpRect interpolates two rectangles.
func lerpRect(a, b image.Rectangle, t float32) image.Rectangle {
	f := func(x, y int) int { return x + int(float32(y-x)*t+0.5) }
	return image.Rect(f(a.Min.X, b.Min.X), f(a.Min.Y, b.Min.Y), f(a.Max.X, b.Max.X), f(a.Max.Y, b.Max.Y))
}
