package flow

import (
	"image"
	"io"
	"sort"
	"strings"

	"gioui.org/f32"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
)

// Pos is a point in the document: item index and rune offset inside its text.
type Pos struct{ Item, Off int }

func (p Pos) less(q Pos) bool { return p.Item < q.Item || (p.Item == q.Item && p.Off < q.Off) }

type visible struct {
	idx, top, h int
	laid        *Laid
}

// Selector is a text selection that spans items. It is anchored to document
// positions (item, offset), never to pixels, so it survives scrolling,
// virtualisation and items streaming in below it.
type Selector struct {
	Anchor, Focus Pos
	Has           bool
	Dragging      bool
	// Sep joins items in the copied text.
	Sep string
	// Text returns the plain text of item i (the string offsets index).
	Text func(i int) string
	// Click, if set, runs on a primary press; true consumes it.
	Click func(idx, y int) bool
	// Count is the number of items in the document.
	Count func() int
	// Pad is the item content inset (x, y) in px.
	PadX, PadY int

	vis  []visible
	last f32.Point
	tag  struct{ _ int }
	// Copied is the last text written to the clipboard (for tests and logs).
	Copied string
}

// Begin clears the geometry of the previous frame's visible items; call it
// right before the list lays out.
func (s *Selector) Begin() { s.vis = s.vis[:0] }

// Record notes an item as laid out with its height; tops are fixed by End.
func (s *Selector) Record(idx, h int, l *Laid) {
	s.vis = append(s.vis, visible{idx: idx, h: h, laid: l})
}

// End assigns the item tops from the list position after layout. layout.List
// calls the element for the items it needs, forwards first and then backwards
// to fill a gap, so the records are sorted and trimmed to the visible window.
func (s *Selector) End(pos layout.Position, gap int) {
	sort.Slice(s.vis, func(i, j int) bool { return s.vis[i].idx < s.vis[j].idx })
	out := s.vis[:0]
	for _, v := range s.vis {
		if v.idx >= pos.First && v.idx < pos.First+pos.Count && (len(out) == 0 || out[len(out)-1].idx != v.idx) {
			out = append(out, v)
		}
	}
	s.vis = out
	y := -pos.Offset
	for i := range s.vis {
		s.vis[i].top = y
		y += s.vis[i].h + gap
	}
}

// Range is the selected part of item i as [lo, hi).
func (s *Selector) Range(i int) (lo, hi int) {
	if !s.Has || s.Anchor == s.Focus {
		return 0, 0
	}
	a, b := s.Anchor, s.Focus
	if b.less(a) {
		a, b = b, a
	}
	if i < a.Item || i > b.Item {
		return 0, 0
	}
	lo, hi = 0, 1<<30
	if i == a.Item {
		lo = a.Off
	}
	if i == b.Item {
		hi = b.Off
	}
	return lo, hi
}

// SelectedText builds the plain text of the selection, item by item.
func (s *Selector) SelectedText() string {
	if !s.Has || s.Anchor == s.Focus {
		return ""
	}
	a, b := s.Anchor, s.Focus
	if b.less(a) {
		a, b = b, a
	}
	var parts []string
	for i := a.Item; i <= b.Item; i++ {
		rs := []rune(s.Text(i))
		lo, hi := s.Range(i)
		hi = min(hi, len(rs))
		if lo < hi {
			parts = append(parts, string(rs[lo:hi]))
		} else if i != a.Item && i != b.Item {
			parts = append(parts, "")
		}
	}
	return strings.Join(parts, s.Sep)
}

// SelectAll selects n items.
func (s *Selector) SelectAll(n int) {
	if n == 0 {
		return
	}
	s.Anchor = Pos{0, 0}
	s.Focus = Pos{n - 1, len([]rune(s.Text(n - 1)))}
	s.Has = true
}

