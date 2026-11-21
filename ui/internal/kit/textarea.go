package kit

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TextAreaProps is TextFieldProps's flowing sibling on the same editing
// core, ui-kit §6.9 (UK-08). Without EnterSubmits, Enter inserts a newline
// and the submit key belongs to the caller — the core never reports
// Submitted. With it, the composer's rule applies (§6.9, F10.4b): Enter
// reports Submitted and Shift+Enter inserts the newline. The box grows with
// its content to MaxLines lines and then scrolls inside: past the cap the
// editor flows at its full height inside a list viewport of the inner rect —
// the box inset by its padding, the mdview code-surface pattern — so the box
// stays put, the padding stays clear, and the content moves inside the
// viewport while the box's height follows the content on Smooth (§3.7).
type TextAreaProps struct {
	Label       string
	Placeholder string
	Helper      string
	Error       string
	// MaxLines caps the box: at most this many lines are visible and the
	// rest scrolls inside (§6.9). Zero means uncapped — the box grows with
	// its content.
	MaxLines int
	// EnterSubmits turns Enter into the composer's submit key (§6.9):
	// Submitted reports a plain Enter and Shift+Enter inserts the newline.
	EnterSubmits bool
	// Monospace renders the editor in theme.TypeCode for code/TOML editing.
	Monospace bool
	// ErrorLines lists 1-based line numbers to mark with a danger indicator.
	ErrorLines []int
	Disabled   bool
}

func (p *TextAreaProps) editorType() theme.Type {
	if p != nil && p.Monospace {
		return theme.TypeCode()
	}
	return editorType()
}

// TextAreaState is the long-lived state the caller keeps per logical
// field (UK-02).
type TextAreaState struct {
	core fieldCore
	err  fieldErrorState
	// boxF follows the content height on Smooth (§6.9); scroll is the
	// in-box ScrollArea, active past the cap.
	boxF   motion.Follower
	scroll *ScrollAreaState
	// viewport and linePx are last frame's in-box viewport height and one
	// text line's height while the box scrolls, the numbers ScrollToCaret
	// aligns to; caret is the caret position it last aligned, so a wheel
	// scroll away from the caret is not fought until the caret moves again.
	viewport, linePx, caret int
	caretSeen               bool
}

// NewTextAreaState builds the state for one area.
func NewTextAreaState() *TextAreaState {
	return &TextAreaState{core: newFieldCore(false), boxF: motion.NewFollower(motion.Smooth()), scroll: NewScrollAreaState()}
}

// Text returns the area's value.
func (s *TextAreaState) Text() string { return s.core.text() }

// SetText replaces the area's value.
func (s *TextAreaState) SetText(v string) { s.core.setText(v) }

// Tag is the area's input tag, for focus scripting and gallery previews.
func (s *TextAreaState) Tag() event.Tag { return s.core.Tag() }

// MoveCaretToEnd moves the caret after the value, as the caller does when it
// replaced the value and the user goes on typing at the end (the composer's
// command choice and draft restore, F10.4b).
func (s *TextAreaState) MoveCaretToEnd() { s.core.caretToEnd() }

// SetCaret sets the selection/caret range in rune offsets.
func (s *TextAreaState) SetCaret(start, end int) { s.core.ed.SetCaret(start, end) }

// MarkSubmitted marks the area submitted, as the caller does when the
// form around it is sent: the §6.8 error gate opens from then on.
func (s *TextAreaState) MarkSubmitted() { s.err.submitted = true }

// Measure returns the area's natural box at its current content: the
// editor laid out into a throwaway macro with a dead input source, so
// nothing paints and no event is consumed, plus the label and the room
// the below-line needs. A capped area measures at its cap — the box the
// control actually draws (§6.9).
func (s *TextAreaState) Measure(env *Env, gtx *layout.Context, p *TextAreaProps) layout.Dimensions {
	sz := fieldMetrics(env, gtx, p.Label)
	w := textAreaWidth(env, gtx, p)
	line := fieldLineHeight(env, gtx, p.Helper, p.Error, p.Error != "", w)
	edType := p.editorType()
	return layout.Dimensions{
		Size:     image.Pt(w, sz.labelH+min(s.bodyHeight(env, gtx, w, edType), s.maxBody(env, gtx, p))+2*sz.boxPadY+int(sz.lineGapPx+line+0.5)),
		Baseline: sz.labelH + sz.boxPadY + labelLine(env, gtx, edType)/2,
	}
}

