package kit

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func (s *TabsState) layoutGeometry(env *Env, gtx *layout.Context, pr TabsProps, maxW, h int) {
	pad := gtx.Dp(theme.SpaceXXS)
	if pr.Variant == TabsDocument {
		pad = 0
	}
	x := pad
	totalW := pad
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		w := s.measureTabWidth(env, gtx, pr.Variant, it)
		p := stepPresence(env, &sl.presence)
		animW := int(float32(w)*unit01(p) + 0.5)
		sl.targetX = x
		sl.targetW = animW
		x += animW
		totalW = x
	}
	totalW += pad

	overflowW := 0
	hasOverflow := totalW > maxW
	if hasOverflow {
		ovPr := IconButtonProps{Icon: IconChevronDown, Label: "Hidden tabs", Variant: IconButtonGhost}
		overflowW = s.overflowBtn.Measure(env, gtx, &ovPr).Size.X + gtx.Dp(theme.SpaceXS)
	}
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

func (s *TabsState) measureTabWidth(env *Env, gtx *layout.Context, v TabsVariant, it TabItem) int {
	padX := gtx.Dp(theme.SpaceLG)
	gap := gtx.Dp(theme.SpaceXS)
	w := 2 * padX
	if it.Pinned && v == TabsDocument {
		w += gtx.Dp(theme.IconSM)
		if it.Dirty {
			w += gtx.Dp(theme.SpaceSM) + gap
		}
		return max(w, gtx.Dp(theme.HitMin))
	}
	if it.Icon != "" {
		w += gtx.Dp(theme.IconSM) + gap
	}
	if it.Dirty && v == TabsDocument {
		w += gtx.Dp(theme.SpaceSM) + gap
	}
	if it.Label != "" {
		w += measureLabel(env, gtx, theme.TypeLabel(), it.Label).X
	}
	if v == TabsDocument && it.Closable && !it.Pinned {
		closeSz := gtx.Dp(theme.IconSM) + gtx.Dp(theme.SpaceXS)
		w += gap + closeSz
	}
	return max(w, gtx.Dp(theme.HitMin))
}

func (s *TabsState) stepIndicator(env *Env, gtx *layout.Context, pr TabsProps) {
	sl := s.slots[pr.ActiveID]
	if sl == nil {
		s.indicator.step(env, image.Rectangle{}, false)
		return
	}
	target := sl.rect
	if pr.Variant == TabsDocument {
		barH := gtx.Dp(theme.FocusStroke)
		target = image.Rect(sl.rect.Min.X, sl.rect.Max.Y-barH, sl.rect.Max.X, sl.rect.Max.Y)
	}
	s.indicator.step(env, target, true)
}

func (s *TabsState) draw(env *Env, gtx *layout.Context, pr TabsProps, maxW, h int, ev *TabsEvents) error {
	c := env.Th.Chrome()
	stripR := s.stripRect
	focused := env.FocusVisible(gtx, s.Tag()) || env.previewFocus(s.Tag())
	focus := followState(env, &s.focusF, focused)

	// Register single tab-stop + pointer area on the strip.
	func() {
		defer clip.Rect{Max: stripR.Size()}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, s.Tag())
	}()

	if pr.Variant == TabsMode {
		fillLayer(gtx, stripR, gtx.Dp(theme.RadiusMD), c.FillSoft, 1)
		s.indicator.draw(gtx, max(gtx.Dp(theme.RadiusMD)-gtx.Dp(theme.SpaceXXS), gtx.Dp(theme.RadiusXS)), c.SurfaceRaised)
	} else {
		divY := h - gtx.Dp(theme.Hairline)
		fillLayer(gtx, image.Rect(0, divY, maxW, h), 0, c.Border, 1)
	}

	// Clipped tab strip content.
	if err := s.drawStripTabs(env, gtx, pr, stripR, focus, ev); err != nil {
		return err
	}

	if pr.Variant == TabsDocument {
		s.indicator.draw(gtx, gtx.Dp(theme.Hairline), c.Action)
	}

	if len(s.hiddenIDs) > 0 {
		if err := s.drawOverflow(env, gtx, pr, stripR.Max.X, h, ev); err != nil {
			return err
		}
	}
	return nil
}

