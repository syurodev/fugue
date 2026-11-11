package mdview

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// view is one frame's drawing context: the theme, the document's state and
// the shaper every measurement and glyph path goes through.
type view struct {
	th     *theme.Theme
	st     *State
	shaper *text.Shaper
	locale system.Locale
}

// ink is the text color of a span: the link color, muted alt text for an
// image, else the theme's syntax color (main text for plain spans).
func (v *view) ink(sp *markdown.Span) color.NRGBA {
	switch {
	case sp.Link != nil && sp.Link.Openable:
		return v.th.Link()
	case sp.Image != nil:
		return v.th.Chrome().FgMuted
	default:
		return v.th.Syntax(theme.SyntaxRole(sp.Role)).Color
	}
}

// drawFlow paints a wrapped run at the current offset and runs the state of
// each link in it. The run's static drawing — glyphs, surfaces, underlines and
// the pointer areas of its links — is recorded once into its own ops and
// replayed every frame; what changes with input (hover, focus ring, popover)
// is handled live. avail is the width lines align inside and popovers stay
// within.
func (v *view) drawFlow(gtx *layout.Context, f *flow, align markdown.Align, avail int) {
	if f.rec == nil {
		v.record(gtx, f, align, avail)
	}
	f.call.Add(gtx.Ops)
	for i := range f.links {
		an := &f.links[i]
		ls := v.st.link(an.link)
		v.pollLink(gtx, an.link, ls)
		if gtx.Focused(ls) {
			for _, box := range an.boxes {
				v.outline(gtx, box, v.th.Chrome().FocusBorder)
			}
		}
		v.popover(gtx, an, avail)
	}
}

// record draws the static part of a run into the run's own ops.
func (v *view) record(gtx *layout.Context, f *flow, align markdown.Align, avail int) {
	f.rec = new(op.Ops)
	rec := *gtx
	rec.Ops = f.rec
	m := op.Record(f.rec)
	for i := range f.lines {
		l := &f.lines[i]
		shift := 0
		switch align {
		case markdown.AlignCenter:
			shift = (avail - l.w) / 2
		case markdown.AlignRight:
			shift = avail - l.w
		case markdown.AlignNone, markdown.AlignLeft:
		}
		for k := range l.atoms {
			a := &l.atoms[k]
			x, base := shift+a.x, l.top+l.base
			v.drawAtom(&rec, a, x, base)
			if lk := a.span.Link; lk != nil && lk.Openable {
				f.links = v.noteLink(&rec, f.links, lk, image.Rect(x, base-a.asc, x+a.w, base+a.desc))
			}
		}
	}
	f.call = m.Stop()
}

// linkAnchor is a link met in a run and the boxes of its atoms; the popover
// attaches to the first.
type linkAnchor struct {
	link  *markdown.Link
	boxes []image.Rectangle
}

// noteLink registers one atom's box as a pointer and focus target of its link
// and files the box under the link.
func (v *view) noteLink(gtx *layout.Context, anchors []linkAnchor, lk *markdown.Link, box image.Rectangle) []linkAnchor {
	area := clip.Rect(box).Push(gtx.Ops)
	event.Op(gtx.Ops, v.st.link(lk))
	pointer.CursorPointer.Add(gtx.Ops)
	area.Pop()
	for i := range anchors {
		if anchors[i].link == lk {
			anchors[i].boxes = append(anchors[i].boxes, box)
			return anchors
		}
	}
	return append(anchors, linkAnchor{link: lk, boxes: []image.Rectangle{box}})
}

