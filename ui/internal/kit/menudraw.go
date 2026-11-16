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

// menuItemWidth is the room one item needs: the paddings, the icon column,
// the label, the shortcut column and the submenu arrow.
func menuItemWidth(env *Env, gtx *layout.Context, it *MenuItem, cols menuCols) int {
	w := 2*gtx.Dp(theme.SpaceMD) + measureLabel(env, gtx, theme.TypeBody(), it.Label).X
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
func drawMenuItem(env *Env, gtx *layout.Context, it *MenuItem, r image.Rectangle, cols menuCols) error {
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
	x := r.Min.X + gtx.Dp(theme.SpaceMD)
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
	right := r.Max.X - gtx.Dp(theme.SpaceMD)
	if len(it.Sub) > 0 {
		arrow := gtx.Dp(theme.IconSM)
		right -= arrow
		if err := drawIconAt(env, gtx, image.Pt(right, r.Min.Y+(r.Dy()-arrow)/2), IconChevronRight, theme.IconSM, ink); err != nil {
			return err
		}
		right -= gtx.Dp(theme.SpaceLG)
	}
	return drawMenuShortcut(env, gtx, it, r, right)
}

// drawMenuShortcut draws the item's Kbd chip, right-aligned at x right.
func drawMenuShortcut(env *Env, gtx *layout.Context, it *MenuItem, r image.Rectangle, right int) error {
	if it.Shortcut.Key == "" {
		return nil
	}
	size, err := KbdSize(env, gtx, it.Shortcut)
	if err != nil {
		return fmt.Errorf("menu item %q: %w", it.ID, err)
	}
	var kerr error
	pt := image.Pt(right-size.X, r.Min.Y+(r.Dy()-size.Y)/2)
	at(gtx, pt, func(gtx *layout.Context) {
		_, kerr = drawKbd(env, gtx, it.Shortcut, it.Disabled, pt)
	})
	return kerr
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
