package kit

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// menuCols are the columns a panel's items share: whether the icon column
// exists, and the width of the widest shortcut chip, which every shortcut
// right-aligns in so the labels and the chips line up (§6.15).
type menuCols struct {
	icons bool
	kbd   int
}

// menuColumns measures the shared columns of items.
func menuColumns(env *Env, gtx *layout.Context, items []MenuItem) (menuCols, error) {
	var cols menuCols
	for i := range items {
		if items[i].Icon != "" {
			cols.icons = true
		}
		if items[i].Shortcut.Key != "" {
			size, err := KbdSize(env, gtx, items[i].Shortcut)
			if err != nil {
				return cols, fmt.Errorf("menu item %q: %w", items[i].ID, err)
			}
			cols.kbd = max(cols.kbd, size.X)
		}
	}
	return cols, nil
}

// layoutRows measures the items into rows and returns the panel's size: the
// widest item plus the padding, never under minW. When any item has an icon
// every item leaves the icon column, so the labels line up (§6.15).
func (lv *menuLevel) layoutRows(env *Env, gtx *layout.Context, minW, insetX int) (image.Point, error) {
	lv.pad = gtx.Dp(theme.SpaceSM)
	lv.rows = lv.rows[:0]
	cols, err := menuColumns(env, gtx, lv.items)
	if err != nil {
		return image.Point{}, err
	}
	lv.cols = cols
	y, w := 0, max(minW-2*lv.pad, 0)
	for i := range lv.items {
		h := gtx.Dp(theme.Row)
		if lv.items[i].separator {
			h = 2*gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.Hairline)
		} else {
			w = max(w, menuItemWidth(env, gtx, &lv.items[i], cols, insetX))
		}
		lv.rows = append(lv.rows, menuRow{y: y, h: h})
		y += h
	}
	return image.Pt(w+2*lv.pad, y+2*lv.pad), nil
}

