package kit

import (
	"image"
	"testing"

	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// clipRig is one control of TestEveryControlEventAreaIsClippedToItsBounds:
// lay draws it at the cursor and returns its box and the part under test
// (the control itself, or a button inside a field), both relative to the
// cursor; got reports whether the control received the pointer.
type clipRig struct {
	lay func(gtx *layout.Context) (box, part image.Rectangle)
	got func() bool
}

// clipRigNames are the pointer-taking controls of the kit: the controls that
// tag themselves at their own origin or at more than the box they draw would
// show here.
func clipRigNames() []string {
	return []string{"Button", "IconButton", "TextField", "TextArea", "search clear button", "secret reveal button", "Select", "Combobox", "ListRow", "SectionHeader", "SplitPane handle"}
}

// freeGtx is gtx without a minimum size, so a field lays out at its own width.
func freeGtx(gtx *layout.Context) *layout.Context {
	g := *gtx
	g.Constraints.Min = image.Point{}
	return &g
}

// wholeBox is a control whose part under test is its whole box.
func wholeBox(size image.Point) (box, part image.Rectangle) {
	r := image.Rectangle{Max: size}
	return r, r
}

// pressRig is a Button or IconButton: lay runs its layout, which reports the
// activation, and size is its box.
func pressRig(c *pressable, lay func(gtx *layout.Context) (bool, error), size func(gtx *layout.Context) image.Point) clipRig {
	hit := false
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			clicked, err := lay(gtx)
			if err != nil {
				panic(err)
			}
			hit = hit || clicked || c.click.Hovered() || c.click.Pressed()
			return wholeBox(size(gtx))
		},
		got: func() bool { return hit },
	}
}

// fieldRig is a text field of variant v: its whole box, or with trailing its
// trailing button, which is what got looks at.
func fieldRig(h *harness, s *TextFieldState, v TextFieldVariant, trailing bool, got func() bool) clipRig {
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := freeGtx(gtx)
			p := &TextFieldProps{Label: "Label", Variant: v, Helper: "helper"}
			box := image.Rectangle{Max: s.Measure(h.env, g, p).Size}
			if _, err := s.Layout(h.env, g, p); err != nil {
				panic(err)
			}
			if !trailing {
				return box, box
			}
			return box, trailingRect(g, textFieldWidth(h.env, g, p), fieldMetrics(h.env, g, p.Label))
		},
		got: got,
	}
}

// clipRigs builds the rig of a control over its own state.
func clipRigs(t *testing.T, h *harness, name string) clipRig {
	t.Helper()
	switch name {
	case "Button":
		s, bp := NewButtonState(), ButtonProps{Text: "Save"}
		return pressRig(&s.pressable, func(gtx *layout.Context) (bool, error) { return s.Layout(h.env, gtx, bp) },
			func(gtx *layout.Context) image.Point { return s.Measure(h.env, gtx, bp).Size })
	case "IconButton":
		s, ip := NewIconButtonState(), &IconButtonProps{Icon: IconSearch, Label: "Find"}
		return pressRig(&s.pressable, func(gtx *layout.Context) (bool, error) { return s.Layout(h.env, gtx, ip) },
			func(gtx *layout.Context) image.Point { return s.Measure(h.env, gtx, ip).Size })
	case "TextArea":
		s := NewTextAreaState()
		return clipRig{
			lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
				g, p := freeGtx(gtx), &TextAreaProps{Label: "Label", Helper: "helper"}
				box := image.Rectangle{Max: s.Measure(h.env, g, p).Size}
				s.Layout(h.env, g, p)
				return box, box
			},
			got: func() bool { return h.gtx.Focused(&s.core.ed) },
		}
	case "search clear button":
		s := NewTextFieldState()
		s.SetText("abc")
		return fieldRig(h, s, TextFieldSearch, true, func() bool { return s.Text() == "" })
	case "secret reveal button":
		s := NewTextFieldState()
		s.SetText("s3cret")
		return fieldRig(h, s, TextFieldSecret, true, func() bool { return s.revealed })
	case "Select":
		return selectClipRig(t, h)
	case "Combobox":
		return comboboxClipRig(t, h)
	case "ListRow":
		return listRowClipRig(h)
	case "SectionHeader":
		return sectionHeaderClipRig(h)
	case "SplitPane handle":
		return splitPaneClipRig(h)
	}
	s := NewTextFieldState()
	return fieldRig(h, s, TextFieldText, false, func() bool { return h.gtx.Focused(&s.core.ed) })
}

func splitPaneClipRig(h *harness) clipRig {
	s := NewSplitPaneState()
	ratio := float32(0.5)
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(160, 80))
			box := image.Rectangle{Max: g.Constraints.Max}
			part := splitHandleRect(&g, Horizontal, 80, 160, 80)
			if r, ok, _ := s.Layout(h.env, &g, SplitPaneProps{
				Orientation: Horizontal,
				Ratio:       ratio,
			}, nil, nil); ok {
				ratio = r
			}
			return box, part
		},
		got: func() bool { return s.pressing || !s.lastClickTime.IsZero() },
	}
}

func selectClipRig(t *testing.T, h *harness) clipRig {
	t.Helper()
	s := NewSelectState()
	p := &SelectProps{Label: "Engine", Placeholder: "Choose", Options: []SelectOption{{ID: "a", Label: "First"}}}
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := freeGtx(gtx)
			box := image.Rectangle{Max: s.Measure(h.env, g, p).Size}
			if _, err := s.Layout(h.env, g, p); err != nil {
				t.Fatal(err)
			}
			return box, box
		},
		got: func() bool { return h.gtx.Focused(s.Tag()) || s.IsOpen() },
	}
}

