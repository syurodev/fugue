package kit

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// StripRect returns the visible tab-strip clip rectangle, for bounds tests.
func (s *TabsState) StripRect() image.Rectangle { return s.stripRect }

// DraggedDeferred reports whether the dragged tab was deferred above the strip.
func (s *TabsState) DraggedDeferred() bool { return s.dragDefer }

func (s *TabsState) tabFocusTag(v TabsVariant, id string) event.Tag {
	if v == TabsDocument {
		if sl := s.slots[id]; sl != nil {
			return &sl.tag
		}
	}
	return s.Tag()
}

func (s *TabsState) activeDragID(pr TabsProps) string {
	if s.dragActive && s.dragID != "" {
		return s.dragID
	}
	return pr.DraggingID
}

func (s *TabsState) layoutGeometry(env *Env, gtx *layout.Context, pr TabsProps, maxW, h int) {
	padX := gtx.Dp(theme.FocusOffset) + gtx.Dp(theme.FocusStroke)
	padY := 0
	if pr.Variant == TabsMode {
		padX = gtx.Dp(theme.SpaceXXS)
		padY = gtx.Dp(theme.SpaceXXS)
	}
	tabGap := gtx.Dp(theme.SpaceXXS)
	dragID := s.activeDragID(pr)
	x := padX
	totalW := padX
	for i, it := range pr.Items {
		sl := s.slots[it.ID]
		w := s.measureTabWidth(env, gtx, pr.Variant, it, it.ID == dragID)
		p := stepPresence(env, &sl.presence)
		animW := int(float32(w)*unit01(p) + 0.5)
		if i > 0 {
			x += tabGap
		}
		sl.targetX = x
		sl.targetW = animW
		x += animW
		totalW = x
	}
	totalW += padX

	overflowW := s.measureOverflowWidth(env, gtx, pr, totalW, maxW)
	stripW := max(maxW-overflowW, 0)
	s.stripRect = image.Rect(0, 0, stripW, h)
	maxScroll := max(totalW-stripW, 0)
	s.scrollX = min(max(s.scrollX, 0), maxScroll)

	s.hiddenIDs = s.hiddenIDs[:0]
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		ax := int(followSmooth(env, &sl.x, float32(sl.targetX)) + 0.5)
		aw := int(followSmooth(env, &sl.width, float32(sl.targetW)) + 0.5)
		drawX := ax - s.scrollX
		sl.rect = image.Rect(drawX, padY, drawX+aw, h-padY)
		sl.visible = sl.rect.Max.X > 0 && sl.rect.Min.X < stripW
		if sl.rect.Min.X < 0 || sl.rect.Max.X > stripW {
			s.hiddenIDs = append(s.hiddenIDs, it.ID)
		}
	}
}

func (s *TabsState) measureOverflowWidth(env *Env, gtx *layout.Context, pr TabsProps, totalW, maxW int) int {
	if totalW <= maxW {
		return 0
	}
	estHidden := max(len(pr.Items)/2, 1)
	ovPr := ButtonProps{Text: strconv.Itoa(estHidden), Icon: IconChevronDown, Variant: Ghost, Size: SizeSM}
	return s.overflowBtn.Measure(env, gtx, ovPr).Size.X + gtx.Dp(theme.SpaceXS)
}

func (s *TabsState) measureTabWidth(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, dragging bool) int {
	padX := gtx.Dp(theme.SpaceMD)
	gap := gtx.Dp(theme.SpaceXS)
	w := 2 * padX
	if it.Pinned && v == TabsDocument {
		w += gtx.Dp(theme.IconSM)
		return max(w, gtx.Dp(theme.HitMin))
	}
	if it.Icon != "" {
		w += gtx.Dp(theme.IconSM) + gap
	}
	if it.Label != "" {
		w += measureLabel(env, gtx, theme.TypeLabel(), it.Label).X
	}
	if v == TabsDocument && !dragging && (it.Closable || it.Dirty) {
		w += gap + gtx.Dp(theme.TabCloseHit)
	}
	return max(w, gtx.Dp(theme.HitMin))
}

func (s *TabsState) stepIndicator(env *Env, pr TabsProps) {
	sl := s.slots[pr.ActiveID]
	if sl == nil {
		s.indicator.step(env, image.Rectangle{}, false)
		return
	}
	s.indicator.step(env, sl.rect, true)
}

