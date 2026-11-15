package kit

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Button, ui-kit §6.2: an optional icon and a type.label label on a
// size.control box, in four variants and the three §3.4 sizes. It shows
// every §4.1 state — rest, hover, pressed, the focus-visible ring (only
// after Tab, via Env.FocusVisible), disabled and busy — and reports
// Clicked during the frame that saw it (UK-06). Enter and Space activate
// while focused, Space on release: widget.Clickable owns both. The
// background changes on Snappy (§3.7); the button never scales on press.
// Busy swaps the icon for a spinner and keeps the measured width, so a
// toolbar never jumps; the spinner asks for frames only while drawn.
//
// Props and state are separate values (UK-02): the caller rebuilds
// ButtonProps every frame and keeps one ButtonState per logical button for
// its lifetime — in lists, keyed by the object's stable ID, never by index.

// ButtonVariant is the visual variant (§6.2): Primary is the one main
// action of a frame or dialog, Secondary is the default, Ghost carries
// secondary actions in toolbars and rows, Danger marks data-destroying
// actions (never the busiest button of a frequently used screen).
type ButtonVariant int

// The §6.2 variants.
const (
	Primary   ButtonVariant = iota
	Secondary               // the default variant
	Ghost
	Danger
)

// ButtonSize picks a size.control token (§3.4).
type ButtonSize int

// The §3.4 control sizes.
const (
	SizeSM ButtonSize = iota // toolbar and in-row (24 dp)
	SizeMD                   // the default (28 dp)
	SizeLG                   // dialog primary and composer (32 dp)
)

// ButtonProps are the per-frame, caller-owned properties (UK-02). Icon is
// a packaged IconName; empty means none. Busy and Disabled come from the
// caller's operation; §4.1 prefers an enabled control that explains itself
// over a disabled one.
type ButtonProps struct {
	Text     string
	Icon     IconName
	Variant  ButtonVariant
	Size     ButtonSize
	Busy     bool
	Disabled bool
}

// ButtonState is the long-lived state the caller keeps per logical button
// (UK-02): the clickable, the busy spinner and the §3.7 transitions.
type ButtonState struct {
	click  widget.Clickable
	spin   Spinner
	hoverF motion.Follower
	pressF motion.Follower
}

// NewButtonState builds the state for one button.
func NewButtonState() *ButtonState { return &ButtonState{} }

// Tag is the button's input tag, for focus scripting and previews.
func (s *ButtonState) Tag() event.Tag { return &s.click }

// Measure returns the box the button draws from its idle content: the
// number measured layouts need, without drawing or touching the input
// source. Busy never changes it (§6.2).
func (s *ButtonState) Measure(env *Env, gtx *layout.Context, p ButtonProps) layout.Dimensions {
	w, h := buttonBox(env, gtx, p)
	return layout.Dimensions{Size: image.Pt(w, h), Baseline: (h + labelLine(env, gtx, theme.TypeLabel())) / 2}
}

// buttonBox is the idle box of §6.2: the height token of the size, twice
// the padding, the label and — when there is one — the icon slot with its
// gap, at least HitMin wide.
func buttonBox(env *Env, gtx *layout.Context, p ButtonProps) (w, h int) {
	padX, iconGap := buttonMetrics(p.Size)
	iconD := buttonIconSize(p.Size)
	h = controlHeight(gtx, p.Size)
	w = 2*gtx.Dp(padX) + measureLabel(env, gtx, theme.TypeLabel(), p.Text).X
	if p.Icon != "" {
		w += gtx.Dp(iconD) + gtx.Dp(iconGap)
	}
	w = max(w, gtx.Dp(theme.HitMin))
	return w, h
}

// Layout draws the button and reports whether it was activated this frame
// (UK-06): by pointer click, or by Enter and Space while focused. Busy
// blocks repeat activation (§4.1). An unknown icon is a programming error
// and comes back as an error instead of a button with no glyph.
func (s *ButtonState) Layout(env *Env, gtx *layout.Context, p ButtonProps) (bool, error) {
	var clicked bool
	if p.Disabled {
		// A disabled control consumes nothing: no hover, no focus (§4.1).
		d := gtx.Disabled()
		gtx = &d
	} else {
		clicked = s.click.Clicked(*gtx) && !p.Busy
	}
	st := resolveState(env, gtx, s.Tag(), &s.click, p.Disabled, p.Busy, false)
	var drawErr error
	s.click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		d, err := s.draw(env, &gtx, p, st)
		drawErr = err
		return d
	})
	if drawErr != nil {
		return false, fmt.Errorf("draw the button: %w", drawErr)
	}
	return clicked, nil
}

