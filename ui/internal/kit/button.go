package kit

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Button, ui-kit §6.2: an optional icon and a type.label label on a
// size.control box with radius.md, in six variants and the three §3.4
// sizes. It shows every §4.1 state — rest, hover, pressed, the focus ring
// outside the body (only after Tab, via Env.FocusVisible), disabled (the
// whole control at DisabledOpacity) and busy — and reports Clicked during
// the frame that saw it (UK-06). Enter and Space activate while focused,
// Space on release: widget.Clickable owns both.
//
// Motion (§3.7): fills and the label color change on Snappy, a press shrinks
// the drawing — never the hit area — to motion.press and sends a ripple
// from the press point, the ring fades in, and the icon and the spinner
// cross-fade when busy begins or ends. Busy keeps the idle-measured box, so
// a toolbar never jumps; the spinner asks for frames only while drawn.
// Reduced motion makes all of it instant.
//
// Props and state are separate values (UK-02): the caller rebuilds
// ButtonProps every frame and keeps one ButtonState per logical button for
// its lifetime — in lists, keyed by the object's stable ID, never by index.
// A Button draws outside its box (the ring), so no parent may clip it.

// ButtonVariant is the visual variant (§6.2): Secondary is the default tinted
// fill, Primary is the one main action of a frame or dialog, Bordered adds a
// faint border to a transparent body, Ghost shows its body only on hover,
// and Danger marks data-destroying actions (never the busiest button of a
// frequently used screen).
type ButtonVariant int

// The §6.2 variants.
const (
	Secondary ButtonVariant = iota // the default variant
	Primary
	Bordered
	Ghost
	Danger
	// Link is an underlined, transparent button for a quiet secondary
	// action beside a primary one ("Use recovery key"): it reads as
	// clickable where a Ghost resting on FgMuted reads as a plain label.
	Link
)

// ButtonSize picks a size.control token (§3.4).
type ButtonSize int

// The §3.4 control sizes.
const (
	SizeMD ButtonSize = iota // the default (28 dp)
	SizeSM                   // toolbar and in-row (24 dp)
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
	// Block stretches the button to the full width the layout allows, for
	// the one main action of a panel.
	Block bool
	// Wrap lets a long label wrap across lines within the available width
	// instead of overflowing the container.
	Wrap bool
}

// ButtonState is the long-lived state the caller keeps per logical button
// (UK-02): the clickable, the motion followers and the busy spinner.
type ButtonState struct {
	pressable
	spin  Spinner
	busyF motion.Follower
}

// NewButtonState builds the state for one button.
func NewButtonState() *ButtonState {
	return &ButtonState{pressable: newPressable(), busyF: motion.NewFollower(motion.Snappy())}
}

// Measure returns the box the button draws from its idle content: the
// number measured layouts need, without drawing or touching the input
// source. Busy never changes it (§6.2).
func (s *ButtonState) Measure(env *Env, gtx *layout.Context, p ButtonProps) layout.Dimensions {
	spec := computeButtonBox(env, gtx, p)
	return layout.Dimensions{Size: image.Pt(spec.w, spec.h), Baseline: (spec.h + labelLine(env, gtx, theme.TypeLabel())) / 2}
}

type buttonBoxSpec struct {
	w        int
	h        int
	cw       int
	textMaxW int
	wrapped  bool
}

// buttonBox is the idle box of §6.2: the height token of the size, twice
// the padding, the label and — when there is one — the icon slot with its
// gap, at least HitMin wide. cw is the content's width inside it.
func buttonBox(env *Env, gtx *layout.Context, p ButtonProps) (w, h, cw int) {
	spec := computeButtonBox(env, gtx, p)
	return spec.w, spec.h, spec.cw
}

