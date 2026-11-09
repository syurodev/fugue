// Package view is the virtualised transcript used by the selection and list
// spikes: layout.List for the scrolling, flow for text, flow.Selector for the
// cross-item selection.
package view

import (
	"image"
	"image/color"
	"math/rand"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
)

// Entry is an item plus its expand state and layout cache.
type Entry struct {
	gen.Entry
	Open bool
	Item flow.Item
	laid *flow.Laid
}

func (e *Entry) rebuild() {
	if e.Kind == "raw" { // the entry brings its own blocks (diff lines)
		e.Item.Blocks = e.Body
		e.Item.Rev++
		return
	}
	e.Item.Blocks = e.Item.Blocks[:0]
	e.Item.Blocks = append(e.Item.Blocks, flow.Block{Runs: gen.Inline(e.Head)})
	if e.Kind == "assistant" || e.Kind == "user" || e.Open {
		e.Item.Blocks = append(e.Item.Blocks, e.Body...)
	}
	e.Item.Rev++
}

// SetBody replaces the body (a stream tick).
func (e *Entry) SetBody(b []flow.Block) { e.Body = b; e.rebuild() }

// Collapsible reports whether the entry folds.
func (e *Entry) Collapsible() bool { return e.Kind == "tool_call" || e.Kind == "reasoning" }

// View is the transcript widget.
type View struct {
	Entries []*Entry
	List    widget.List
	Sel     flow.Selector
	Env     *flow.Env
	Theme   *material.Theme
	Pal     *flow.Palette
	// Geometry of the last frame, for tests and the stream driver.
	Size   image.Point
	Toggle func(idx int)
}

// New builds a view over entries.
func New(th *material.Theme, env *flow.Env, pal *flow.Palette, es []gen.Entry) *View {
	v := &View{Theme: th, Env: env, Pal: pal}
	v.List.Axis = layout.Vertical
	for _, e := range es {
		v.Add(e)
	}
	v.Sel.PadX, v.Sel.PadY = 16, 6
	v.Sel.Sep = "\n\n"
	v.Sel.Count = func() int { return len(v.Entries) }
	v.Sel.Text = func(i int) string { return v.Entries[i].Item.Text() }
	v.Sel.Click = func(idx, y int) bool {
		e := v.Entries[idx]
		if !e.Collapsible() || y > env.Px*2 { // only the header line toggles
			return false
		}
		v.ToggleAt(idx)
		return true
	}
	return v
}

// Add appends an entry.
func (v *View) Add(e gen.Entry) *Entry {
	en := &Entry{Entry: e}
	en.rebuild()
	v.Entries = append(v.Entries, en)
	return en
}

// ToggleAt folds or unfolds one entry.
func (v *View) ToggleAt(i int) {
	e := v.Entries[i]
	e.Open = !e.Open
	e.rebuild()
}

// Layout draws the transcript.
func (v *View) Layout(gtx layout.Context) layout.Dimensions {
	v.Sel.Update(gtx, &v.List.List, v.Size)
	v.Sel.Begin()
	n := len(v.Entries)
	px, py := v.Sel.PadX, v.Sel.PadY
	ls := material.List(v.Theme, &v.List)
	dims := ls.Layout(gtx, n, func(gtx layout.Context, i int) layout.Dimensions {
		e := v.Entries[i]
		w := gtx.Constraints.Max.X - 2*px
		if e.laid == nil || e.laid.Rev != e.Item.Rev || e.laid.Width != w {
			e.laid = v.Env.Layout(&e.Item, w)
		}
		h := e.laid.Height + 2*py
		v.Sel.Record(i, h, e.laid)
		lo, hi := v.Sel.Range(i)
		defer op.Offset(image.Pt(px, py)).Push(gtx.Ops).Pop()
		v.Env.Paint(gtx, e.laid, lo, hi)
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, h)}
	})
	v.Sel.End(v.List.Position, 0)
	v.Size = dims.Size
	v.Sel.Register(gtx, image.Pt(dims.Size.X-gtx.Dp(unit.Dp(16)), dims.Size.Y))
	return dims
}

// NewTheme returns the material theme over the shaper.
func NewTheme(sh *text.Shaper, pal *flow.Palette) *material.Theme {
	th := material.NewTheme()
	th.Shaper = sh
	th.Bg, th.Fg = pal.Bg, pal.Fg
	return th
}

// Dark is the palette of the spikes.
func Dark() *flow.Palette {
	c := func(r, g, b uint8) color.NRGBA { return color.NRGBA{r, g, b, 255} }
	return &flow.Palette{
		Fg: c(0xe6, 0xe6, 0xe6), Dim: c(0x8b, 0x92, 0x9a), Bg: c(0x16, 0x18, 0x1d), CodeBg: c(0x21, 0x25, 0x2c),
		Sel: color.NRGBA{0x3b, 0x7d, 0xdd, 0x90}, AddBg: color.NRGBA{0x1f, 0x4d, 0x2b, 0xff}, DelBg: color.NRGBA{0x5a, 0x23, 0x27, 0xff},
		Tok: [8]color.NRGBA{{}, c(0xff, 0x79, 0xc6), c(0xa5, 0xd6, 0xa7), c(0x6a, 0x73, 0x7d), c(0xf1, 0xa1, 0x4c), c(0x7a, 0xb8, 0xff), c(0xc7, 0x92, 0xea), c(0x4e, 0xc9, 0xb0)},
	}
}

// Background paints the window background.
func Background(gtx layout.Context, c color.NRGBA) {
	defer clip.Rect{Max: gtx.Constraints.Max}.Push(gtx.Ops).Pop()
	paint.Fill(gtx.Ops, c)
}

func newRand() *rand.Rand { return rand.New(rand.NewSource(9)) }
