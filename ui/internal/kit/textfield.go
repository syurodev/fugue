package kit

import (
	"errors"
	"fmt"
	"image"
	"image/color"
	"strconv"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TextField, ui-kit §6.8: a fixed label above (the placeholder is never the
// label), a size.control.md box at radius.md filled FillSoft with no border
// (the v0.3 flat input), an optional leading icon and trailing button, and
// helper or error text below. Variants: Text (plain), Search (magnifier,
// a clear button that fades in with text, Escape clears), Secret (masked
// by default with an explicit reveal, never prefilled, copy and cut
// refused while masked — and the value never reaches a log, because the
// kit logs nothing), Number (a unit and a valid range; an empty field is
// never 0). The §4.1 states and the §3.7 motion live in the shared core
// (editor.go): the fill lifts on Snappy, the ring fades in, helper and
// error crossfade while a new error line opens its height on Smooth.
//
// The error shows only after the field was left or a submit happened,
// then stays and updates while editing; an untouched empty field shows
// none. The caller reads the events in the frame (UK-06) and keeps one
// TextFieldState per logical field (UK-02).

// errUnknownVariant names a variant outside the §6.8 set.
var errUnknownVariant = errors.New("unknown text field variant")

// TextFieldVariant is the §6.8 variant.
type TextFieldVariant int

// The §6.8 variants.
const (
	TextFieldText   TextFieldVariant = iota // the plain input
	TextFieldSearch                         // magnifier, clear, Escape clears
	TextFieldSecret                         // masked with a reveal, never prefilled
	TextFieldNumber                         // unit and valid range
)

// TextFieldProps are the per-frame, caller-owned properties (UK-02). A
// secret field is never prefilled (§6.8): a fresh state is empty, and the
// kit fills nothing by itself. Min and Max bound the Number variant; nil
// bounds are unbounded.
type TextFieldProps struct {
	Label       string
	Placeholder string
	Helper      string
	Error       string
	Variant     TextFieldVariant
	Icon        IconName // a leading icon; Search always shows the magnifier
	Unit        string   // Number: the suffix drawn inside the box
	Min         *float64 // Number: the smallest valid value
	Max         *float64 // Number: the largest valid value
	Disabled    bool
}

// TextFieldState is the long-lived state the caller keeps per logical
// field (UK-02): the editing core, the error rule and the trailing
// buttons' pressables (clear, reveal).
type TextFieldState struct {
	core     fieldCore
	err      fieldErrorState
	revealed bool
	clear    pressable
	reveal   pressable
	// clearMv and revealMv carry the trailing buttons' motion from the
	// input phase to the draw phase of one frame.
	clearMv, revealMv motionVals
}

// NewTextFieldState builds the state for one field.
func NewTextFieldState() *TextFieldState {
	return &TextFieldState{
		core:   newFieldCore(true),
		clear:  newPressable(),
		reveal: newPressable(),
	}
}

// Text returns the field's value.
func (s *TextFieldState) Text() string { return s.core.text() }

// SetText replaces the field's value: a caller's reset. Never use it to
// prefill a Secret field (§6.8).
func (s *TextFieldState) SetText(v string) { s.core.setText(v) }

// Tag is the field's input tag, for focus scripting and gallery previews.
func (s *TextFieldState) Tag() event.Tag { return s.core.Tag() }

// MarkSubmitted marks the field submitted, as the caller does when the
// form around it is sent: the §6.8 error gate opens from then on, exactly
// as it does on the field's own Enter.
func (s *TextFieldState) MarkSubmitted() { s.err.submitted = true }

// SetRevealed shows or hides a Secret field's value (§6.8); a caller's
// reveal-all toggle drives the same state the reveal button does.
func (s *TextFieldState) SetRevealed(v bool) { s.revealed = v }

// Number returns the Number variant's value: ok is false for an empty
// field — an empty field is never 0 (§6.8) — and for text that parses as
// no number. The value is not clamped into the range; an out-of-range
// value shows through the error state instead.
func (s *TextFieldState) Number() (float64, bool) {
	txt := strings.TrimSpace(s.core.text())
	if txt == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(txt, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// Measure returns the field's natural box: the widest of its label and
// placeholder plus paddings and slots, at the full §6.8 height of label,
// box and helper line. It draws nothing and touches no input source.
func (s *TextFieldState) Measure(env *Env, gtx *layout.Context, p *TextFieldProps) layout.Dimensions {
	sz := fieldMetrics(env, gtx, p.Label, p.Helper != "")
	lead, right := textFieldSlots(env, gtx, p)
	w := fieldMinWidth(env, gtx, p.Label, p.Placeholder, lead+right)
	return layout.Dimensions{
		Size:     image.Pt(w, sz.total()),
		Baseline: sz.labelH + sz.boxPadY + labelLine(env, gtx, editorType())/2,
	}
}

// Layout draws the field at the current offset and reports the events of
// the frame (UK-06): Changed, Submitted (Enter — the field asks for the
// editor's submit translation) and FocusLost. An unknown variant is a
// programming error and comes back as an error instead of a silent plain
// field.
func (s *TextFieldState) Layout(env *Env, gtx *layout.Context, p *TextFieldProps) (FieldEvents, error) {
	if p.Variant < TextFieldText || p.Variant > TextFieldNumber {
		return FieldEvents{}, fmt.Errorf("unknown text field variant %d: %w", int(p.Variant), errUnknownVariant)
	}
	pre := fieldMetrics(env, gtx, p.Label, false)
	w := textFieldWidth(env, gtx, p)
	box := image.Rect(0, pre.labelH, w, pre.labelH+pre.boxH)

	masked := p.Variant == TextFieldSecret && !s.revealed
	s.core.ed.Mask = maskOf(masked)
	ev := s.core.begin(gtx, box, fieldOpts{
		masked:       masked,
		submit:       true,
		escapeClears: p.Variant == TextFieldSearch,
	}, p.Disabled)
	if p.Variant == TextFieldSearch {
		cf := followState(env, &s.core.clearF, s.core.text() != "")
		if cf > 0 {
			btn := trailingButtonSize(gtx)
			var clicked bool
			clicked, s.clearMv = s.clear.begin(env, gtx, image.Pt(btn, btn), pressOpts{disabled: p.Disabled})
			if clicked {
				s.core.setText("")
				ev.Changed = true
			}
		}
	}
	if p.Variant == TextFieldSecret {
		followState(env, &s.core.revealF, s.revealed)
		btn := trailingButtonSize(gtx)
		var clicked bool
		clicked, s.revealMv = s.reveal.begin(env, gtx, image.Pt(btn, btn), pressOpts{disabled: p.Disabled, toggled: s.revealed})
		if clicked {
			s.revealed = !s.revealed
		}
	}

	errored := s.err.visible(p.Error, s.outOfRange(p), ev)
	fm := s.core.advance(env, s.core.hovered(env) && !p.Disabled, s.core.focused(env, gtx) && !p.Disabled, errored, fieldLineTarget(env, gtx, p.Helper, errored))
	if err := s.draw(env, gtx, p, w, pre, errored, fm); err != nil {
		return FieldEvents{}, fmt.Errorf("draw the text field: %w", err)
	}
	return ev, nil
}

// Measure-independent layout width: the natural box of the §6.8 content
// stretched to the constraints' minimum and clamped to their maximum, so
// a form column fills and a measured cell keeps its slot.
func textFieldWidth(env *Env, gtx *layout.Context, p *TextFieldProps) int {
	lead, right := textFieldSlots(env, gtx, p)
	w := fieldMinWidth(env, gtx, p.Label, p.Placeholder, lead+right)
	if mn := gtx.Constraints.Min.X; mn > w {
		w = mn
	}
	return min(w, gtx.Constraints.Max.X)
}

// textFieldSlots are the px room the variant's leading icon and trailing
// element take inside the box, so the text never slides when a button or
// a unit appears (§6.8 keeps the box steady).
func textFieldSlots(env *Env, gtx *layout.Context, p *TextFieldProps) (lead, right int) {
	if p.Variant == TextFieldSearch || p.Icon != "" {
		lead = gtx.Dp(theme.IconMD) + gtx.Dp(theme.SpaceSM)
	}
	btn := trailingButtonSize(gtx)
	switch p.Variant {
	case TextFieldSearch, TextFieldSecret:
		right = gtx.Dp(theme.SpaceXS) + btn + gtx.Dp(theme.SpaceSM)
	case TextFieldNumber:
		if p.Unit != "" {
			right = gtx.Dp(theme.SpaceXS) + measureLabel(env, gtx, theme.TypeSmall(), p.Unit).X + gtx.Dp(theme.SpaceSM)
		}
	case TextFieldText:
		right = gtx.Dp(theme.SpaceLG)
	}
	return lead, right
}

// trailingButtonSize is the px side of the in-box clear and reveal
// buttons: the small control token, never under size.hit.min (§3.4).
func trailingButtonSize(gtx *layout.Context) int {
	return max(gtx.Dp(theme.ControlSM), gtx.Dp(theme.HitMin))
}

// outOfRange reports a parsed number outside the caller's range (§6.8);
// it feeds the error rule, so it surfaces only after the field was left
// or a submit happened.
func (s *TextFieldState) outOfRange(p *TextFieldProps) bool {
	if p.Variant != TextFieldNumber || p.Min == nil && p.Max == nil {
		return false
	}
	v, ok := s.Number()
	if !ok {
		return false
	}
	return p.Min != nil && v < *p.Min || p.Max != nil && v > *p.Max
}

// fieldLineTarget is the target height of the below-line: present when a
// helper stands or the error shows (§6.8), zero otherwise.
func fieldLineTarget(env *Env, gtx *layout.Context, helper string, errored bool) float32 {
	if helper == "" && !errored {
		return 0
	}
	return float32(labelLine(env, gtx, theme.TypeSmall()))
}

// draw paints the whole field at the width Layout resolved: the label,
// the box with its fill, border and ring, the leading icon, the editor
// (and the placeholder while empty), the unit, the trailing buttons and
// the helper/error line.
func (s *TextFieldState) draw(env *Env, gtx *layout.Context, p *TextFieldProps, w int, sz fieldSizes, errored bool, fm fieldMotion) error {
	c := env.Th.Chrome()
	look := fieldLookOf(env, p.Disabled, errored)
	if p.Label != "" {
		at(gtx, image.Pt(0, 0), func(gtx *layout.Context) {
			drawText(env, gtx, theme.TypeSmall(), fade(c.FgMuted, dimOf(p.Disabled)), p.Label, false)
		})
	}
	paintBox(gtx, image.Rect(0, sz.labelH, w, sz.labelH+sz.boxH), look, fm)

	ig := *gtx
	if p.Disabled {
		ig = gtx.Disabled()
	}
	if err := s.drawInside(env, &ig, p, w, sz, look, p.Disabled); err != nil {
		return err
	}
	if err := s.drawTrailing(env, &ig, p, w, sz, p.Disabled); err != nil {
		return err
	}
	at(gtx, image.Pt(0, sz.labelH+sz.boxH+int(sz.lineGapPx+0.5)), func(gtx *layout.Context) {
		belowLine(env, gtx, w, p.Helper, p.Error, c.FgSubtle, env.Th.Status().Danger, fm)
	})
	return nil
}

// drawInside draws what the box holds left of the trailing element: the
// leading icon, the editor's text — with the placeholder under it while
// the field is empty — and the Number variant's unit.
func (s *TextFieldState) drawInside(env *Env, gtx *layout.Context, p *TextFieldProps, w int, sz fieldSizes, look controlLook, disabled bool) error {
	c := env.Th.Chrome()
	lead, right := textFieldSlots(env, gtx, p)
	top := sz.labelH
	textX := sz.padX + lead
	textW := max(w-right-textX, 0)
	if p.Variant == TextFieldSearch || p.Icon != "" {
		pt := image.Pt(sz.padX, top+(sz.boxH-gtx.Dp(theme.IconMD))/2)
		if err := drawIconAt(env, gtx, pt, leadingIcon(p), theme.IconMD, fade(c.IconMuted, dimOf(disabled))); err != nil {
			return fmt.Errorf("draw the leading icon: %w", err)
		}
	}
	if s.core.text() == "" && p.Placeholder != "" {
		at(gtx, image.Pt(textX, top+sz.boxPadY), func(gtx *layout.Context) {
			drawText(env, gtx, editorType(), fade(c.Placeholder, dimOf(disabled)), p.Placeholder, false)
		})
	}
	at(gtx, image.Pt(textX, top+sz.boxPadY), func(gtx *layout.Context) {
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
		drawEditor(env, &g, &s.core.ed, look.fg, fade(c.SelectedWash, dimOf(disabled)))
	})
	if p.Variant == TextFieldNumber && p.Unit != "" {
		uw := measureLabel(env, gtx, theme.TypeSmall(), p.Unit).X
		pt := image.Pt(w-gtx.Dp(theme.SpaceXS)-uw, top+(sz.boxH-labelLine(env, gtx, theme.TypeSmall()))/2)
		at(gtx, pt, func(gtx *layout.Context) {
			drawText(env, gtx, theme.TypeSmall(), fade(c.FgMuted, dimOf(disabled)), p.Unit, false)
		})
	}
	return nil
}

// drawTrailing draws the variant's trailing control: the search clear
// button fading in with text, the secret reveal with its eye crossfade.
// Their input ran in Layout; this is the painting half of the frame.
func (s *TextFieldState) drawTrailing(env *Env, gtx *layout.Context, p *TextFieldProps, w int, sz fieldSizes, disabled bool) error {
	btn := trailingButtonSize(gtx)
	top := sz.labelH + (sz.boxH-btn)/2
	switch p.Variant {
	case TextFieldSearch:
		if cf := s.core.clearF.Value(); cf > 0 {
			r := image.Rect(w-gtx.Dp(theme.SpaceXS)-btn, top, w-gtx.Dp(theme.SpaceXS), top+btn)
			l := iconButtonLookOf(env, IconButtonGhost, false, disabled).dim(cf)
			var err error
			s.clear.paint(env, gtx, r, gtx.Dp(theme.RadiusSM), l, s.clearMv, func(fg color.NRGBA) {
				// The paint body sits at r; the content draws from r's origin,
				// like a Button's does from its own box's.
				at(gtx, r.Min, func(gtx *layout.Context) {
					pt := image.Pt((btn-gtx.Dp(theme.IconSM))/2, (btn-gtx.Dp(theme.IconSM))/2)
					if e := drawIconAt(env, gtx, pt, IconClose, theme.IconSM, fg); e != nil {
						err = fmt.Errorf("draw the clear icon: %w", e)
					}
				})
			})
			return err
		}
	case TextFieldSecret:
		r := image.Rect(w-gtx.Dp(theme.SpaceXS)-btn, top, w-gtx.Dp(theme.SpaceXS), top+btn)
		l := iconButtonLookOf(env, IconButtonGhost, s.revealed, disabled)
		rv := s.core.revealF.Value()
		var err error
		s.reveal.paint(env, gtx, r, gtx.Dp(theme.RadiusSM), l, s.revealMv, func(fg color.NRGBA) {
			at(gtx, r.Min, func(gtx *layout.Context) {
				pt := image.Pt((btn-gtx.Dp(theme.IconMD))/2, (btn-gtx.Dp(theme.IconMD))/2)
				if e := crossfadeIcons(env, gtx, pt, theme.IconMD, IconEye, IconEyeOff, fg, rv); e != nil {
					err = fmt.Errorf("draw the reveal icon: %w", e)
				}
			})
		})
		return err
	case TextFieldText, TextFieldNumber:
	}
	return nil
}

// maskOf is the editor's Mask: the bullet while masked, none revealed.
func maskOf(masked bool) rune {
	if masked {
		return secretMask
	}
	return 0
}

// dimOf is the alpha of a disabled part of a field (§4.1): the whole
// control draws at DisabledOpacity.
func dimOf(disabled bool) float32 {
	if disabled {
		return theme.DisabledOpacity
	}
	return 1
}

// leadingIcon names the leading icon of a frame: Search always carries the
// magnifier (§6.8); the other variants take the caller's Icon.
func leadingIcon(p *TextFieldProps) IconName {
	if p.Variant == TextFieldSearch {
		return IconSearch
	}
	return p.Icon
}