// Layout draws the area at the current offset and reports the events of
// the frame (UK-06). Submitted reports only when the caller turned the
// submit key on with EnterSubmits (§6.9).
func (s *TextAreaState) Layout(env *Env, gtx *layout.Context, p *TextAreaProps) FieldEvents {
	pre := fieldMetrics(env, gtx, p.Label)
	w := textAreaWidth(env, gtx, p)
	maxBody := s.maxBody(env, gtx, p)
	full := s.bodyHeight(env, gtx, w, p.editorType())
	content := min(full, maxBody)
	scrolls := full > maxBody
	boxH := int(followSmooth(env, &s.boxF, float32(content+2*pre.boxPadY)) + 0.5)
	box := image.Rect(0, pre.labelH, w, pre.labelH+boxH)

	opts := fieldOpts{submit: p.EnterSubmits}
	ev := s.core.begin(gtx, box, opts, p.Disabled)
	if !scrolls {
		s.viewport, s.caretSeen = 0, false
	}
	errored := s.err.visible(p.Error, false, ev)
	fm := s.core.advance(env, s.core.hovered(env) && !p.Disabled, s.core.focused(env, gtx) && !p.Disabled, errored, fieldLineHeight(env, gtx, p.Helper, p.Error, errored, w))
	s.draw(env, gtx, p, pre, boxH, maxBody, scrolls, errored, fm)
	return ev
}

// ScrollToCaret keeps the caret visible while typing below the fold (§6.9,
// F10.4b): past the cap the caller calls it after Layout while the field is
// focused, and the in-box viewport moves only when the caret moved since the
// last alignment, so a wheel scroll away from the caret is left alone. It
// does nothing while the box does not scroll.
func (s *TextAreaState) ScrollToCaret() {
	if s.viewport <= 0 || s.linePx <= 0 {
		return
	}
	_, caret := s.core.ed.Selection()
	if s.caretSeen && caret == s.caret {
		return
	}
	s.caret, s.caretSeen = caret, true
	caretY := int(s.core.ed.CaretCoords().Y)
	off := s.scroll.Offset()
	switch {
	case caretY < off:
		s.scroll.SetOffset(caretY)
	case caretY+s.linePx > off+s.viewport:
		s.scroll.SetOffset(caretY + s.linePx - s.viewport)
	}
}

// maxBody is the px height the editor's text may occupy: MaxLines lines,
// unbounded when the area is uncapped (§6.9).
func (s *TextAreaState) maxBody(env *Env, gtx *layout.Context, p *TextAreaProps) int {
	if p.MaxLines <= 0 {
		return 1 << 20
	}
	_, line := env.Content(p.editorType())
	return p.MaxLines * gtx.Sp(line)
}

