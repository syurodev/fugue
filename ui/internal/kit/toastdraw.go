package kit

import (
	"fmt"
	"image"
	"strconv"

	"gioui.org/layout"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// drawToastPanel paints one toast into rect: surface, icon, title, detail of
// at most two lines, merge badge, and the action and close buttons laid out
// where they draw (UK-E10). A toast on its way out (active false) draws its
// controls disabled, so it takes no input (§3.7). The manager and the inline
// specimens share this painter, so a specimen is the manager's pixels; the
// click comes back instead of a callback (UK-06).
func drawToastPanel(env *Env, gtx *layout.Context, rect image.Rectangle, t *Toast, count int, active bool, action *ButtonState, close *IconButtonState) (toastClick, error) {
	c := env.Th.Chrome()
	radius := gtx.Dp(theme.RadiusXL)
	DrawFloatShadow(gtx, rect, radius)
	fillRRect(gtx.Ops, rect, radius, c.SurfaceRaised)
	strokeRRect(gtx.Ops, rect, radius, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)
	inner := rect.Inset(gtx.Dp(theme.SpaceLG))

	line := labelLine(env, gtx, theme.TypeLabel())
	if err := drawToastHeader(env, gtx, rect, inner, t, count, line); err != nil {
		return toastNoClick, err
	}
	drawToastDetail(env, gtx, rect, inner, t, line)
	click, err := drawToastControls(env, gtx, rect, t, active, action, close)
	if err != nil {
		return toastNoClick, fmt.Errorf("draw the toast controls: %w", err)
	}
	return click, nil
}

// drawToastHeader paints the status icon, the title and the merge badge.
func drawToastHeader(env *Env, gtx *layout.Context, rect, inner image.Rectangle, t *Toast, count, line int) error {
	c := env.Th.Chrome()
	x, y := inner.Min.X, inner.Min.Y
	if t.Icon != "" {
		iconPx := gtx.Dp(theme.IconMD)
		if err := drawIconAt(env, gtx, image.Pt(x, y+(line-iconPx)/2), t.Icon, theme.IconMD, c.IconMuted); err != nil {
			return fmt.Errorf("draw the toast icon: %w", err)
		}
		x += iconPx + gtx.Dp(theme.SpaceSM)
	}
	drawMenuText(env, gtx, theme.TypeLabel(), c.Fg, t.Title, image.Pt(x, y), line)
	if count > 1 {
		badge := "×" + strconv.Itoa(count)
		bx := rect.Max.X - 2*gtx.Dp(theme.SpaceLG) - measureLabel(env, gtx, theme.TypeSmall(), badge).X
		drawMenuText(env, gtx, theme.TypeSmall(), c.FgMuted, badge, image.Pt(bx, y), line)
	}
	return nil
}

// drawToastDetail paints the detail, wrapped at detailWidth and clipped to
// two lines (§6.18).
func drawToastDetail(env *Env, gtx *layout.Context, rect, inner image.Rectangle, t *Toast, line int) {
	if t.Detail == "" {
		return
	}
	c := env.Th.Chrome()
	dw := detailWidth(gtx, rect.Dx())
	dy := gtx.Dp(theme.SpaceXS)
	at(gtx, image.Pt(inner.Min.X, inner.Min.Y+line+dy), func(gtx *layout.Context) {
		g := *gtx
		g.Constraints.Max.X = dw
		two := 2 * labelLine(env, gtx, theme.TypeSmall())
		defer clip.Rect(image.Rect(0, 0, dw, two)).Push(gtx.Ops).Pop()
		dims := drawText(env, gtx, theme.TypeSmall(), c.FgMuted, t.Detail, true)
		recordRun(env, image.Rectangle{Max: image.Pt(min(dims.Size.X, dw), min(dims.Size.Y, two))}.Add(image.Pt(inner.Min.X, inner.Min.Y+line+dy)))
	})
}

// drawToastControls lays the close and action buttons out at their boxes and
// reports the click; a toast on its way out (active false) draws them
// disabled (§3.7).
func drawToastControls(env *Env, gtx *layout.Context, rect image.Rectangle, t *Toast, active bool, action *ButtonState, close *IconButtonState) (toastClick, error) {
	btn := max(gtx.Dp(theme.ControlXS), gtx.Dp(theme.HitMin))
	cy := rect.Min.Y + (rect.Dy()-btn)/2
	cx := rect.Max.X - gtx.Dp(theme.SpaceLG) - btn
	var clicked bool
	var err error
	at(gtx, image.Pt(cx, cy), func(gtx *layout.Context) {
		clicked, err = close.Layout(env, gtx, &IconButtonProps{
			Icon: IconClose, Label: labelClose, Variant: IconButtonGhost, Size: SizeXS,
			Disabled: !active,
		})
	})
	if err != nil {
		return toastNoClick, fmt.Errorf("draw the toast close button: %w", err)
	}
	if clicked {
		return toastCloseClick, nil
	}
	if t.Action == "" {
		return toastNoClick, nil
	}
	pr := ButtonProps{Text: t.Action, Variant: Secondary, Size: SizeSM}
	aw := action.Measure(env, gtx, pr).Size.X
	ax := cx - gtx.Dp(theme.SpaceSM) - aw
	ay := rect.Min.Y + (rect.Dy()-gtx.Dp(theme.ControlSM))/2
	var actionClicked bool
	at(gtx, image.Pt(ax, ay), func(gtx *layout.Context) {
		ig := *gtx
		if !active {
			ig = gtx.Disabled()
		}
		actionClicked, err = action.Layout(env, &ig, pr)
	})
	if err != nil {
		return toastNoClick, fmt.Errorf("draw the toast action: %w", err)
	}
	if actionClicked {
		return toastActionClick, nil
	}
	return toastNoClick, nil
}

// ToastSpecimen is the state of one inline toast specimen — the gallery's
// matrices (UK-E03) — of the same pixels the manager draws: its own two
// controls, the merged count it shows, and no timer.
type ToastSpecimen struct {
	t      Toast
	count  int
	action ButtonState
	close  IconButtonState
}

// NewToastSpecimen builds a specimen of t showing one event.
func NewToastSpecimen(t *Toast) *ToastSpecimen {
	return &ToastSpecimen{t: *t, count: 1}
}

// SetCount sets the merged count the specimen's badge shows.
func (s *ToastSpecimen) SetCount(n int) { s.count = n }

// Measure is the specimen's box at the full ToastWidth.
func (s *ToastSpecimen) Measure(env *Env, gtx *layout.Context) image.Point {
	return image.Pt(gtx.Dp(theme.ToastWidth), toastHeight(env, gtx, &s.t, gtx.Dp(theme.ToastWidth)))
}

// Layout draws the specimen at the current cursor, taking no input (a
// specimen is not a live toast).
func (s *ToastSpecimen) Layout(env *Env, gtx *layout.Context) image.Point {
	size := s.Measure(env, gtx)
	if _, err := drawToastPanel(env, gtx, image.Rectangle{Max: size}, &s.t, s.count, false, &s.action, &s.close); err != nil {
		env.noteToastError(err)
	}
	return size
}
