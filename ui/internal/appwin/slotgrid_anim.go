package appwin

import (
	"image"

	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// armClosingAnims starts an Exit fade for every regular slot removed by doc,
// preserving its current drawn rect and opacity if it was mid-enter.
func (g *slotGrid) armClosingAnims(doc LayoutDoc) {
	sameShape := len(doc.Columns) == len(g.doc.Columns)
	for _, oldP := range allCells(g.doc) {
		g.armClosingCell(doc, oldP, sameShape)
	}
}

func (g *slotGrid) armClosingCell(doc LayoutDoc, oldP Pos, sameShape bool) {
	if isMainPos(oldP) {
		return
	}
	oldRef := refAt(g.doc, oldP)
	if oldRef.Kind == "" {
		return
	}
	if _, still := findCell(doc, oldRef); still {
		return
	}
	if sameShape && len(doc.Columns[oldP.Col].Slots) == len(g.doc.Columns[oldP.Col].Slots) {
		if newSlot, ok := slotAt(doc, oldP); ok && newSlot.Ref.Kind == oldRef.Kind {
			return
		}
	}
	cur := g.currentSlotRect(oldP, oldRef)
	if cur.Empty() {
		return
	}
	var pres *motion.Presence
	if anim := g.animOf(oldRef); anim != nil && anim.entering {
		p := anim.presence
		pres = &p
	}
	delete(g.anims, keyOfRef(oldRef))
	g.startClosing(oldP, oldRef, cur, pres)
}

// armAnims arms Smooth slide animations for existing slots that moved or
// resized, and enter animations (size from 0 at the slot's edge plus fade-in
// on Smooth) for newly opened slots (§5.3, §3.7).
func (g *slotGrid) armAnims(doc LayoutDoc) {
	if len(g.rects) == 0 {
		clear(g.anims)
		return
	}
	colsChanged := len(doc.Columns) != len(g.doc.Columns)
	keep := make(map[string]bool, len(doc.Columns)*maxRowsPerColumn)
	for _, p := range allCells(doc) {
		ref := refAt(doc, p)
		if ref.Kind == "" {
			continue
		}
		key := keyOfRef(ref)
		keep[key] = true
		g.armCellAnim(doc, p, ref, key, colsChanged)
	}
	for key := range g.anims {
		if !keep[key] {
			delete(g.anims, key)
		}
	}
}

func (g *slotGrid) armCellAnim(doc LayoutDoc, p Pos, ref SlotRef, key string, colsChanged bool) {
	if old, found := findCell(g.doc, ref); found {
		g.armExistingCellAnim(doc, old, p, ref, key, colsChanged)
		return
	}
	g.armNewCellAnim(doc, p, ref, key, colsChanged)
}

func (g *slotGrid) armExistingCellAnim(doc LayoutDoc, old, p Pos, ref SlotRef, key string, colsChanged bool) {
	rowChanged := p.Col < len(g.doc.Columns) && len(doc.Columns[p.Col].Slots) != len(g.doc.Columns[p.Col].Slots)
	if old == p && !colsChanged && !rowChanged && g.animOf(ref) == nil {
		return
	}
	if cur := g.currentSlotRect(old, ref); !cur.Empty() {
		g.startSlideAnim(key, cur)
	}
}

func (g *slotGrid) armNewCellAnim(doc LayoutDoc, p Pos, ref SlotRef, key string, colsChanged bool) {
	if isMainPos(p) {
		oldMain := refAt(g.doc, p)
		if cur := g.currentSlotRect(p, oldMain); !cur.Empty() && (colsChanged || g.animOf(oldMain) != nil) {
			g.startSlideAnim(key, cur)
		}
		return
	}
	if cs := g.takeClosing(ref); cs != nil {
		g.startReenterAnim(key, cs)
		return
	}
	if g.isInPlaceReplacement(doc, p, ref, colsChanged) {
		oldRef := refAt(g.doc, p)
		if cur := g.currentSlotRect(p, oldRef); !cur.Empty() && g.animOf(oldRef) != nil {
			g.startSlideAnim(key, cur)
		}
		return
	}
	if from, ok := g.enterFromRect(doc, p); ok {
		g.startEnterAnim(key, from)
	}
}

func (g *slotGrid) isInPlaceReplacement(doc LayoutDoc, p Pos, ref SlotRef, colsChanged bool) bool {
	if colsChanged {
		return false
	}
	oldSlot, existed := slotAt(g.doc, p)
	return existed && len(doc.Columns[p.Col].Slots) == len(g.doc.Columns[p.Col].Slots) && oldSlot.Ref.Kind == ref.Kind
}

// currentSlotRect returns the rect a cell last drew at, interpolating any
// active slide or enter animation so mid-flight reversals never jump.
func (g *slotGrid) currentSlotRect(p Pos, ref SlotRef) image.Rectangle {
	rect := g.rects[p.key()]
	if rect.Empty() {
		return image.Rectangle{}
	}
	if anim := g.animOf(ref); anim != nil {
		return lerpRect(anim.from, rect, anim.spring.Value)
	}
	return rect
}

// enterFromRect computes the collapsed edge rect a newly opened regular slot
// grows from on Smooth: right edge for a new column, top or bottom edge for an
// inserted row inside an existing column.
func (g *slotGrid) enterFromRect(doc LayoutDoc, p Pos) (image.Rectangle, bool) {
	if len(g.doc.Columns) == 0 || g.lastArea.Empty() {
		return image.Rectangle{}, false
	}
	if p.Col >= len(g.doc.Columns) {
		lastCol := len(g.doc.Columns) - 1
		lastRect := g.currentSlotRect(Pos{Col: lastCol, Row: 0}, refAt(g.doc, Pos{Col: lastCol, Row: 0}))
		if lastRect.Empty() {
			return image.Rectangle{}, false
		}
		return image.Rect(lastRect.Max.X, g.lastArea.Min.Y, lastRect.Max.X, g.lastArea.Max.Y), true
	}
	colRect := g.currentSlotRect(Pos{Col: p.Col, Row: 0}, refAt(g.doc, Pos{Col: p.Col, Row: 0}))
	if colRect.Empty() {
		return image.Rectangle{}, false
	}
	if len(doc.Columns) > len(g.doc.Columns) {
		return image.Rect(colRect.Min.X, g.lastArea.Min.Y, colRect.Min.X, g.lastArea.Max.Y), true
	}
	if p.Row == 0 {
		return image.Rect(colRect.Min.X, colRect.Min.Y, colRect.Max.X, colRect.Min.Y), true
	}
	return image.Rect(colRect.Min.X, colRect.Max.Y, colRect.Max.X, colRect.Max.Y), true
}

// startAnim arms a Smooth slide of refKey from from to wherever it lays out
// next (§5.3, §3.7).
func (g *slotGrid) startAnim(refKey string, from image.Rectangle) {
	g.startSlideAnim(refKey, from)
}

func (g *slotGrid) startSlideAnim(refKey string, from image.Rectangle) {
	if from.Empty() {
		return
	}
	s := motion.NewSpringSpec(0, motion.Smooth())
	s.Target = 1
	if existing := g.anims[refKey]; existing != nil && existing.entering {
		existing.from = from
		existing.spring = s
		return
	}
	g.anims[refKey] = &slotAnim{from: from, spring: s}
}

func (g *slotGrid) startEnterAnim(refKey string, from image.Rectangle) {
	p := motion.NewPresence(motion.Smooth(), motion.Exit())
	p.Show(true)
	s := motion.NewSpringSpec(0, motion.Smooth())
	s.Target = 1
	g.anims[refKey] = &slotAnim{from: from, spring: s, entering: true, presence: p}
}

func (g *slotGrid) startReenterAnim(refKey string, cs *closingSlot) {
	p := cs.presence
	p.Show(true)
	s := motion.NewSpringSpec(0, motion.Smooth())
	s.Target = 1
	g.anims[refKey] = &slotAnim{from: cs.rect, spring: s, entering: true, presence: p}
}

func (g *slotGrid) startClosing(pos Pos, ref SlotRef, rect image.Rectangle, existing *motion.Presence) {
	var p motion.Presence
	if existing != nil {
		p = *existing
	} else {
		p = motion.NewPresence(motion.Smooth(), motion.Exit())
		p.Snap(true)
	}
	p.Show(false)
	key := keyOfRef(ref)
	for i, cs := range g.closing {
		if keyOfRef(cs.ref) == key {
			g.closing[i] = &closingSlot{pos: pos, ref: ref, rect: rect, presence: p}
			return
		}
	}
	g.closing = append(g.closing, &closingSlot{pos: pos, ref: ref, rect: rect, presence: p})
}

func (g *slotGrid) takeClosing(ref SlotRef) *closingSlot {
	key := keyOfRef(ref)
	for i, cs := range g.closing {
		if keyOfRef(cs.ref) == key {
			g.closing = append(g.closing[:i], g.closing[i+1:]...)
			return cs
		}
	}
	return nil
}

// advanceAnims steps every animation and drops the finished ones.
func (g *slotGrid) advanceAnims(env *kit.Env) {
	if env.ReducedMotion {
		clear(g.anims)
		g.closing = nil
		return
	}
	dt := env.DT()
	for key, anim := range g.anims {
		anim.spring.Advance(dt)
		presMoving := false
		if anim.entering {
			presMoving = anim.presence.Advance(dt)
		}
		if anim.spring.AtRest() && !presMoving {
			delete(g.anims, key)
			continue
		}
		env.Keep()
	}
	kept := g.closing[:0]
	for _, cs := range g.closing {
		if cs.presence.Advance(dt) {
			env.Keep()
		}
		if cs.presence.Visible() {
			kept = append(kept, cs)
		}
	}
	g.closing = kept
}

// animOf returns the in-flight animation of a ref, or nil.
func (g *slotGrid) animOf(ref SlotRef) *slotAnim {
	return g.anims[keyOfRef(ref)]
}

// drawnCellRect returns the animated rect for p using this frame's target geom.
func (g *slotGrid) drawnCellRect(geom []cellGeom, p Pos) image.Rectangle {
	rect := g.rectOf(geom, p)
	if rect.Empty() {
		return image.Rectangle{}
	}
	if anim := g.animOf(refAt(g.doc, p)); anim != nil {
		return lerpRect(anim.from, rect, anim.spring.Value)
	}
	return rect
}

// keyOfRef names a ref for the animation table: kind and id, the identity a
// moving slot keeps.
func keyOfRef(ref SlotRef) string { return string(ref.Kind) + ":" + ref.ID }

func (g *slotGrid) drawAnimatedDividers(gtx *layout.Context, env *kit.Env, geom []cellGeom, area image.Rectangle, visible int) {
	line := max(gtx.Dp(theme.Hairline), 1)
	col := env.Th.Chrome().BorderSubtle
	for i := range visible - 1 {
		left := g.drawnCellRect(geom, Pos{Col: i, Row: 0})
		if !left.Empty() && left.Max.X < area.Max.X {
			paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(left.Max.X, area.Min.Y, left.Max.X+line, area.Max.Y)).Op())
		}
	}
	for c := range visible {
		if len(g.doc.Columns[c].Slots) < maxRowsPerColumn {
			continue
		}
		top := g.drawnCellRect(geom, Pos{Col: c, Row: 0})
		if !top.Empty() {
			paint.FillShape(gtx.Ops, col, clip.Rect(image.Rect(top.Min.X, top.Max.Y, top.Max.X, top.Max.Y+line)).Op())
		}
	}
}

