package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TextAreaProps is TextFieldProps's flowing sibling on the same editing
// core, ui-kit §6.9 (UK-08). Enter inserts a newline; the submit key belongs to the
// caller — the core never reports Submitted here (the composer wires its
// own key, F10.4b). The box grows with its content to MaxLines lines and
// then scrolls inside: the editor is laid out constrained to the cap, so
// its viewport clamps and its own wheel scrolling takes over, while the
// box's height follows the content on Smooth (§3.7) and clips what it is
// not tall enough for yet.
type TextAreaProps struct {
	Label       string
	Placeholder string
	Helper      string
	Error       string
	// MaxLines caps the box: at most this many lines are visible and the
	// rest scrolls inside (§6.9). Zero means uncapped — the box grows with
	// its content.
	MaxLines int
	Disabled bool
}

// TextAreaState is the long-lived state the caller keeps per logical
// field (UK-02).
type TextAreaState struct {
	core fieldCore
	err  fieldErrorState
	// boxF follows the content height on Smooth (§6.9); list is the
	// in-box scroller, active past the cap.
	boxF motion.Follower
	list layout.List
}

// NewTextAreaState builds the state for one area.
func NewTextAreaState() *TextAreaState {
	l := layout.List{Axis: layout.Vertical}
	return &TextAreaState{core: newFieldCore(false), boxF: motion.NewFollower(motion.Smooth()), list: l}
}

// Text returns the area's value.
func (s *TextAreaState) Text() string { return s.core.text() }

// SetText replaces the area's value.
func (s *TextAreaState) SetText(v string) { s.core.setText(v) }

// Tag is the area's input tag, for focus scripting and gallery previews.
func (s *TextAreaState) Tag() event.Tag { return s.core.Tag() }

// MarkSubmitted marks the area submitted, as the caller does when the
// form around it is sent: the §6.8 error gate opens from then on.
func (s *TextAreaState) MarkSubmitted() { s.err.submitted = true }

// Measure returns the area's natural box at its current content: the
// editor laid out into a throwaway macro with a dead input source, so
// nothing paints and no event is consumed, plus the label and the helper
// line.
func (s *TextAreaState) Measure(env *Env, gtx *layout.Context, p *TextAreaProps) layout.Dimensions {
	sz := fieldMetrics(env, gtx, p.Label, p.Helper != "")
	w := fieldMinWidth(env, gtx, p.Label, p.Placeholder, 0)
	body := s.bodyHeight(env, gtx, w)
	return layout.Dimensions{
		Size:     image.Pt(w, sz.labelH+min(body, 1<<20)+2*sz.boxPadY+int(sz.lineGapPx+sz.linePx+0.5)),
		Baseline: sz.labelH + sz.boxPadY + labelLine(env, gtx, editorType())/2,
	}
}

// Layout draws the area at the current offset and reports the events of
// the frame (UK-06). Submitted is never set: Enter inserts a newline and
// the submit key belongs to the caller (§6.9).
func (s *TextAreaState) Layout(env *Env, gtx *layout.Context, p *TextAreaProps) FieldEvents {
	pre := fieldMetrics(env, gtx, p.Label, false)
	w := textAreaWidth(env, gtx, p)
	maxBody := 1 << 20
	if p.MaxLines > 0 {
		_, line := env.Content(editorType())
		maxBody = p.MaxLines * gtx.Sp(line)
	}
	full := s.bodyHeight(env, gtx, w)
	content := min(full, maxBody)
	scrolls := full > maxBody
	boxH := int(followSmooth(env, &s.boxF, float32(content+2*pre.boxPadY)) + 0.5)
	box := image.Rect(0, pre.labelH, w, pre.labelH+boxH)

	ev := s.core.begin(gtx, box, fieldOpts{}, p.Disabled)
	errored := s.err.visible(p.Error, false, ev)
	fm := s.core.advance(env, s.core.hovered(env) && !p.Disabled, s.core.focused(env, gtx) && !p.Disabled, errored, fieldLineTarget(env, gtx, p.Helper, errored))
	s.draw(env, gtx, p, pre, boxH, maxBody, scrolls, errored, fm)
	return ev
}