func (s *TabsState) drawStripTabs(env *Env, gtx *layout.Context, pr TabsProps, stripR image.Rectangle, focus float32, ev *TabsEvents) error {
	c := env.Th.Chrome()
	defer clip.Rect{Max: stripR.Size()}.Push(gtx.Ops).Pop()
	for _, it := range pr.Items {
		sl := s.slots[it.ID]
		if sl == nil || !sl.visible {
			continue
		}
		active := it.ID == pr.ActiveID
		if !active && !it.Disabled && pr.Variant == TabsDocument {
			if s.pressID == it.ID {
				fillLayer(gtx, sl.rect, gtx.Dp(theme.RadiusXS), c.PressedWash, 1)
			} else if s.hoverID == it.ID {
				fillLayer(gtx, sl.rect, gtx.Dp(theme.RadiusXS), c.HoverWash, 1)
			}
		}
		if err := s.drawTabContent(env, gtx, pr.Variant, it, sl, active, stripR.Max.X, ev); err != nil {
			return err
		}
	}
	if focus > 0 {
		insetFocusRing(gtx, stripR, gtx.Dp(theme.RadiusMD), c.FocusBorder, focus)
	}
	return nil
}

func (s *TabsState) drawTabContent(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, sl *tabSlot, active bool, stripMaxX int, ev *TabsEvents) error {
	c := env.Th.Chrome()
	ink := tabInk(&c, active, it.Disabled)

	x := sl.rect.Min.X + gtx.Dp(theme.SpaceLG)
	gap := gtx.Dp(theme.SpaceXS)
	if v == TabsDocument && it.Dirty {
		dotD := gtx.Dp(theme.SpaceSM)
		dy := sl.rect.Min.Y + (sl.rect.Dy()-dotD)/2
		fillLayer(gtx, image.Rect(x, dy, x+dotD, dy+dotD), dotD/2, env.Th.Status().Info, 1)
		x += dotD + gap
	}
	if it.Icon != "" || (v == TabsDocument && it.Pinned) {
		ic := it.Icon
		if ic == "" {
			ic = IconPin
		}
		iconPx := gtx.Dp(theme.IconSM)
		iy := sl.rect.Min.Y + (sl.rect.Dy()-iconPx)/2
		if err := drawIconAt(env, gtx, image.Pt(x, iy), ic, theme.IconSM, ink); err != nil {
			return err
		}
		x += iconPx + gap
	}
	if (v != TabsDocument || !it.Pinned) && it.Label != "" {
		ly := sl.rect.Min.Y + centerLabelY(env, gtx, theme.TypeLabel(), sl.rect.Dy())
		right := min(sl.rect.Max.X, stripMaxX)
		if maxLblW := right - x - gtx.Dp(theme.SpaceXS); maxLblW > 0 {
			drawRunAt(env, gtx, image.Pt(x, ly), maxLblW, sl.rect.Dy(), theme.TypeLabel(), ink, it.Label, true)
		}
	}
	return s.drawTabCloseButton(env, gtx, v, it, sl, ev)
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

func (s *TabsState) drawTabCloseButton(env *Env, gtx *layout.Context, v TabsVariant, it TabItem, sl *tabSlot, ev *TabsEvents) error {
	if v != TabsDocument || !it.Closable || it.Pinned {
		return nil
	}
	closeSz := gtx.Dp(theme.IconSM)
	cx := sl.rect.Max.X - gtx.Dp(theme.SpaceSM) - closeSz
	cy := sl.rect.Min.Y + (sl.rect.Dy()-closeSz)/2
	var cerr error
	at(gtx, image.Pt(cx, cy), func(gtx *layout.Context) {
		cPr := IconButtonProps{Icon: IconClose, Label: "Close tab", Variant: IconButtonGhost, Disabled: it.Disabled}
		var clicked bool
		clicked, cerr = sl.closeBtn.Layout(env, gtx, &cPr)
		if clicked {
			ev.Closed = it.ID
		}
	})
	return cerr
}

func (s *TabsState) drawOverflow(env *Env, gtx *layout.Context, pr TabsProps, startX, h int, ev *TabsEvents) error {
	ovPr := IconButtonProps{Icon: IconChevronDown, Label: "Hidden tabs", Variant: IconButtonGhost}
	ovSize := s.overflowBtn.Measure(env, gtx, &ovPr).Size
	bx := startX + gtx.Dp(theme.SpaceXS)
	by := (h - ovSize.Y) / 2
	var clicked bool
	var oerr error
	at(gtx, image.Pt(bx, by), func(gtx *layout.Context) {
		clicked, oerr = s.overflowBtn.Layout(env, gtx, &ovPr)
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