// drawClosingSlot paints a closing slot's header and body fading out together
// on Exit above its sliding neighbours, without taking or blocking pointer input.
func (g *slotGrid) drawClosingSlot(gtx *layout.Context, env *kit.Env, sc *screens, cs *closingSlot) {
	if cs.rect.Empty() {
		return
	}
	alpha := min(max(cs.presence.Progress(), 0), 1)
	if alpha <= 0 {
		return
	}
	disabled := gtx.Disabled()
	defer pointer.PassOp{}.Push(disabled.Ops).Pop()
	defer clip.Rect(cs.rect).Push(disabled.Ops).Pop()
	defer paint.PushOpacity(disabled.Ops, alpha).Pop()
	h := min(disabled.Dp(theme.SlotHeaderHeight), cs.rect.Dy())
	head := image.Rect(cs.rect.Min.X, cs.rect.Min.Y, cs.rect.Max.X, cs.rect.Min.Y+h)
	if cs.rect.Dx() >= disabled.Dp(theme.ControlSM)*3 {
		g.drawHeaderBar(&disabled, env, sc, cs.pos, cs.ref, head, nil)
	} else if !head.Empty() {
		paint.FillShape(disabled.Ops, env.Th.Chrome().CanvasBg, clip.Rect(head).Op())
	}
	body := image.Rect(cs.rect.Min.X, cs.rect.Min.Y+h, cs.rect.Max.X, cs.rect.Max.Y)
	if !body.Empty() {
		paint.FillShape(disabled.Ops, env.Th.Chrome().CanvasBg, clip.Rect(body).Op())
		env.Offset(&disabled, body.Min, func(gtx *layout.Context) {
			local := *gtx
			local.Constraints = layout.Constraints{Max: body.Size()}
			defer clip.Rect{Max: body.Size()}.Push(local.Ops).Pop()
			sc.layoutSlotBody(&local, env, cs.pos, cs.ref)
		})
	}
}
