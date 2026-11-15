package kit

import (
	"errors"
	"fmt"
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// IconButton, ui-kit §6.3: one icon on a box of at least size.hit.min,
// however small the glyph. It has the ghost and secondary variants, the
// toggled state for pinning and follow buttons (SelectedWash, §4.1), and
// the same §4.1 states and keyboard behavior as Button. Label is required:
// it is the accessible name — Gio v0.10.3 exposes no accessibility tree,
// so the kit carries the name for it and for the tooltip that will point
// at this control (UK-05).

// IconButtonVariant is the visual variant (§6.3): ghost is the default
// toolbar icon button, secondary gives it the hairline box.
type IconButtonVariant int

// The §6.3 variants.
const (
	IconButtonGhost IconButtonVariant = iota // the default
	IconButtonSecondary
)

// IconButtonProps are the per-frame, caller-owned properties (UK-02).
// Toggled marks a pinned or following control; Label must be non-empty —
// it is the accessible name and the later tooltip text.
type IconButtonProps struct {
	Icon     IconName
	Label    string
	Variant  IconButtonVariant
	Toggled  bool
	Disabled bool
}

// IconButtonState is the long-lived state the caller keeps per logical
// control (UK-02).
type IconButtonState struct {
	click  widget.Clickable
	hoverF motion.Follower
	pressF motion.Follower
}

// NewIconButtonState builds the state for one icon button.
func NewIconButtonState() *IconButtonState { return &IconButtonState{} }

// Tag is the control's input tag, for focus scripting and previews.
func (s *IconButtonState) Tag() event.Tag { return &s.click }

// Measure returns the hit-area box: always at least size.hit.min square
// (§6.3), whatever the glyph. It draws nothing and touches no input source.
func (s *IconButtonState) Measure(_ *Env, gtx *layout.Context, _ IconButtonProps) layout.Dimensions {
	side := gtx.Dp(theme.HitMin)
	return layout.Dimensions{Size: image.Pt(side, side)}
}

// errIconButtonName reports the missing accessible name of §6.3.
var errIconButtonName = errors.New("an IconButton needs a Label: it is the accessible name and the tooltip text")

// Layout draws the control and reports whether it was activated this frame
// (UK-06), with Button's keyboard behavior. An empty Label or an unknown
// icon is a programming error and comes back as an error.
func (s *IconButtonState) Layout(env *Env, gtx *layout.Context, p IconButtonProps) (bool, error) {
	if p.Label == "" {
		return false, errIconButtonName
	}
	var clicked bool
	if p.Disabled {
		d := gtx.Disabled()
		gtx = &d
	} else {
		clicked = s.click.Clicked(*gtx)
	}
	st := resolveState(env, gtx, s.Tag(), &s.click, p.Disabled, false, p.Toggled)
	var drawErr error
	s.click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		err := s.draw(env, &gtx, p, st)
		drawErr = err
		return layout.Dimensions{}
	})
	if drawErr != nil {
		return false, fmt.Errorf("draw the icon button: %w", drawErr)
	}
	return clicked, nil
}

// draw paints the hit-area box, the centered icon and the focus ring.
func (s *IconButtonState) draw(env *Env, gtx *layout.Context, p IconButtonProps, st controlState) error {
	side := gtx.Dp(theme.HitMin)
	r := image.Rect(0, 0, side, side)
	look, border := iconButtonLookOf(env, p.Variant, st)
	hv, pv := followState(env, &s.hoverF, st.hovered), followState(env, &s.pressF, st.pressed)
	drawControlBody(env, gtx, r, gtx.Dp(theme.RadiusMD), look, border, hv, pv)

	box := gtx.Dp(theme.IconMD)
	if err := drawIconAt(env, gtx, image.Pt((side-box)/2, (side-box)/2), p.Icon, theme.IconMD, look.fg); err != nil {
		return err
	}
	if st.focused {
		focusRing(env, gtx, r, gtx.Dp(theme.RadiusMD))
	}
	return nil
}

// iconButtonLookOf resolves the color pairing of one IconButton state
// (UK-03): ghost rests on IconMuted and lifts to Fg under interaction or
// when toggled, so the glyph keeps 4.5:1 over its wash on every chrome
// surface (UK-E04); secondary reads Fg on the hairline box. The toggled
// state sits on SelectedWash (§6.3). Disabled reads FgDisabled on the bare
// surface (§4.1).
func iconButtonLookOf(env *Env, v IconButtonVariant, st controlState) (controlLook, bool) {
	c := env.Th.Chrome()
	l := controlLook{hover: c.HoverWash, press: c.PressedWash, fg: c.IconMuted}
	border := false
	switch v {
	case IconButtonGhost:
	case IconButtonSecondary:
		l.fg = c.Fg
		border = true
	}
	if st.toggled {
		l.fill, l.base = true, c.SelectedWash
		l.fg = c.Fg
	}
	if st.hovered || st.pressed {
		l.fg = c.Fg
	}
	if st.disabled {
		l.fill = false
		l.fg = c.FgDisabled
	}
	return l, border
}