func computeButtonBox(env *Env, gtx *layout.Context, p ButtonProps) buttonBoxSpec {
	padX, iconGap := buttonMetrics(p.Size)
	h := controlHeight(gtx, p.Size)
	single := measureLabel(env, gtx, theme.TypeLabel(), p.Text)
	iconW := 0
	if p.Icon != "" {
		iconW = gtx.Dp(buttonIconSize(p.Size)) + gtx.Dp(iconGap)
	}
	cw := single.X + iconW
	w := max(cw+2*gtx.Dp(padX), gtx.Dp(theme.HitMin))
	if p.Wrap && gtx.Constraints.Max.X > 0 && gtx.Constraints.Max.X < 1<<20 && w > gtx.Constraints.Max.X {
		w = gtx.Constraints.Max.X
		textMaxW := max(w-2*gtx.Dp(padX)-iconW, 1)
		wrapped := measureWrappedLabel(env, gtx, theme.TypeLabel(), p.Text, textMaxW)
		padY := max(h-single.Y, gtx.Dp(theme.SpaceXS))
		h = max(h, wrapped.Y+padY)
		return buttonBoxSpec{w: w, h: h, cw: textMaxW + iconW, textMaxW: textMaxW, wrapped: true}
	}
	if p.Block && gtx.Constraints.Max.X > w && gtx.Constraints.Max.X < 1<<20 {
		w = gtx.Constraints.Max.X
	}
	return buttonBoxSpec{w: w, h: h, cw: cw, textMaxW: single.X}
}

func measureWrappedLabel(env *Env, gtx *layout.Context, t theme.Type, txt string, maxW int) image.Point {
	g := *gtx
	g.Constraints.Min = image.Point{}
	g.Constraints.Max.X = maxW
	m := op.Record(gtx.Ops)
	dims := drawTextAligned(env, &g, t, env.Th.Chrome().Fg, txt, text.WrapWords, text.Middle)
	m.Stop()
	return dims.Size
}

// Layout draws the button at the current offset and reports whether it was
// activated this frame (UK-06): by pointer click, or by Enter and Space
// while focused. Busy blocks repeat activation (§4.1). An unknown icon is a
// programming error and comes back as an error instead of a button with no
// glyph.
func (s *ButtonState) Layout(env *Env, gtx *layout.Context, p ButtonProps) (bool, error) {
	spec := computeButtonBox(env, gtx, p)
	busy := p.Busy && !p.Disabled
	clicked, mv := s.begin(env, gtx, image.Pt(spec.w, spec.h), pressOpts{disabled: p.Disabled, busy: p.Busy})
	swap := followState(env, &s.busyF, busy)
	var err error
	s.paint(env, gtx, image.Rect(0, 0, spec.w, spec.h), gtx.Dp(theme.RadiusMD), buttonLookOf(env, p.Variant, p.Disabled), mv,
		func(fg color.NRGBA) { err = s.content(env, gtx, p, fg, spec, swap) })
	if err != nil {
		return false, fmt.Errorf("draw the button: %w", err)
	}
	return clicked, nil
}

// content draws the label and the icon or spinner in fg. swap is the busy
// cross-fade, 0 idle to 1 busy: with an icon the spinner takes the icon's
// slot and the label stays put (§4.1, §6.2); without one the spinner fades
// in over the fading label, centered in the same box, so the width never
// changes. Entering content scales up from 0.8, leaving content down to it.
func (s *ButtonState) content(env *Env, gtx *layout.Context, p ButtonProps, fg color.NRGBA, spec buttonBoxSpec, swap float32) error {
	_, iconGap := buttonMetrics(p.Size)
	x := (spec.w - spec.cw) / 2
	labelH := buttonLabelHeight(env, gtx, p.Text, spec)
	labelAt := image.Pt(x, (spec.h-labelH)/2)
	drawLabel := func(a float32) {
		at(gtx, labelAt, func(gtx *layout.Context) {
			dims := drawButtonLabel(env, gtx, theme.TypeLabel(), fade(fg, a), p.Text, spec)
			recordRun(env, image.Rectangle{Max: dims.Size}.Add(labelAt))
			if p.Variant == Link {
				underline(gtx, dims.Size, fade(fg, a))
			}
		})
	}
	if p.Icon == "" {
		if swap < 1 {
			drawLabel(1 - swap)
		}
		box := gtx.Dp(theme.IconMD)
		s.spinner(env, gtx, image.Pt((spec.w-box)/2, (spec.h-box)/2), box, fade(fg, swap), swap)
		return nil
	}
	iconD := buttonIconSize(p.Size)
	slot := gtx.Dp(iconD)
	pt := image.Pt(x, (spec.h-slot)/2)
	if swap < 1 {
		if err := drawIconScaled(env, gtx, pt, p.Icon, iconD, fade(fg, 1-swap), 0.8+0.2*(1-swap)); err != nil {
			return err
		}
	}
	labelAt.X = x + slot + gtx.Dp(iconGap)
	drawLabel(1)
	s.spinner(env, gtx, pt, slot, fade(fg, swap), swap)
	return nil
}