func (s *TabsState) draw(env *Env, gtx *layout.Context, pr TabsProps, h int, ev *TabsEvents) error {
	s.focusRingR = image.Rectangle{}
	s.dragDefer = false
	c := env.Th.Chrome()
	stripR := s.stripRect
	focused := pr.Variant == TabsMode && (env.FocusVisible(gtx, s.Tag()) || env.previewFocus(s.Tag()))
	focus := followState(env, &s.focusF, focused)

	func() {
		defer clip.Rect{Max: stripR.Size()}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, s.Tag())
	}()

	if pr.Variant == TabsMode {
		fillLayer(gtx, stripR, gtx.Dp(theme.RadiusMD), c.FillSoft, 1)
	}

	if err := s.drawStripTabs(env, gtx, pr, stripR, focus); err != nil {
		return err
	}

	if len(s.hiddenIDs) > 0 {
		if err := s.drawOverflow(env, gtx, pr, stripR.Max.X, h, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *TabsState) drawStripTabs(env *Env, gtx *layout.Context, pr TabsProps, stripR image.Rectangle, focus float32) error {
	c := env.Th.Chrome()
	rad := gtx.Dp(theme.RadiusMD)
	dragID := s.activeDragID(pr)
	var dragItem TabItem
	var dragSlot *tabSlot

	func() {
		defer clip.Rect{Max: stripR.Size()}.Push(gtx.Ops).Pop()
		s.drawInactiveHovers(env, gtx, pr, stripR.Max.X, rad)
		s.indicator.drawBordered(gtx, rad, c.SurfaceRaised, c.BorderSubtle)
		dragItem, dragSlot = s.drawClippedTabs(env, gtx, pr, dragID, stripR.Max.X)
		if pr.Variant == TabsMode && focus > 0 {
			s.focusRingR = stripR
			insetFocusRing(gtx, stripR, rad, c.FocusBorder, focus)
		}
	}()

	if dragSlot != nil {
		return s.deferDraggedTab(env, gtx, pr, dragItem, dragSlot, stripR, rad)
	}
	return nil
}

func (s *TabsState) drawClippedTabs(env *Env, gtx *layout.Context, pr TabsProps, dragID string, stripMaxX int) (TabItem, *tabSlot) {
	var dragItem TabItem
	var dragSlot *tabSlot
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		if sl == nil || !sl.visible {
			continue
		}
		if it.ID == dragID {
			dragItem = it
			dragSlot = sl
			continue
		}
		if err := s.drawTabItem(env, gtx, pr, it, sl, stripMaxX); err != nil {
			return TabItem{}, nil
		}
	}
	return dragItem, dragSlot
}

func (s *TabsState) deferDraggedTab(env *Env, gtx *layout.Context, pr TabsProps, it TabItem, sl *tabSlot, stripR image.Rectangle, rad int) error {
	c := env.Th.Chrome()
	r := sl.rect
	if s.dragActive && s.dragID == it.ID {
		dx := s.dragPtrX - s.dragStartX
		x := min(max(r.Min.X+dx, stripR.Min.X), max(stripR.Max.X-r.Dx(), stripR.Min.X))
		r = image.Rect(x, r.Min.Y, x+r.Dx(), r.Max.Y)
	}
	s.dragDefer = true
	m := op.Record(gtx.Ops)
	DrawFloatShadow(gtx, r, rad)
	fillLayer(gtx, r, rad, c.SurfaceRaised, 1)
	strokeRRect(gtx.Ops, r, rad, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)
	saved := sl.rect
	sl.rect = r
	registerDocTabArea(gtx, pr.Variant, it.Disabled, sl)
	err := s.drawRegularTab(env, gtx, pr.Variant, it, sl, c.Fg, true, false, false, 1, stripR.Max.X, true)
	sl.rect = saved
	op.Defer(gtx.Ops, m.Stop())
	return err
}

func (s *TabsState) drawInactiveHovers(env *Env, gtx *layout.Context, pr TabsProps, stripMaxX, rad int) {
	c := env.Th.Chrome()
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		if sl == nil || !sl.visible || it.ID == pr.ActiveID || it.Disabled {
			continue
		}
		if s.hoverID == it.ID || env.previewHover(&sl.tag) {
			fillLayer(gtx, sl.rect, rad, c.FillSoft, tabEdgeAlpha(sl.rect, stripMaxX))
		}
	}
}

