package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// menuHasIcon reports whether any item carries an icon.
func menuHasIcon(items []MenuItem) bool {
	for i := range items {
		if items[i].Icon != "" {
			return true
		}
	}
	return false
}

// menuItemWidth is the room one item needs: the paddings, the icon column,
// the label, the shortcut and the submenu arrow.
func menuItemWidth(env *Env, gtx *layout.Context, it *MenuItem, icons bool) int {
	w := 2*gtx.Dp(theme.SpaceMD) + measureLabel(env, gtx, theme.TypeBody(), it.Label).X
	if icons {
		w += gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	if it.Shortcut != "" {
		w += gtx.Dp(theme.SpaceXL) + measureLabel(env, gtx, theme.TypeSmall(), it.Shortcut).X
	}
	if len(it.Sub) > 0 {
		w += gtx.Dp(theme.SpaceLG) + gtx.Dp(theme.IconSM)
	}
	return w
}

// drawMenuItem draws one row inside r: a separator is a hairline; an item is
// the icon column, the label, and the shortcut and the arrow at the right
// edge. A disabled item draws in FgDisabled (§3.5).
func drawMenuItem(env *Env, gtx *layout.Context, it *MenuItem, r image.Rectangle, icons bool) error {
	c := env.Th.Chrome()
	if it.separator {
		y := r.Min.Y + r.Dy()/2
		paint.FillShape(gtx.Ops, c.Border, clip.Rect(image.Rect(r.Min.X, y, r.Max.X, y+gtx.Dp(theme.Hairline))).Op())
		return nil
	}
	fg, ink, sub := c.Fg, c.IconMuted, c.FgSubtle
	if it.Disabled {
		fg, ink, sub = c.FgDisabled, c.FgDisabled, c.FgDisabled
	}
	x := r.Min.X + gtx.Dp(theme.SpaceMD)
	iconPx := gtx.Dp(theme.IconMD)
	if icons {
		if it.Icon != "" {
			if err := drawIconAt(env, gtx, image.Pt(x, r.Min.Y+(r.Dy()-iconPx)/2), it.Icon, theme.IconMD, ink); err != nil {
				return err
			}
		}
		x += iconPx + gtx.Dp(theme.SpaceSM)
	}
	drawMenuText(env, gtx, theme.TypeBody(), fg, it.Label, image.Pt(x, r.Min.Y), r.Dy())
	right := r.Max.X - gtx.Dp(theme.SpaceMD)
	if len(it.Sub) > 0 {
		arrow := gtx.Dp(theme.IconSM)
		right -= arrow
		if err := drawIconAt(env, gtx, image.Pt(right, r.Min.Y+(r.Dy()-arrow)/2), IconChevronRight, theme.IconSM, ink); err != nil {
			return err
		}
		right -= gtx.Dp(theme.SpaceLG)
	}
	if it.Shortcut != "" {
		w := measureLabel(env, gtx, theme.TypeSmall(), it.Shortcut).X
		drawMenuText(env, gtx, theme.TypeSmall(), sub, it.Shortcut, image.Pt(right-w, r.Min.Y), r.Dy())
	}
	return nil
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