func buttonLabelHeight(env *Env, gtx *layout.Context, txt string, spec buttonBoxSpec) int {
	if spec.wrapped {
		return measureWrappedLabel(env, gtx, theme.TypeLabel(), txt, spec.textMaxW).Y
	}
	return measureLabel(env, gtx, theme.TypeLabel(), txt).Y
}

func drawButtonLabel(env *Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string, spec buttonBoxSpec) layout.Dimensions {
	if spec.wrapped {
		g := *gtx
		g.Constraints.Min = image.Point{X: spec.textMaxW}
		g.Constraints.Max.X = spec.textMaxW
		return drawTextAligned(env, &g, t, c, txt, text.WrapWords, text.Middle)
	}
	return drawText(env, gtx, t, c, txt, false)
}

// spinner draws the busy ring in the box×box slot at p while the
// cross-fade shows any of it: it asks for frames only while drawn (§6.5).
func (s *ButtonState) spinner(env *Env, gtx *layout.Context, p image.Point, box int, c color.NRGBA, swap float32) {
	if swap <= 0 {
		return
	}
	at(gtx, p, func(gtx *layout.Context) {
		center := f32.Pt(float32(box)/2, float32(box)/2)
		defer scaleAbout(gtx, center, 0.8+0.2*swap).Pop()
		s.spin.Layout(env, gtx, box, c)
	})
}

// buttonLookOf resolves the color pairing of one Button variant (UK-03):
// primary on Action with its hover and press fills, secondary on FillSoft,
// bordered on BorderSubtle over nothing, ghost on nothing until hover, and
// danger on DangerSoft in DangerFg, deepening to its hover and press fills.
// The focus ring takes the control's own tone, always solid (§3.8): Action for
// primary, the solid Status.Danger — never DangerSoft — for danger, FocusBorder
// for the rest. Disabled
// draws the whole look at DisabledOpacity (§4.1). Ghost rests on FgMuted
// and lifts to Fg under interaction, so the label keeps 4.5:1 over its wash
// on every chrome surface (UK-E04).
func buttonLookOf(env *Env, v ButtonVariant, disabled bool) controlLook {
	c := env.Th.Chrome()
	l := controlLook{hover: c.HoverWash, press: c.PressedWash, fg: c.Fg, ripple: c.RippleWash, ring: c.FocusBorder}
	switch v {
	case Primary:
		l.base, l.hover, l.press, l.fg, l.ripple = c.Action, c.ActionHover, c.ActionPressed, c.ActionFg, c.RippleOnAction
		l.ring = c.Action
	case Secondary:
		l.base, l.hover = c.FillSoft, c.FillSoftHover
	case Bordered:
		l.border, l.hover, l.press = c.BorderSubtle, c.FillSoft, c.FillSoftHover
	case Ghost:
		l.fg, l.fgHover = c.FgMuted, c.Fg
	case Link:
		l.fg = c.Fg
	case Danger:
		l.base, l.hover, l.press, l.fg = c.DangerSoft, c.DangerSoftHover, c.DangerSoftPressed, c.DangerFg
		l.ring = env.Th.Status().Danger
	}
	if l.fgHover.A == 0 {
		l.fgHover = l.fg
	}
	if disabled {
		l = l.dim(theme.DisabledOpacity)
	}
	return l
}

// controlHeight is the height token of a §3.4 control size.
func controlHeight(gtx *layout.Context, s ButtonSize) int {
	if s == SizeSM {
		return gtx.Dp(theme.ControlSM)
	}
	if s == SizeLG {
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

// underline draws a 1 dp rule under a label of the given size at the current
// offset, the Link variant's mark.
func underline(gtx *layout.Context, size image.Point, c color.NRGBA) {
	y := size.Y - gtx.Dp(theme.SpaceXXS)
	r := image.Rect(0, y, size.X, y+max(gtx.Dp(1), 1))
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, c)
}
