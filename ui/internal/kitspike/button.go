package kitspike

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"
)

// ButtonVariant is the visual variant of a button (§6.2).
type ButtonVariant int

const (
	// Primary is the one main action of a frame or dialog.
	Primary ButtonVariant = iota
	// Secondary is the default variant.
	Secondary
	// Ghost is for secondary actions in toolbars and rows.
	Ghost
	// Danger is for data-destroying actions.
	Danger
)

// ButtonSize picks a size.control token.
type ButtonSize int

const (
	SizeSM ButtonSize = iota
	SizeMD
	SizeLG
)

// ButtonProps are the per-frame, caller-owned properties (UK-02): rebuilding
// this struct every frame is the intended use. It carries no pointer or
// focus state.
type ButtonProps struct {
	Text     string
	Icon     string // a Lucide name; empty for none
	Variant  ButtonVariant
	Size     ButtonSize
	Busy     bool
	Disabled bool
	// Error draws the danger border of §4.1; the message itself belongs to
	// the caller, below the button.
	Error bool
}

// ButtonState is the long-lived state the caller keeps per logical button
// (UK-02), keyed by the object's stable ID in lists.
type ButtonState struct {
	click widget.Clickable
	spin  spinner
}

// NewButtonState builds state for one button.
func NewButtonState() *ButtonState { return &ButtonState{} }

// Tag exposes the input tag for focus scripting in the gallery and tests.
func (s *ButtonState) Tag() event.Tag { return &s.click }

// Layout draws the button and reports whether it was activated this frame
// (UK-06): by pointer click, or by Enter and Space while focused (Space only
// on release, per §6.2 — widget.Clickable does both). A disabled button
// activates nothing and keeps the caller's width from the props.
func (s *ButtonState) Layout(env *Env, gtx *layout.Context, p ButtonProps) bool {
	var clicked bool
	if p.Disabled {
		d := gtx.Disabled()
		gtx = &d
	} else {
		clicked = s.click.Clicked(*gtx)
	}
	s.click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return s.draw(env, &gtx, p)
	})
	return clicked
}

// draw paints the button body. State priority follows §4.1: disabled, busy,
// pressed, keyboard focus, hover, rest.
func (s *ButtonState) draw(env *Env, gtx *layout.Context, p ButtonProps) layout.Dimensions {
	c := env.Th.Chrome()
	st := env.Th.Status()
	tag := &s.click

	h := controlH(gtx, p.Size)
	padX, iconGap := buttonMetrics(gtx, p.Size)
	iconD := unit.Dp(14)
	if p.Size == SizeLG {
		iconD = unit.Dp(16)
	}

	// Width from the label and icon; busy swaps the icon in place so the
	// button does not jump (§6.2).
	labelW := measureText(gtx, env.Sh, TypeLabel, 1<<20, p.Text).X
	w := 2*padX + labelW
	if p.Icon != "" || p.Busy {
		w += dp(gtx, iconD) + iconGap
	}
	if w < dp(gtx, HitMin) {
		w = dp(gtx, HitMin)
	}
	r := image.Rect(0, 0, w, h)

	hover := env.Hovered(tag) && !p.Disabled
	pressed := (env.Pressed(tag) || s.click.Pressed()) && !p.Disabled
	focusVisible := env.FocusVisible(gtx, tag)

	bg, fg := buttonColors(env, p, hover, pressed)
	if p.Disabled {
		bg, fg = transparent, FgDisabled(env.Th)
		if p.Variant == Secondary || p.Variant == Ghost {
			bg = fade(c.Fg, 0.04)
		}
	}
	fillRRect(gtx.Ops, r, dp(gtx, RadiusMD), bg)
	if p.Variant == Secondary || (p.Error && p.Variant == Ghost) {
		border := c.Border
		if p.Error {
			border = st.Danger
		}
		strokeRRect(gtx.Ops, r, dp(gtx, RadiusMD), float32(dp(gtx, Hairline)), border)
	}

	// Content: [spinner|icon] gap label, centered.
	x := padX
	if p.Busy {
		box := dp(gtx, iconD)
		at(gtx, image.Pt(x, (h-box)/2), func(gtx *layout.Context) layout.Dimensions {
			s.spin.layout(env, gtx, box, fg)
			return layout.Dimensions{Size: image.Pt(box, box), Baseline: 0}
		})
		x += box + iconGap
	} else if p.Icon != "" {
		box := dp(gtx, iconD)
		at(gtx, image.Pt(x, (h-box)/2), func(gtx *layout.Context) layout.Dimensions {
			// Icons ignore the error: a missing glyph draws nothing, which
			// is the safe failure for a throwaway spike.
			_ = drawIconFresh(gtx, p.Icon, iconD, fg)
			return layout.Dimensions{Size: image.Pt(box, box), Baseline: 0}
		})
		x += box + iconGap
	}
	at(gtx, image.Pt(x, verticalCenter(gtx, TypeLabel, h)), func(gtx *layout.Context) layout.Dimensions {
		return drawText(gtx, env.Sh, TypeLabel, w, p.Text, fg)
	})

	if focusVisible {
		focusRing(env, gtx, r, dp(gtx, RadiusMD))
	}
	return layout.Dimensions{Size: image.Pt(w, h), Baseline: h - lineTextH(gtx, TypeLabel)/2}
}

// buttonColors picks the body and foreground pair for a variant in its
// current state.
func buttonColors(env *Env, p ButtonProps, hover, pressed bool) (bg, fg color.NRGBA) {
	c := env.Th.Chrome()
	st := env.Th.Status()
	switch p.Variant {
	case Primary:
		bg, fg = c.Action, c.ActionFg
		switch {
		case pressed:
			bg = c.ActionPressed
		case hover:
			bg = c.ActionHover
		}
	case Danger:
		bg, fg = st.Danger, white
		switch {
		case pressed:
			bg = mix(st.Danger, black, 0.2)
		case hover:
			bg = mix(st.Danger, black, 0.1)
		}
	case Secondary:
		bg, fg = transparent, c.Fg
		switch {
		case pressed:
			bg = PressedWash(env.Th)
		case hover:
			bg = HoverWash(env.Th)
		}
	case Ghost:
		bg, fg = transparent, c.FgMuted
		if pressed {
			bg = PressedWash(env.Th)
		} else if hover {
			bg = HoverWash(env.Th)
		}
	}
	return bg, fg
}

// controlH is the height token of a size.
func controlH(gtx *layout.Context, s ButtonSize) int {
	switch s {
	case SizeSM:
		return dp(gtx, ControlSM)
	case SizeLG:
		return dp(gtx, ControlLG)
	default:
		return dp(gtx, ControlMD)
	}
}

// buttonMetrics are the padding and icon gap tokens of §6.2 per size.
func buttonMetrics(gtx *layout.Context, s ButtonSize) (padX, iconGap int) {
	switch s {
	case SizeSM:
		return dp(gtx, ButtonPadSM), dp(gtx, IconButtonGapSM)
	default:
		return dp(gtx, ButtonPadMDLG), dp(gtx, IconButtonGapMD)
	}
}

// The wash variants draw on translucent fills, so the resting bodies of the
// quiet variants need a real transparent color.
var transparent = color.NRGBA{}

var white = color.NRGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}
var black = color.NRGBA{A: 0xff}