// bodyHeight lays the editor into a throwaway macro with a dead input
// source and returns its full text height: the number the box grows
// around, capped by the caller at the point of use, and the number that
// says whether the in-box scroller is needed at all.
func (s *TextAreaState) bodyHeight(env *Env, gtx *layout.Context, w int, edType theme.Type) int {
	textW := max(w-2*gtx.Dp(theme.SpaceLG), 0)
	m := op.Record(gtx.Ops)
	g := *gtx
	var dead input.Source
	g.Source = dead
	g.Constraints = layout.Constraints{Max: image.Pt(textW, 1<<20)}
	dims := drawEditorWithType(env, &g, &s.core.ed, edType, env.Th.Chrome().Fg, env.Th.Chrome().SelectedWash)
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

// draw paints the area: the label, the box with the editor inside the
// inner rect — the box inset by its padding, which the scroll clip is
// too, so text never draws into the padding however far it scrolls — and
// the helper/error line below.
func (s *TextAreaState) draw(env *Env, gtx *layout.Context, p *TextAreaProps, sz fieldSizes, boxH, maxBody int, scrolls, errored bool, fm fieldMotion) {
	c := env.Th.Chrome()
	look := fieldLookOf(env, p.Disabled, errored)
	w := textAreaWidth(env, gtx, p)
	if p.Label != "" {
		at(gtx, image.Pt(0, 0), func(gtx *layout.Context) {
			dims := drawText(env, gtx, theme.TypeSmall(), fade(c.FgMuted, dimOf(p.Disabled)), p.Label, false)
			recordRun(env, image.Rectangle{Max: dims.Size})
		})
	}
	box := image.Rect(0, sz.labelH, w, sz.labelH+boxH)
	paintBox(gtx, box, look, fm)

	ig := *gtx
	if p.Disabled {
		ig = gtx.Disabled()
	}
	at(&ig, box.Min, func(gtx *layout.Context) {
		edType := p.editorType()
		_, line := env.Content(edType)
		linePx := gtx.Sp(line)
		inner := image.Rect(sz.padX, sz.boxPadY, max(w-sz.padX, sz.padX), max(boxH-sz.boxPadY, sz.boxPadY))
		textW := inner.Dx()
		scrollOff := 0
		func() {
			defer clip.Rect(inner).Push(gtx.Ops).Pop()
			if scrolls {
				// Past the cap the editor flows at its full height inside the
				// ScrollArea viewport of the inner rect; ScrollArea owns the
				// wheel and draws the §6.29 indicator bar. The viewport and the
				// line height are what ScrollToCaret aligns the caret to.
				s.viewport, s.linePx = inner.Dy(), linePx
				scrollOff = s.scroll.Offset()
				at(gtx, inner.Min, func(gtx *layout.Context) {
					g := *gtx
					g.Constraints = layout.Constraints{Max: image.Pt(textW, inner.Dy())}
					s.scroll.Layout(env, &g, ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
						return drawEditorWithType(env, gtx, &s.core.ed, edType, look.fg, fade(c.SelectedWash, dimOf(p.Disabled)))
					})
				})
			} else {
				if s.core.text() == "" && p.Placeholder != "" {
					at(gtx, inner.Min, func(gtx *layout.Context) {
						dims := drawText(env, gtx, edType, fade(c.Placeholder, dimOf(p.Disabled)), p.Placeholder, false)
						recordRun(env, image.Rectangle{Max: dims.Size}.Add(inner.Min))
					})
				}
				at(gtx, inner.Min, func(gtx *layout.Context) {
					g := *gtx
					g.Constraints = layout.Constraints{Max: image.Pt(textW, maxBody)}
					drawEditorWithType(env, &g, &s.core.ed, edType, look.fg, fade(c.SelectedWash, dimOf(p.Disabled)))
				})
			}
		}()
		drawTextAreaErrorMarkers(env, gtx, inner, linePx, scrollOff, p.ErrorLines)
	})
	lineY := sz.labelH + boxH + int(sz.lineGapPx+0.5)
	var lineBounds image.Rectangle
	at(gtx, image.Pt(0, lineY), func(gtx *layout.Context) {
		lineBounds = belowLine(env, gtx, w, p.Helper, p.Error, c.FgSubtle, env.Th.Status().Danger, fm)
	})
	recordRun(env, lineBounds.Add(image.Pt(0, lineY)))
}

func drawTextAreaErrorMarkers(env *Env, gtx *layout.Context, inner image.Rectangle, linePx, scrollOff int, errorLines []int) {
	if len(errorLines) == 0 || linePx <= 0 {
		return
	}
	barW := max(gtx.Dp(3), 2)
	barX := max(inner.Min.X-barW-gtx.Dp(theme.SpaceXXS), 0)
	for _, ln := range errorLines {
		if ln < 1 {
			continue
		}
		y0 := inner.Min.Y + (ln-1)*linePx - scrollOff
		y1 := y0 + linePx
		if y1 <= inner.Min.Y || y0 >= inner.Max.Y {
			continue
		}
		r := image.Rect(barX, max(y0, inner.Min.Y), barX+barW, min(y1, inner.Max.Y))
		paint.FillShape(gtx.Ops, env.Th.Status().Danger, clip.Rect(r).Op())
	}
}