func (s *TabsState) drawTabItem(env *Env, gtx *layout.Context, pr TabsProps, it TabItem, sl *tabSlot, stripMaxX int) error {
	c := env.Th.Chrome()
	active := it.ID == pr.ActiveID
	hov := s.hoverID == it.ID || env.previewHover(&sl.tag)
	tabFoc, foc := tabFocused(env, gtx, pr.Variant, it.Disabled, sl, s.Tag(), active)
	dragging := (s.dragActive && s.dragID == it.ID) || pr.DraggingID == it.ID

	registerDocTabArea(gtx, pr.Variant, it.Disabled, sl)

	alpha := tabEdgeAlpha(sl.rect, stripMaxX)
	if dragging {
		fillLayer(gtx, sl.rect, gtx.Dp(theme.RadiusMD), c.SurfaceRaised, alpha)
		strokeRRect(gtx.Ops, sl.rect, gtx.Dp(theme.RadiusMD), float32(gtx.Dp(theme.Hairline)), fade(c.FocusBorder, alpha))
	}
	if tabFoc {
		s.focusRingR = sl.rect
		insetFocusRing(gtx, sl.rect, gtx.Dp(theme.RadiusMD), c.FocusBorder, alpha)
	}

	ink := fade(tabInk(&c, active, it.Disabled), alpha)
	if pr.Variant == TabsDocument && it.Pinned {
		return s.drawPinnedTab(env, gtx, it, sl, ink, hov, foc)
	}
	return s.drawRegularTab(env, gtx, pr.Variant, it, sl, ink, active, hov, foc, alpha, stripMaxX, false)
}

func tabFocused(env *Env, gtx *layout.Context, v TabsVariant, disabled bool, sl *tabSlot, stripTag event.Tag, active bool) (tabFoc, foc bool) {
	if v == TabsDocument {
		tabFoc = !disabled && (env.FocusVisible(gtx, &sl.tag) || env.previewFocus(&sl.tag))
		return tabFoc, tabFoc
	}
	return false, active && env.FocusVisible(gtx, stripTag)
}

func registerDocTabArea(gtx *layout.Context, v TabsVariant, disabled bool, sl *tabSlot) {
	if v != TabsDocument || disabled {
		return
	}
	defer clip.Rect(sl.rect).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &sl.tag)
}

func (s *TabsState) drawPinnedTab(env *Env, gtx *layout.Context, it TabItem, sl *tabSlot, ink color.NRGBA, hov, foc bool) error {
	sl.closeRect = image.Rectangle{}
	ic := it.Icon
	if ic == "" {
		ic = IconPin
	}
	iconPx := gtx.Dp(theme.IconSM)
	ix := sl.rect.Min.X + (sl.rect.Dx()-iconPx)/2
	iy := sl.rect.Min.Y + (sl.rect.Dy()-iconPx)/2
	if err := drawIconAt(env, gtx, image.Pt(ix, iy), ic, theme.IconSM, ink); err != nil {
		return err
	}
	if !it.Disabled && env.Preview == (Preview{}) && it.Label != "" {
		env.TooltipAnchor(&sl.tag, sl.rect, hov, foc, false, image.Point{}, TooltipProps{Text: it.Label})
	}
	return nil
}

func (s *TabsState) drawRegularTab(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, sl *tabSlot, ink color.NRGBA, active, hov, foc bool, alpha float32, stripMaxX int, dragging bool) error {
	x := sl.rect.Min.X + gtx.Dp(theme.SpaceMD)
	gap := gtx.Dp(theme.SpaceXS)
	if it.Icon != "" {
		iconPx := gtx.Dp(theme.IconSM)
		iy := sl.rect.Min.Y + (sl.rect.Dy()-iconPx)/2
		if err := drawIconAt(env, gtx, image.Pt(x, iy), it.Icon, theme.IconSM, ink); err != nil {
			return err
		}
		x += iconPx + gap
	}
	right := min(sl.rect.Max.X, stripMaxX) - gtx.Dp(theme.SpaceMD)
	if dragging {
		right = sl.rect.Max.X - gtx.Dp(theme.SpaceMD)
	}
	hasCloseSlot := v == TabsDocument && !dragging && (it.Closable || it.Dirty)
	if hasCloseSlot {
		right -= gtx.Dp(theme.TabCloseHit) + gap
	}
	if it.Label != "" {
		ly := sl.rect.Min.Y + centerLabelY(env, gtx, theme.TypeLabel(), sl.rect.Dy())
		if maxLblW := right - x; maxLblW > 0 {
			drawRunAt(env, gtx, image.Pt(x, ly), maxLblW, sl.rect.Dy(), theme.TypeLabel(), ink, it.Label, true)
		}
	}
	if dragging {
		sl.closeRect = image.Rectangle{}
		return nil
	}
	return s.drawCloseSlot(env, gtx, v, it, sl, active, hov, foc, alpha)
}

