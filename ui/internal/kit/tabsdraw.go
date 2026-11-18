package kit

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func (s *TabsState) layoutGeometry(env *Env, gtx *layout.Context, pr TabsProps, maxW, h int) {
	pad := 0
	if pr.Variant == TabsMode {
		pad = gtx.Dp(theme.SpaceXXS)
	}
	tabGap := gtx.Dp(theme.SpaceXXS)
	x := pad
	totalW := pad
	for i, it := range pr.Items {
		sl := s.slots[it.ID]
		w := s.measureTabWidth(env, gtx, pr.Variant, it)
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
	totalW += pad

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
		sl.rect = image.Rect(drawX, pad, drawX+aw, h-pad)
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

func (s *TabsState) measureTabWidth(env *Env, gtx *layout.Context, v TabsVariant, it TabItem) int {
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
	if v == TabsDocument && (it.Closable || it.Dirty) {
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
	c := env.Th.Chrome()
	stripR := s.stripRect
	focused := env.FocusVisible(gtx, s.Tag()) || env.previewFocus(s.Tag())
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
	defer clip.Rect{Max: stripR.Size()}.Push(gtx.Ops).Pop()

	s.drawInactiveHovers(env, gtx, pr, stripR.Max.X, rad)

	// Shared Smooth sliding pill for both document and mode variants (§6.22).
	s.indicator.drawBordered(gtx, rad, c.SurfaceRaised, c.BorderSubtle)

	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		if sl == nil || !sl.visible {
			continue
		}
		if err := s.drawTabItem(env, gtx, pr, it, sl, stripR.Max.X); err != nil {
			return err
		}
	}
	if focus > 0 {
		insetFocusRing(gtx, stripR, rad, c.FocusBorder, focus)
	}
	return nil
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
	foc := env.previewFocus(&sl.tag) || (active && env.FocusVisible(gtx, s.Tag()))
	dragging := (s.dragActive && s.dragID == it.ID) || pr.DraggingID == it.ID

	alpha := tabEdgeAlpha(sl.rect, stripMaxX)
	if dragging {
		fillLayer(gtx, sl.rect, gtx.Dp(theme.RadiusMD), c.SurfaceRaised, alpha)
		strokeRRect(gtx.Ops, sl.rect, gtx.Dp(theme.RadiusMD), float32(gtx.Dp(theme.Hairline)), fade(c.FocusBorder, alpha))
	}
	if env.previewFocus(&sl.tag) {
		insetFocusRing(gtx, sl.rect, gtx.Dp(theme.RadiusMD), c.FocusBorder, alpha)
	}

	ink := fade(tabInk(&c, active, it.Disabled), alpha)
	if pr.Variant == TabsDocument && it.Pinned {
		return s.drawPinnedTab(env, gtx, it, sl, ink, hov, foc)
	}
	return s.drawRegularTab(env, gtx, pr.Variant, it, sl, ink, active, hov, foc, alpha, stripMaxX)
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

func (s *TabsState) drawRegularTab(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, sl *tabSlot, ink color.NRGBA, active, hov, foc bool, alpha float32, stripMaxX int) error {
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
	hasCloseSlot := v == TabsDocument && (it.Closable || it.Dirty)
	if hasCloseSlot {
		right -= gtx.Dp(theme.TabCloseHit) + gap
	}
	if it.Label != "" {
		ly := sl.rect.Min.Y + centerLabelY(env, gtx, theme.TypeLabel(), sl.rect.Dy())
		if maxLblW := right - x; maxLblW > 0 {
			drawRunAt(env, gtx, image.Pt(x, ly), maxLblW, sl.rect.Dy(), theme.TypeLabel(), ink, it.Label, true)
		}
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