// bodyHeight lays the editor into a throwaway macro with a dead input
// source and returns its full text height: the number the box grows
// around, capped by the caller at the point of use, and the number that
// says whether the in-box scroller is needed at all.
func (s *TextAreaState) bodyHeight(env *Env, gtx *layout.Context, w int) int {
	textW := max(w-2*gtx.Dp(theme.SpaceLG), 0)
	m := op.Record(gtx.Ops)
	g := *gtx
	var dead input.Source
	g.Source = dead
	g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
	dims := drawEditor(env, &g, &s.core.ed, env.Th.Chrome().Fg, env.Th.Chrome().SelectedWash)
	m.Stop()
	return dims.Size.Y
}

// textAreaWidth is the area's layout width: the natural box stretched to
// the constraints' minimum and clamped to their maximum.
func textAreaWidth(env *Env, gtx *layout.Context, p *TextAreaProps) int {
	w := fieldMinWidth(env, gtx, p.Label, p.Placeholder, 0)
	if mn := gtx.Constraints.Min.X; mn > w {
		w = mn
	}
	return min(w, gtx.Constraints.Max.X)
}

// draw paints the area: the label, the box clipped to its animated height
// with the editor inside, and the helper/error line below.
func (s *TextAreaState) draw(env *Env, gtx *layout.Context, p *TextAreaProps, sz fieldSizes, boxH, maxBody int, scrolls, errored bool, fm fieldMotion) {
	c := env.Th.Chrome()
	look := fieldLookOf(env, p.Disabled, errored)
	w := textAreaWidth(env, gtx, p)
	if p.Label != "" {
		at(gtx, image.Pt(0, 0), func(gtx *layout.Context) {
			drawText(env, gtx, theme.TypeSmall(), fade(c.FgMuted, dimOf(p.Disabled)), p.Label, false)
		})
	}
	box := image.Rect(0, sz.labelH, w, sz.labelH+boxH)
	paintBox(gtx, box, look, fm)

	ig := *gtx
	if p.Disabled {
		ig = gtx.Disabled()
	}
	at(&ig, box.Min, func(gtx *layout.Context) {
		defer clip.Rect(image.Rect(0, 0, w, boxH)).Push(gtx.Ops).Pop()
		pad := image.Pt(sz.padX, sz.boxPadY)
		textW := max(w-2*sz.padX, 0)
		if scrolls {
			// Past the cap the editor flows at its full height inside the
			// scrolling viewport; the list owns the wheel.
			at(gtx, image.Pt(sz.padX, 0), func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Constraints{Max: image.Pt(textW, boxH)}
				s.list.Layout(g, 1, func(gtx layout.Context, _ int) layout.Dimensions {
					g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
					return drawEditor(env, &g, &s.core.ed, look.fg, fade(c.SelectedWash, dimOf(p.Disabled)))
				})
			})
		} else {
			if s.core.text() == "" && p.Placeholder != "" {
				at(gtx, pad, func(gtx *layout.Context) {
					drawText(env, gtx, editorType(), fade(c.Placeholder, dimOf(p.Disabled)), p.Placeholder, false)
				})
			}
			at(gtx, pad, func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Constraints{Max: image.Pt(textW, maxBody)}
				drawEditor(env, &g, &s.core.ed, look.fg, fade(c.SelectedWash, dimOf(p.Disabled)))
			})
		}
	})
	at(gtx, image.Pt(0, sz.labelH+boxH+int(sz.lineGapPx+0.5)), func(gtx *layout.Context) {
		belowLine(env, gtx, w, p.Helper, p.Error, c.FgSubtle, env.Th.Status().Danger, fm)
	})
}
