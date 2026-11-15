package kit

import (
	"errors"
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// IconButton, ui-kit §6.3: one icon centered on a size.control square with
// radius.md — never smaller than size.hit.min, whatever the glyph. Variants
// are ghost (the default), secondary and bordered; the toggled state for
// pinning and follow buttons rests on SelectedWash (§4.1) and fades in and
// out. It has the §4.1 states, the keyboard behavior and the §3.7 motion of
// Button: fills on Snappy, press shrink with ripple, ring fade. Label is
// required: it is the accessible name — Gio v0.10.3 exposes no
// accessibility tree, so the kit carries the name for it and for the
// tooltip that will point at this control (UK-05). Like Button it draws its
// ring outside its box, so no parent may clip it.

// IconButtonVariant is the visual variant (§6.3): ghost is the default
// toolbar icon button, secondary gives it the tinted box, bordered the faint
// border.
type IconButtonVariant int

// The §6.3 variants.
const (
	IconButtonGhost IconButtonVariant = iota // the default
	IconButtonSecondary
	IconButtonBordered
)

// IconButtonProps are the per-frame, caller-owned properties (UK-02).
// Toggled marks a pinned or following control; Label must be non-empty —
// it is the accessible name and the later tooltip text. Size is the
// control token of the square, md by default.
type IconButtonProps struct {
	Icon     IconName
	Label    string
	Variant  IconButtonVariant
	Size     ButtonSize
	Toggled  bool
	Disabled bool
}

// IconButtonState is the long-lived state the caller keeps per logical
// control (UK-02).
type IconButtonState struct{ pressable }

// NewIconButtonState builds the state for one icon button.
func NewIconButtonState() *IconButtonState { return &IconButtonState{pressable: newPressable()} }

// Measure returns the box: the square of the size token, at least
// size.hit.min (§6.3). It draws nothing and touches no input source.
func (s *IconButtonState) Measure(_ *Env, gtx *layout.Context, p IconButtonProps) layout.Dimensions {
	side := max(controlHeight(gtx, p.Size), gtx.Dp(theme.HitMin))
	return layout.Dimensions{Size: image.Pt(side, side)}
}

// errIconButtonName reports the missing accessible name of §6.3.
var errIconButtonName = errors.New("an IconButton needs a Label: it is the accessible name and the tooltip text")

// Layout draws the control at the current offset and reports whether it was
// activated this frame (UK-06), with Button's keyboard behavior. An empty
// Label or an unknown icon is a programming error and comes back as an
// error.
func (s *IconButtonState) Layout(env *Env, gtx *layout.Context, p IconButtonProps) (bool, error) {
	if p.Label == "" {
		return false, errIconButtonName
	}
	side := s.Measure(env, gtx, p).Size
	clicked, mv := s.begin(env, gtx, side, pressOpts{disabled: p.Disabled, toggled: p.Toggled})
	var err error
	s.paint(env, gtx, image.Rectangle{Max: side}, gtx.Dp(theme.RadiusMD), iconButtonLookOf(env, p.Variant, p.Toggled, p.Disabled), mv,
		func(fg color.NRGBA) {
			d := buttonIconSize(p.Size)
			box := gtx.Dp(d)
			err = drawIconAt(env, gtx, image.Pt((side.X-box)/2, (side.Y-box)/2), p.Icon, d, fg)
		})
	if err != nil {
		return false, fmt.Errorf("draw the icon button: %w", err)
	}
	return clicked, nil
}

// iconButtonLookOf resolves the color pairing of one IconButton (UK-03):
// ghost rests on IconMuted over nothing and lifts to Fg under interaction
// or when toggled, so the glyph keeps 4.5:1 over its wash on every chrome
// surface (UK-E04); secondary and bordered read Fg. The toggled state sits
// on SelectedWash, independent of hover (§6.3). Disabled draws the whole
// look at DisabledOpacity (§4.1).
func iconButtonLookOf(env *Env, v IconButtonVariant, toggled, disabled bool) controlLook {
	c := env.Th.Chrome()
	l := controlLook{hover: c.HoverWash, press: c.PressedWash, sel: c.SelectedWash, fg: c.IconMuted, fgHover: c.Fg, ripple: c.RippleWash, ring: c.FocusBorder}
	switch v {
	case IconButtonGhost:
		// Ghost rests on the bare surface in the colors set above.
	case IconButtonSecondary:
		l.base, l.hover, l.fg = c.FillSoft, c.FillSoftHover, c.Fg
	case IconButtonBordered:
		l.border, l.hover, l.press, l.fg = c.BorderSubtle, c.FillSoft, c.FillSoftHover, c.Fg
	}
	if toggled {
		l.fg = c.Fg
	}
	if disabled {
		l = l.dim(theme.DisabledOpacity)
	}
	return l
}