func comboboxClipRig(t *testing.T, h *harness) clipRig {
	t.Helper()
	s := NewComboboxState()
	p := &ComboboxProps{Label: "Engine", Placeholder: "Filter"}
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := freeGtx(gtx)
			box := image.Rectangle{Max: s.Measure(h.env, g, p).Size}
			if _, err := s.Layout(h.env, g, p); err != nil {
				t.Fatal(err)
			}
			return box, box
		},
		got: func() bool { return h.gtx.Focused(s.Tag()) },
	}
}

func listRowClipRig(h *harness) clipRig {
	s := NewListRowState()
	clicked := false
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(200, gtx.Dp(theme.Row)))
			dims, ev := s.Layout(h.env, &g, &ListRowProps{Label: "Item"})
			clicked = clicked || ev.Clicked
			pill := pillRect(gtx, image.Rectangle{Max: dims.Size})
			return pill, pill
		},
		got: func() bool { return clicked || h.gtx.Focused(s.Tag()) },
	}
}

func sectionHeaderClipRig(h *harness) clipRig {
	s := NewSectionHeaderState(true)
	toggled := false
	return clipRig{
		lay: func(gtx *layout.Context) (image.Rectangle, image.Rectangle) {
			g := *gtx
			g.Constraints = layout.Exact(image.Pt(200, gtx.Dp(theme.HitMin)))
			dims, ev := s.Layout(h.env, &g, &SectionHeaderProps{Label: "Group", Collapsible: true}, nil)
			toggled = toggled || ev.Toggled
			return wholeBox(dims.Size)
		},
		got: func() bool { return toggled || h.gtx.Focused(s.Tag()) },
	}
}

// clipRun is one control's run: a frame loop with a backdrop laid out under
// the control, which receives every press the control does not take.
type clipRun struct {
	t          *testing.T
	h          *harness
	rig        clipRig
	back       widget.Clickable
	backClicks int
	box, part  image.Rectangle
	// origin is where the control sits in the frame.
	origin image.Point
}

// newClipRun builds the control's rig and lets its motion settle.
func newClipRun(t *testing.T, name string) *clipRun {
	t.Helper()
	h := newHarness(t)
	r := &clipRun{t: t, h: h, rig: clipRigs(t, h, name), origin: image.Pt(60, 40)}
	for range 30 {
		r.frame()
	}
	return r
}

// frame lays the backdrop, then the control over it, one frame.
func (r *clipRun) frame() {
	r.t.Helper()
	r.h.frameSize(r.t, 400, 300)
	if r.back.Clicked(r.h.gtx) {
		r.backClicks++
	}
	r.back.Layout(r.h.gtx, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{Size: gtx.Constraints.Max} })
	at(&r.h.gtx, r.origin, func(gtx *layout.Context) { r.box, r.part = r.rig.lay(gtx) })
	r.box, r.part = r.box.Add(r.origin), r.part.Add(r.origin)
	r.h.end()
}

// press clicks at p and lets the control see it.
func (r *clipRun) press(p image.Point) {
	r.t.Helper()
	r.h.click(float32(p.X), float32(p.Y))
	r.frame()
	r.frame()
}

// TestEveryControlEventAreaIsClippedToItsBounds covers UK-E10: Gio hands a
// pointer event to the topmost area under it only, so an event area that
// reaches past its control's box swallows the presses meant for what is
// underneath — and an area that sits somewhere other than where the control
// is drawn is as wrong. For every control: a press inside its box reaches it;
// a press just outside each of its four sides does not, and a backdrop laid
// out under it receives that press instead; and a press at the origin of a
// field does not trigger its trailing buttons, where their areas used to be.
// The one exception is the window-wide area of an open root menu (§6.15), whose
// own evidence is TestMenuClickOutsideClosesAndDoesNotFallThrough.
func TestEveryControlEventAreaIsClippedToItsBounds(t *testing.T) {
	t.Parallel()
	for _, name := range clipRigNames() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			in := newClipRun(t, name)
			in.press(in.part.Min.Add(in.part.Size().Div(2)))
			if !in.rig.got() {
				t.Fatalf("a press in the middle of the control's box %v did not reach it", in.part)
			}
			// A fresh run: the inside press changed the control's state.
			checkOutside(t, newClipRun(t, name))
		})
	}
}

// checkOutside presses just outside each side of the part under test, and at
// the field's origin for a button inside a field.
func checkOutside(t *testing.T, r *clipRun) {
	t.Helper()
	part, mid := r.part, r.part.Min.Add(r.part.Size().Div(2))
	sides := map[string]image.Point{
		"left": {part.Min.X - 1, mid.Y}, "right": {part.Max.X, mid.Y},
		"above": {mid.X, part.Min.Y - 1}, "below": {mid.X, part.Max.Y},
	}
	for side, p := range sides {
		before := r.backClicks
		r.press(p)
		if r.rig.got() {
			t.Errorf("a press just %s of the box %v reached the control", side, part)
		}
		if part == r.box && r.backClicks != before+1 {
			t.Errorf("a press just %s of the box %v did not reach the backdrop underneath", side, part)
		}
	}
	if part != r.box {
		r.press(r.box.Min.Add(image.Pt(2, 2)))
		if r.rig.got() {
			t.Errorf("a press at the field's origin %v triggered the button drawn at %v", r.box.Min, part)
		}
	}
}
