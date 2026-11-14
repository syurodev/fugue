package kitspike

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/fonts"
)

// FieldVariant is the variant of a TextField (§6.8).
type FieldVariant int

const (
	FieldText FieldVariant = iota
	FieldSearch
	FieldSecret
)

// FieldProps are the per-frame properties (UK-02).
type FieldProps struct {
	Label       string
	Placeholder string
	Helper      string
	// Err is the validation message; the field shows it only after the
	// first blur or submit (§6.8: no error on an untouched empty field).
	Err      string
	Variant  FieldVariant
	Disabled bool
}

// FieldEvents is what happened this frame (UK-06).
type FieldEvents struct {
	Changed   bool
	Submitted bool
	FocusLost bool
}

// TextFieldState is the long-lived state (UK-02): one editing core
// (UK-08) — the widget.Editor — plus the touched flag and the variant
// toggles.
type TextFieldState struct {
	ed          widget.Editor
	reveal      widget.Clickable
	clear       widget.Clickable
	secretShown bool
	touched     bool
	submitted   bool
	focused     bool
}

// NewTextFieldState builds state for one single-line field.
func NewTextFieldState() *TextFieldState {
	s := &TextFieldState{}
	s.ed.SingleLine = true
	s.ed.Submit = true
	return s
}

// SetText replaces the contents programmatically.
func (s *TextFieldState) SetText(v string) { s.ed.SetText(v) }

// Text is the current value.
func (s *TextFieldState) Text() string { return s.ed.Text() }

// Touch marks the field as if it had been blurred, so a validation error
// shows; the gallery's error-shown examples and screenshots use it.
func (s *TextFieldState) Touch() { s.touched = true }

// Tag exposes the editing core's input tag for focus scripting.
func (s *TextFieldState) Tag() event.Tag { return &s.ed }