// menuItemWidth is the room one item needs: the paddings, the icon column,
// the label, the shortcut column and the submenu arrow.
func menuItemWidth(env *Env, gtx *layout.Context, it *MenuItem, cols menuCols, insetX int) int {
	w := 2*insetX + measureLabel(env, gtx, theme.TypeBody(), it.Label).X
	if cols.icons {
		w += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	if cols.kbd > 0 {
		w += gtx.Dp(theme.SpaceXL) + cols.kbd
	}
	if len(it.Sub) > 0 {
		w += gtx.Dp(theme.SpaceLG) + gtx.Dp(theme.IconSM)
	}
	return w
}

// drawMenuItem draws one row inside r: a separator is a hairline; an item is
// the icon column, the label, and the shortcut chip and the arrow at the
// right edge. A disabled item draws in FgDisabled (§3.5).
func drawMenuItem(env *Env, gtx *layout.Context, it *MenuItem, r image.Rectangle, cols menuCols, insetX int) error {
	c := env.Th.Chrome()
	if it.separator {
		y := r.Min.Y + r.Dy()/2
		paint.FillShape(gtx.Ops, c.Border, clip.Rect(image.Rect(r.Min.X, y, r.Max.X, y+gtx.Dp(theme.Hairline))).Op())
		return nil
	}
	fg, ink := c.Fg, c.IconMuted
	if it.Disabled {
		fg, ink = c.FgDisabled, c.FgDisabled
	}
	x := r.Min.X + insetX
	iconPx := gtx.Dp(theme.IconMD)
	if cols.icons {
		if it.Icon != "" {
			if err := drawIconAt(env, gtx, image.Pt(x, r.Min.Y+(r.Dy()-iconPx)/2), it.Icon, theme.IconMD, ink); err != nil {
				return err
			}
		}
		x += iconPx + gtx.Dp(theme.SpaceSM)
	}
	drawMenuText(env, gtx, theme.TypeBody(), fg, it.Label, image.Pt(x, r.Min.Y), r.Dy())
	right := r.Max.X - insetX
	if len(it.Sub) > 0 {
		arrow := gtx.Dp(theme.IconSM)
		right -= arrow
		if err := drawIconAt(env, gtx, image.Pt(right, r.Min.Y+(r.Dy()-arrow)/2), IconChevronRight, theme.IconSM, ink); err != nil {
			return err
		}
		right -= gtx.Dp(theme.SpaceLG)
	}
	return drawMenuShortcut(env, gtx, it.ID, it.Shortcut, it.Disabled, r, right)
}

// drawMenuPanelSurface draws the floating panel surface (shadow, SurfaceRaised,
// BorderSubtle, radius.xl) and the sliding highlight pill when hiAv > 0.
func drawMenuPanelSurface(env *Env, gtx *layout.Context, rect image.Rectangle, pad int, hiYv, hiAv float32) image.Rectangle {
	c := env.Th.Chrome()
	radius := gtx.Dp(theme.RadiusXL)
	DrawFloatShadow(gtx, rect, radius)
	fillRRect(gtx.Ops, rect, radius, c.SurfaceRaised)
	strokeRRect(gtx.Ops, rect, radius, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)
	inner := rect.Inset(pad)
	if hiAv > 0 {
		top := inner.Min.Y + int(hiYv+0.5)
		hiRect := image.Rect(inner.Min.X, top, inner.Max.X, top+gtx.Dp(theme.Row))
		fillRRect(gtx.Ops, hiRect, gtx.Dp(theme.RadiusMD), fade(c.HoverWash, hiAv))
	}
	return inner
}

// drawMenuShortcut draws a Kbd chip, right-aligned at x right.
func drawMenuShortcut(env *Env, gtx *layout.Context, id string, sc Shortcut, disabled bool, r image.Rectangle, right int) error {
	if sc.Key == "" {
		return nil
	}
	size, err := KbdSize(env, gtx, sc)
	if err != nil {
		return fmt.Errorf("menu item %q: %w", id, err)
	}
	var kerr error
	pt := image.Pt(right-size.X, r.Min.Y+(r.Dy()-size.Y)/2)
	at(gtx, pt, func(gtx *layout.Context) {
		_, kerr = drawKbd(env, gtx, sc, disabled, pt)
	})
	return kerr
}

func suggestColumns(env *Env, gtx *layout.Context, items []SuggestItem) (menuCols, error) {
	var cols menuCols
	for i := range items {
		if items[i].Icon != "" {
			cols.icons = true
		}
		if items[i].Shortcut.Key != "" {
			size, err := KbdSize(env, gtx, items[i].Shortcut)
			if err != nil {
				return cols, err
			}
			cols.kbd = max(cols.kbd, size.X)
		}
	}
	return cols, nil
}

func suggestItemWidth(env *Env, gtx *layout.Context, it *SuggestItem, cols menuCols, insetX int) int {
	w := 2*insetX + measureLabel(env, gtx, theme.TypeBody(), it.Label).X
	if cols.icons {
		w += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	if it.Secondary != "" {
		w += gtx.Dp(theme.SpaceSM) + measureLabel(env, gtx, theme.TypeSmall(), it.Secondary).X
	}
	if cols.kbd > 0 {
		w += gtx.Dp(theme.SpaceXL) + cols.kbd
	}
	return w
}

func drawSuggestItem(env *Env, gtx *layout.Context, it *SuggestItem, r image.Rectangle, cols menuCols, insetX int) error {
	c := env.Th.Chrome()
	fg, secFg, ink := c.Fg, c.FgMuted, c.IconMuted
	if it.Disabled {
		fg, secFg, ink = c.FgDisabled, c.FgDisabled, c.FgDisabled
	}
	x := r.Min.X + insetX
	iconPx := gtx.Dp(theme.IconMD)
	if cols.icons {
		if it.Icon != "" {
			if err := drawIconAt(env, gtx, image.Pt(x, r.Min.Y+(r.Dy()-iconPx)/2), it.Icon, theme.IconMD, ink); err != nil {
				return err
			}
		}
		x += iconPx + gtx.Dp(theme.SpaceSM)
	}
	drawMenuText(env, gtx, theme.TypeBody(), fg, it.Label, image.Pt(x, r.Min.Y), r.Dy())
	if it.Secondary != "" {
		x += measureLabel(env, gtx, theme.TypeBody(), it.Label).X + gtx.Dp(theme.SpaceSM)
		drawMenuText(env, gtx, theme.TypeSmall(), secFg, it.Secondary, image.Pt(x, r.Min.Y), r.Dy())
	}
	right := r.Max.X - insetX
	return drawMenuShortcut(env, gtx, it.ID, it.Shortcut, it.Disabled, r, right)
}

func suggestEdgeEnabled(items []SuggestItem, first bool) int {
	for i := range items {
		idx := i
		if !first {
			idx = len(items) - 1 - i
		}
		if !items[idx].Disabled {
			return idx
		}
	}
	return -1
}

func (s *SuggestListState) drawStatusRow(env *Env, gtx *layout.Context, inner image.Rectangle, p *SuggestProps) {
	c := env.Th.Chrome()
	x := inner.Min.X + s.itemInset
	h := s.rows[0].h
	if s.spinnerVisible(gtx, p.Inline) {
		iconPx := gtx.Dp(theme.IconMD)
		pt := image.Pt(x, inner.Min.Y+(h-iconPx)/2)
		at(gtx, pt, func(gtx *layout.Context) {
			s.spinner.Layout(env, gtx, iconPx, c.IconMuted)
		})
		x += iconPx + gtx.Dp(theme.SpaceSM)
	} else if s.status == suggestLoading || p.FieldAlign {
		x += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	drawMenuText(env, gtx, theme.TypeSmall(), c.FgMuted, s.statusLabel(p), image.Pt(x, inner.Min.Y), h)
}

func (s *SuggestListState) itemAt(p image.Point) int {
	inner := s.rect.Inset(s.pad)
	if !p.In(inner) {
		return -1
	}
	for i, r := range s.rows {
		if p.Y-inner.Min.Y < r.y+r.h {
			return i
		}
	}
	return -1
}

func (s *SuggestListState) stepHighlight(items []SuggestItem, dir int) {
	n := len(items)
	if n == 0 {
		s.hi = -1
		return
	}
	start := s.hi
	if start < 0 || start >= n {
		s.hi = suggestEdgeEnabled(items, dir > 0)
		return
	}
	for step := 1; step <= n; step++ {
		idx := (start + dir*step + n*step) % n
		if !items[idx].Disabled {
			s.hi = idx
			return
		}
	}
}

func (s *SuggestListState) pageHighlight(items []SuggestItem, delta int) {
	n := len(items)
	if n == 0 {
		s.hi = -1
		return
	}
	cur := s.hi
	if cur < 0 || cur >= n {
		cur = suggestEdgeEnabled(items, delta > 0)
		s.hi = cur
		return
	}
	dir := 1
	steps := delta
	if delta < 0 {
		dir = -1
		steps = -delta
	}
	for range steps {
		next := cur + dir
		for next >= 0 && next < n && items[next].Disabled {
			next += dir
		}
		if next < 0 || next >= n {
			break
		}
		cur = next
	}
	s.hi = cur
}

// drawMenuText draws one line of a row, centered on the row's height h, and
// records its bounds for the gallery's overflow check (UK-E09).
func drawMenuText(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string, pt image.Point, h int) {
	pt.Y += centerLabelY(env, gtx, t, h)
	at(gtx, pt, func(gtx *layout.Context) {
		dims := drawText(env, gtx, t, c, txt, false)
		recordRun(env, image.Rectangle{Max: dims.Size}.Add(pt))
	})
}