// drawAtom paints one atom: the code surface behind inline code, the glyphs,
// the strikethrough line and the dotted underline of a link.
func (v *view) drawAtom(gtx *layout.Context, a *atom, x, base int) {
	sp := a.span
	if sp.Style&markdown.StyleCode != 0 {
		box := image.Rect(x, base-a.asc-1, x+a.w, base+a.desc+1)
		paint.FillShape(gtx.Ops, v.th.CodeSurface(), clip.UniformRRect(box, gtx.Dp(3)).Op(gtx.Ops))
	}
	ink := v.ink(sp)
	if len(a.glyphs) > 0 {
		off := op.Offset(image.Pt(x, base)).Push(gtx.Ops)
		outline := clip.Outline{Path: v.shaper.Shape(a.glyphs)}.Op().Push(gtx.Ops)
		paint.ColorOp{Color: ink}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		outline.Pop()
		if call := v.shaper.Bitmaps(a.glyphs); call != (op.CallOp{}) {
			call.Add(gtx.Ops)
		}
		off.Pop()
	}
	thin := max(gtx.Dp(1), 1)
	if sp.Style&markdown.StyleStrike != 0 {
		fill(gtx, ink, image.Rect(x, base-a.asc/3, x+a.w, base-a.asc/3+thin))
	}
	if sp.Link != nil && sp.Link.Openable && !a.arrow {
		for dx := 0; dx < a.w; dx += 3 * thin {
			dot := image.Rect(x+dx, base+gtx.Dp(2), min(x+dx+thin, x+a.w), base+gtx.Dp(2)+thin)
			fill(gtx, ink, dot)
		}
	}
}

// outline strokes a rounded box in c.
func (v *view) outline(gtx *layout.Context, box image.Rectangle, c color.NRGBA) {
	w := float32(max(gtx.Dp(unit.Dp(1.5)), 1))
	paint.FillShape(gtx.Ops, c, clip.Stroke{Path: clip.UniformRRect(box, gtx.Dp(3)).Path(gtx.Ops), Width: w}.Op())
}

// pointerState folds a tag's pointer events: whether the pointer is over it
// after them, and whether the primary button was pressed.
func pointerState(gtx *layout.Context, tag event.Tag, over bool) (hovered, pressed bool) {
	hovered = over
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: tag,
			Kinds:  pointer.Enter | pointer.Leave | pointer.Press | pointer.Cancel,
		})
		if !ok {
			return hovered, pressed
		}
		pe, isPointer := ev.(pointer.Event)
		if !isPointer {
			continue
		}
		switch pe.Kind {
		case pointer.Enter:
			hovered = true
		case pointer.Leave, pointer.Cancel:
			hovered = false
		case pointer.Press:
			pressed = pressed || pe.Buttons.Contain(pointer.ButtonPrimary)
		case pointer.Move, pointer.Release, pointer.Drag, pointer.Scroll:
		}
	}
}

// pollLink folds this frame's pointer and key events of one link into its
// state: hover of the link and of its popover (polled together, so a pointer
// that crosses from one to the other never leaves a frame with neither),
// pin on click or Enter or Space, unpin on Escape.
func (v *view) pollLink(gtx *layout.Context, lk *markdown.Link, ls *linkState) {
	var pressed bool
	ls.hover, pressed = pointerState(gtx, ls, ls.hover)
	ls.popHover, _ = pointerState(gtx, &ls.popTag, ls.popHover)
	if pressed {
		gtx.Execute(key.FocusCmd{Tag: ls})
		v.togglePin(lk)
	}
	for {
		ev, ok := gtx.Event(
			key.FocusFilter{Target: ls},
			key.Filter{Focus: ls, Name: key.NameReturn},
			key.Filter{Focus: ls, Name: key.NameSpace},
			key.Filter{Focus: ls, Name: key.NameEscape},
		)
		if !ok {
			return
		}
		ke, isKey := ev.(key.Event)
		if !isKey || ke.State != key.Press {
			continue
		}
		if ke.Name == key.NameEscape && v.st.pinned == lk {
			v.st.pinned = nil
		} else if ke.Name != key.NameEscape {
			v.togglePin(lk)
		}
	}
}

// togglePin pins the popover of lk, or unpins it when it already is; one
// popover is pinned at a time.
func (v *view) togglePin(lk *markdown.Link) {
	if v.st.pinned == lk {
		v.st.pinned = nil
		return
	}
	v.st.pinned = lk
}

// fill paints a solid rectangle.
func fill(gtx *layout.Context, c color.NRGBA, r image.Rectangle) {
	paint.FillShape(gtx.Ops, c, clip.Rect(r).Op())
}