// Layout draws the field and drains its events.
func (s *TextFieldState) Layout(env *Env, gtx *layout.Context, p FieldProps) FieldEvents {
	var ev FieldEvents
	c := env.Th.Chrome()
	st := env.Th.Status()

	gtx = withEnabled(gtx, p.Disabled)

	// Editor events: select, copy, cut, paste, undo and redo are the
	// Editor's own shortcuts; Changed and Submitted are reported up (UK-08,
	// UK-06).
	for {
		e, ok := s.ed.Update(*gtx)
		if !ok {
			break
		}
		switch e.(type) {
		case widget.ChangeEvent:
			ev.Changed = true
		case widget.SubmitEvent:
			ev.Submitted = true
			s.submitted = true
		}
	}
	// Focus tracking: report the loss, and mark touched on it.
	focused := gtx.Focused(&s.ed)
	if s.focused && !focused {
		ev.FocusLost = true
		s.touched = true
	}
	s.focused = focused

	// Escape clears a search field (§6.8).
	for {
		e, ok := gtx.Event(key.Filter{Focus: &s.ed, Name: key.NameEscape})
		if !ok {
			break
		}
		if e.(key.Event).State == key.Press && p.Variant == FieldSearch && s.ed.Len() > 0 {
			s.ed.SetText("")
			ev.Changed = true
		}
	}

	// Variant toggles.
	s.ed.Mask = 0
	if p.Variant == FieldSecret {
		if s.reveal.Clicked(*gtx) {
			s.secretShown = !s.secretShown
		}
		if !s.secretShown {
			s.ed.Mask = '•'
		}
	}
	if p.Variant == FieldSearch && s.clear.Clicked(*gtx) {
		s.ed.SetText("")
		ev.Changed = true
	}

	// Geometry.
	boxH := dp(gtx, ControlMD)
	w := gtx.Constraints.Max.X

	// Error shows only after blur or submit (§6.8).
	showError := (s.touched || s.submitted) && p.Err != ""

	// Label above.
	y := 0
	if p.Label != "" {
		lh := at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
			return drawText(gtx, env.Sh, TypeSmall, w, p.Label, c.FgMuted)
		})
		y += lh.Size.Y + dp(gtx, SpaceXXS)
	}

	// The box.
	box := image.Rect(0, y, w, y+boxH)
	bg := c.CanvasBg
	if p.Disabled {
		bg = fade(c.Fg, 0.04)
	}
	fillRRect(gtx.Ops, box, dp(gtx, RadiusMD), bg)
	borderCol := c.Border
	if showError {
		borderCol = st.Danger
	}
	if focused {
		borderCol = c.FocusBorder
	}
	strokeRRect(gtx.Ops, box, dp(gtx, RadiusMD), float32(dp(gtx, Hairline)), borderCol)

	// Editor inside the box; the search icon takes the left slot, the
	// reveal/clear button the right one.
	left := dp(gtx, SpaceMD)
	right := dp(gtx, SpaceMD)
	if p.Variant == FieldSearch {
		iconBox := dp(gtx, IconSM)
		at(gtx, image.Pt(left, y+(boxH-iconBox)/2), func(gtx *layout.Context) layout.Dimensions {
			_ = drawIconFresh(gtx, "search", IconSM, c.IconMuted)
			return layout.Dimensions{Size: image.Pt(iconBox, iconBox), Baseline: 0}
		})
		left += iconBox + dp(gtx, SpaceSM)
	}
	if p.Variant == FieldSecret {
		right += dp(gtx, HitMin)
		toggleName := "eye-off"
		if s.secretShown {
			toggleName = "eye"
		}
		hit := dp(gtx, HitMin)
		icon := dp(gtx, IconSM)
		pad := (hit - icon) / 2
		at(gtx, image.Pt(w-right, y+(boxH-hit)/2), func(gtx *layout.Context) layout.Dimensions {
			return s.reveal.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
				at(&gtx, image.Pt(pad, pad), func(gtx *layout.Context) layout.Dimensions {
					_ = drawIconFresh(gtx, toggleName, IconSM, c.IconMuted)
					return layout.Dimensions{Size: image.Pt(icon, icon), Baseline: 0}
				})
				return layout.Dimensions{Size: image.Pt(hit, hit), Baseline: 0}
			})
		})
	}
	if p.Variant == FieldSearch && s.ed.Len() > 0 {
		right += dp(gtx, HitMin)
		hit := dp(gtx, HitMin)
		icon := dp(gtx, IconSM)
		pad := (hit - icon) / 2
		at(gtx, image.Pt(w-right, y+(boxH-hit)/2), func(gtx *layout.Context) layout.Dimensions {
			return s.clear.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
				at(&gtx, image.Pt(pad, pad), func(gtx *layout.Context) layout.Dimensions {
					_ = drawIconFresh(gtx, "x", IconSM, c.IconMuted)
					return layout.Dimensions{Size: image.Pt(icon, icon), Baseline: 0}
				})
				return layout.Dimensions{Size: image.Pt(hit, hit), Baseline: 0}
			})
		})
	}

	inner := image.Rect(box.Min.X+left, box.Min.Y+dp(gtx, SpaceXS), box.Max.X-right, box.Max.Y-dp(gtx, SpaceXS))
	editGtx := *gtx
	editGtx.Constraints = layout.Constraints{Min: image.Point{}, Max: inner.Max.Sub(inner.Min)}
	defer op.Offset(inner.Min).Push(gtx.Ops).Pop()

	// Text and caret in Fg (or the disabled tone), selection in a wash.
	fg := c.Fg
	if p.Disabled {
		fg = FgDisabled(env.Th)
	}
	m := op.Record(gtx.Ops)
	paint.ColorOp{Color: fg}.Add(gtx.Ops)
	textMat := m.Stop()
	sm := op.Record(gtx.Ops)
	paint.ColorOp{Color: fade(c.Fg, 0.18)}.Add(gtx.Ops)
	selMat := sm.Stop()

	face := font.Font{Typeface: fonts.Sans, Style: font.Regular, Weight: font.Normal}
	edDims := s.ed.Layout(editGtx, env.Sh, face, TypeBody.Size, textMat, selMat)
	if s.ed.Len() == 0 && p.Placeholder != "" {
		at(gtx, image.Pt(0, (inner.Dy()-edDims.Size.Y)/2), func(gtx *layout.Context) layout.Dimensions {
			return drawText(gtx, env.Sh, TypeBody, inner.Dx(), p.Placeholder, c.FgSubtle)
		})
	}

	// Helper or error below.
	bottom := box.Max.Y
	if showError {
		mh := at(gtx, image.Pt(0, bottom+dp(gtx, SpaceXXS)), func(gtx *layout.Context) layout.Dimensions {
			return drawText(gtx, env.Sh, TypeSmall, w, p.Err, st.Danger)
		})
		bottom += mh.Size.Y + dp(gtx, SpaceXXS)
	} else if p.Helper != "" {
		mh := at(gtx, image.Pt(0, bottom+dp(gtx, SpaceXXS)), func(gtx *layout.Context) layout.Dimensions {
			return drawText(gtx, env.Sh, TypeSmall, w, p.Helper, c.FgMuted)
		})
		bottom += mh.Size.Y + dp(gtx, SpaceXXS)
	}

	// Keyboard focus ring, inside the box edge (§3.4).
	if focused && env.FocusVisible(gtx, &s.ed) {
		focusRing(env, gtx, box, dp(gtx, RadiusMD))
	}
	return ev
}

// withEnabled returns gtx with input disabled when disabled is true.
func withEnabled(gtx *layout.Context, disabled bool) *layout.Context {
	if disabled {
		d := gtx.Disabled()
		return &d
	}
	return gtx
}