func (s *TabsState) drawCloseSlot(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, sl *tabSlot, active, hov, foc bool, alpha float32) error {
	if v != TabsDocument || (!it.Closable && !it.Dirty) {
		sl.closeRect = image.Rectangle{}
		return nil
	}
	hitSz := gtx.Dp(theme.TabCloseHit)
	cx := sl.rect.Max.X - gtx.Dp(theme.SpaceMD) - hitSz
	cy := sl.rect.Min.Y + (sl.rect.Dy()-hitSz)/2
	sl.closeRect = image.Rect(cx, cy, cx+hitSz, cy+hitSz)
	closable := it.Closable && !it.Disabled
	if !closable {
		sl.closeRect = image.Rectangle{}
	}

	closeA := unit01(followState(env, &sl.closeF, closable && (active || hov || foc))) * alpha
	c := env.Th.Chrome()

	if it.Dirty && closeA < alpha {
		dotD := gtx.Dp(theme.SpaceSM)
		dx := cx + (hitSz-dotD)/2
		dy := cy + (hitSz-dotD)/2
		fillLayer(gtx, image.Rect(dx, dy, dx+dotD, dy+dotD), dotD/2, c.Action, alpha-closeA)
	}
	if closeA > 0 {
		iconPx := gtx.Dp(theme.IconXS)
		return drawIconAt(env, gtx, image.Pt(cx+(hitSz-iconPx)/2, cy+(hitSz-iconPx)/2), IconClose, theme.IconXS, fade(c.FgMuted, closeA))
	}
	return nil
}

func (s *TabsState) drawOverflow(env *Env, gtx *layout.Context, pr TabsProps, startX, h int, ev *TabsEvents) error {
	ovPr := ButtonProps{
		Text:    strconv.Itoa(len(s.hiddenIDs)),
		Icon:    IconChevronDown,
		Variant: Ghost,
		Size:    SizeSM,
	}
	ovSize := s.overflowBtn.Measure(env, gtx, ovPr).Size
	bx := startX + gtx.Dp(theme.SpaceXS)
	by := (h - ovSize.Y) / 2
	var clicked bool
	var oerr error
	at(gtx, image.Pt(bx, by), func(gtx *layout.Context) {
		clicked, oerr = s.overflowBtn.Layout(env, gtx, ovPr)
	})
	if oerr != nil {
		return oerr
	}
	if clicked {
		if s.overflowBtn.KeyClicked() {
			s.overflow.OpenKeyboard()
		} else {
			s.overflow.Open()
		}
	}

	menuItems := make([]MenuItem, 0, len(s.hiddenIDs))
	for _, id := range s.hiddenIDs {
		for _, it := range pr.Items {
			if it.ID == id {
				menuItems = append(menuItems, MenuItem{
					ID:       it.ID,
					Label:    it.Label,
					Icon:     it.Icon,
					Disabled: it.Disabled,
				})
				break
			}
		}
	}
	res, err := s.overflow.Layout(env, gtx, MenuProps{
		Anchor: image.Rect(bx, by, bx+ovSize.X, by+ovSize.Y),
		Items:  menuItems,
	})
	if err != nil {
		return err
	}
	if res.Chosen != "" {
		ev.Selected = res.Chosen
		s.touchMRU(res.Chosen)
		s.ensureVisible(res.Chosen)
	}
	return nil
}

func tabEdgeAlpha(r image.Rectangle, stripMaxX int) float32 {
	if r.Dx() <= 0 {
		return 0
	}
	vis := min(r.Max.X, stripMaxX) - max(r.Min.X, 0)
	if vis <= 0 {
		return 0
	}
	if vis >= r.Dx() {
		return 1
	}
	return float32(vis) / float32(r.Dx())
}

func tabInk(c *theme.Chrome, active, disabled bool) color.NRGBA {
	if disabled {
		return c.FgDisabled
	}
	if active {
		return c.Fg
	}
	return c.FgMuted
}

func (s *TabsState) docFilters(pr TabsProps) []event.Filter {
	var filters []event.Filter
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		if sl == nil || it.Disabled {
			continue
		}
		filters = append(filters, key.FocusFilter{Target: &sl.tag})
		if !pr.GlobalShortcuts {
			filters = append(filters, key.Filter{Focus: &sl.tag, Name: key.NameTab, Required: key.ModCtrl, Optional: key.ModShift})
		}
	}
	if pr.GlobalShortcuts {
		filters = append(filters, key.Filter{Name: key.NameTab, Required: key.ModCtrl, Optional: key.ModShift})
	}
	return filters
}