func (s *Selector) hit(p f32.Point) (Pos, bool) {
	if len(s.vis) == 0 {
		return Pos{}, false
	}
	y := int(p.Y)
	v := &s.vis[len(s.vis)-1]
	yy := y - v.top - s.PadY
	for i := range s.vis {
		if y < s.vis[i].top+s.vis[i].h {
			v = &s.vis[i]
			yy = y - v.top - s.PadY
			if i == 0 && y < v.top {
				yy = 0
			}
			break
		}
	}
	return Pos{v.idx, v.laid.Hit(int(p.X)-s.PadX, yy)}, true
}

// Update consumes the input of the frame. area is the list viewport; list is
// scrolled when a drag leaves it. It returns true when the frame changed
// something that needs a redraw soon.
func (s *Selector) Update(gtx layout.Context, list *layout.List, area image.Point) bool {
	redraw := false
	for {
		ev, ok := gtx.Event(
			pointer.Filter{Target: s, Kinds: pointer.Press | pointer.Drag | pointer.Release},
			key.FocusFilter{Target: s},
			key.Filter{Focus: s, Required: key.ModShortcut, Name: "C"},
			key.Filter{Focus: s, Required: key.ModShortcut, Name: "A"},
		)
		if !ok {
			break
		}
		switch e := ev.(type) {
		case pointer.Event:
			s.last = e.Position
			switch e.Kind {
			case pointer.Press:
				if e.Buttons != pointer.ButtonPrimary {
					break
				}
				gtx.Execute(key.FocusCmd{Tag: s})
				p, ok := s.hit(e.Position)
				if !ok {
					break
				}
				if s.Click != nil && s.clickAt(e.Position) {
					break
				}
				if e.Modifiers.Contain(key.ModShift) && s.Has {
					s.Focus = p
				} else {
					s.Anchor, s.Focus, s.Has = p, p, true
				}
				s.Dragging = true
			case pointer.Drag:
				if s.Dragging {
					if p, ok := s.hit(e.Position); ok {
						s.Focus = p
					}
				}
			case pointer.Release:
				s.Dragging = false
			}
		case key.Event:
			if e.State != key.Press {
				break
			}
			if e.Name == "C" {
				s.Copied = s.SelectedText()
				if s.Copied != "" {
					gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(s.Copied))})
				}
			} else if e.Name == "A" {
				s.SelectAll(s.Count())
			}
		}
	}
	if s.Dragging { // keep scrolling while the pointer rests outside the list
		d := 0
		switch {
		case s.last.Y < 0:
			d = int(s.last.Y)
		case int(s.last.Y) > area.Y:
			d = int(s.last.Y) - area.Y
		}
		if d != 0 {
			list.Position.Offset += d / 4
			list.Position.BeforeEnd = true
			redraw = true
		}
		if p, ok := s.hit(s.last); ok { // list moved under a resting pointer
			s.Focus = p
		}
	}
	return redraw
}

func (s *Selector) clickAt(p f32.Point) bool {
	y := int(p.Y)
	for i := range s.vis {
		v := &s.vis[i]
		if y >= v.top && y < v.top+v.h {
			return s.Click(v.idx, y-v.top-s.PadY)
		}
	}
	return false
}

// Register installs the pointer and key handlers over the list viewport. Call
// it after the list has been laid out (it then sits above the list's own
// scroll handler and passes events through to it).
func (s *Selector) Register(gtx layout.Context, size image.Point) {
	defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	pointer.CursorText.Add(gtx.Ops)
	event.Op(gtx.Ops, s)
}

// Vis is the geometry of one visible item in the last frame.
type Vis struct {
	Idx, Top, H int
	Laid        *Laid
}

// Visible lists the items on screen with their tops (list-local px).
func (s *Selector) Visible() []Vis {
	out := make([]Vis, len(s.vis))
	for i, v := range s.vis {
		out[i] = Vis{v.idx, v.top, v.h, v.laid}
	}
	return out
}