// draw paints the body in the resolved state: the variant fills, the
// content centered, then the focus ring last. The box comes from the idle
// content only — busy swaps content inside the same box, never re-measures
// (§6.2).
func (s *ButtonState) draw(env *Env, gtx *layout.Context, p ButtonProps, st controlState) (layout.Dimensions, error) {
	w, h := buttonBox(env, gtx, p)
	r := image.Rect(0, 0, w, h)

	look, border := buttonLookOf(env, p.Variant, st)
	hv, pv := followState(env, &s.hoverF, st.hovered), followState(env, &s.pressF, st.pressed)
	drawControlBody(env, gtx, r, gtx.Dp(theme.RadiusMD), look, border, hv, pv)

	padX, iconGap := buttonMetrics(p.Size)
	iconD := buttonIconSize(p.Size)
	slot := 0
	x := gtx.Dp(padX)
	if p.Icon != "" {
		slot = gtx.Dp(iconD)
	}
	labelY := centerLabelY(env, gtx, theme.TypeLabel(), h)
	drawLabel := func(x int) {
		at(gtx, image.Pt(x, labelY), func(gtx *layout.Context) {
			drawText(env, gtx, theme.TypeLabel(), look.fg, p.Text, false)
		})
	}

	switch {
	case st.busy && !st.disabled && slot > 0:
		// Busy with an icon: the spinner takes the icon's slot, the label
		// stays put (§4.1, §6.2).
		drawSpinner(env, gtx, &s.spin, image.Pt(x, (h-slot)/2), slot, look.fg)
		x += slot + gtx.Dp(iconGap)
		drawLabel(x)
	case st.busy && !st.disabled:
		// Busy without an icon: the spinner replaces the content, centered
		// in the same box, so the width still never changes.
		box := gtx.Dp(theme.IconMD)
		drawSpinner(env, gtx, &s.spin, image.Pt((w-box)/2, (h-box)/2), box, look.fg)
	default:
		if slot > 0 {
			if err := drawIconAt(env, gtx, image.Pt(x, (h-slot)/2), p.Icon, iconD, look.fg); err != nil {
				return layout.Dimensions{}, err
			}
			x += slot + gtx.Dp(iconGap)
		}
		drawLabel(x)
	}

	if st.focused {
		focusRing(env, gtx, r, gtx.Dp(theme.RadiusMD))
	}
	return layout.Dimensions{Size: image.Pt(w, h), Baseline: (h + labelLine(env, gtx, theme.TypeLabel())) / 2}, nil
}

// buttonLookOf resolves the color pairing of one Button state (UK-03): the
// base fill of the variant, the hover and press overlays of §3.5 on top of
// it, and the label and icon color. Disabled drops the fill and reads
// FgDisabled (§4.1); busy changes only the content, never the pairing.
// Ghost rests on FgMuted and lifts to Fg under interaction, so the label
// keeps 4.5:1 over its wash on every chrome surface (UK-E04). The bool is
// secondary's hairline border.
func buttonLookOf(env *Env, v ButtonVariant, st controlState) (controlLook, bool) {
	c := env.Th.Chrome()
	l := controlLook{hover: c.HoverWash, press: c.PressedWash}
	border := false
	switch v {
	case Primary:
		l.fill, l.base, l.fg = true, c.Action, c.ActionFg
		l.hover, l.press = c.ActionHover, c.ActionPressed
	case Secondary:
		l.fg = c.Fg
		border = true
	case Ghost:
		l.fg = c.FgMuted
		if st.hovered || st.pressed {
			l.fg = c.Fg
		}
	case Danger:
		l.fill, l.base, l.fg = true, env.Th.Status().Danger, c.ActionFg
	}
	if st.disabled {
		l.fill = false
		l.fg = c.FgDisabled
	}
	return l, border
}

// controlHeight is the height token of a §3.4 control size.
func controlHeight(gtx *layout.Context, s ButtonSize) int {
	switch s {
	case SizeSM:
		return gtx.Dp(theme.ControlSM)
	case SizeMD:
		return gtx.Dp(theme.ControlMD)
	case SizeLG:
		return gtx.Dp(theme.ControlLG)
	}
	return gtx.Dp(theme.ControlMD)
}

// buttonMetrics are the §6.2 paddings of a size: sm uses the dense inner
// spacing, md and lg the wide one.
func buttonMetrics(s ButtonSize) (padX, iconGap unit.Dp) {
	if s == SizeSM {
		return theme.SpaceMD, theme.SpaceXS
	}
	return theme.SpaceLG, theme.SpaceSM
}

// buttonIconSize is the §3.4 icon size of a control size: IconSM inside a
// 24 dp control, IconMD from 28 dp up.
func buttonIconSize(s ButtonSize) unit.Dp {
	if s == SizeSM {
		return theme.IconSM
	}
	return theme.IconMD
}

// drawSpinner lays the busy spinner of §4.1 in a box×box slot at p: the
// spinner keeps asking for frames only while it is drawn (§6.5).
func drawSpinner(env *Env, gtx *layout.Context, sp *Spinner, p image.Point, box int, c color.NRGBA) {
	at(gtx, p, func(gtx *layout.Context) {
		sp.Layout(env, gtx, box, c)
	})
}
