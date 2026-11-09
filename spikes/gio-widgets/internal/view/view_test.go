package view

import (
	"image"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
)

type rig struct {
	t   *testing.T
	v   *View
	r   input.Router
	ops op.Ops
}

func newRig(t *testing.T, n int) *rig {
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	pal := Dark()
	g := &rig{t: t}
	g.v = New(NewTheme(sh, pal), flow.NewEnv(sh, pal, 15), pal, gen.Entries(n, 1))
	g.frame()
	return g
}

func (g *rig) frame() {
	g.ops.Reset()
	gtx := layout.Context{Ops: &g.ops, Constraints: layout.Exact(image.Pt(900, 700)), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Source: g.r.Source()}
	g.v.Layout(gtx)
	g.r.Frame(gtx.Ops)
}

func (g *rig) ptr(k pointer.Kind, p f32.Point, mod key.Modifiers) {
	g.r.Queue(pointer.Event{Kind: k, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p, Modifiers: mod})
	g.frame()
}

// where gives the pixel of text offset off in the visible item idx.
func (g *rig) where(idx, off int) f32.Point {
	for _, s := range g.v.Sel.Visible() {
		if s.Idx == idx {
			x, y := s.Laid.Caret(off)
			return f32.Pt(float32(x+g.v.Sel.PadX), float32(s.Top+g.v.Sel.PadY+y+s.Laid.LineH()/2))
		}
	}
	g.t.Fatalf("item %d is not visible", idx)
	return f32.Point{}
}

func (g *rig) copy() string {
	g.r.Queue(key.Event{Name: "C", Modifiers: key.ModShortcut, State: key.Press})
	g.frame()
	_, b, _ := g.r.WriteClipboard()
	return string(b)
}

func (g *rig) mid(idx int) int { return len([]rune(g.v.Entries[idx].Item.Text())) / 2 }

func TestSelectAcrossItemsAndCodeBlocks(t *testing.T) {
	g := newRig(t, 300)
	// Find three items whose middles are on screen, the first not a fold
	// header (a press on a header toggles it instead of selecting).
	var a, c int
	for first := 40; c-a < 2; first++ {
		g.v.List.List.Position.First = first
		g.frame()
		a, c = 0, 0
		for _, v := range g.v.Sel.Visible() {
			if g.where(v.Idx, g.mid(v.Idx)).Y > 690 || v.Idx == first {
				continue
			}
			if a == 0 && !g.v.Entries[v.Idx].Collapsible() {
				a = v.Idx
			}
			if a != 0 {
				c = v.Idx
			}
		}
	}
	g.ptr(pointer.Press, g.where(a, g.mid(a)), 0)
	g.ptr(pointer.Move, g.where(c, g.mid(c)), 0)
	g.ptr(pointer.Release, g.where(c, g.mid(c)), 0)

	want := g.v.Sel.SelectedText()
	got := g.copy()
	if got != want || got == "" {
		t.Fatalf("clipboard %q != selection %q", got, want)
	}
	ta := []rune(g.v.Entries[a].Item.Text())
	tc := []rune(g.v.Entries[c].Item.Text())
	if !strings.HasPrefix(got, string(ta[g.mid(a):])) || !strings.HasSuffix(got, string(tc[:g.mid(c)])) {
		t.Fatalf("copy does not run from the middle of item %d to the middle of item %d:\n%q", a, c, got)
	}
	for i := a + 1; i < c; i++ {
		if !strings.Contains(got, g.v.Entries[i].Item.Text()) {
			t.Fatalf("item %d between the ends is missing from the copy", i)
		}
	}
	t.Logf("copied %d runes across items %d..%d", len([]rune(got)), a, c)

	// The selection is anchored to the document: scrolling far away, then
	// streaming into the last item, leaves the copied text unchanged.
	g.v.List.List.Position.First = 250
	g.frame()
	e := g.v.Entries[len(g.v.Entries)-1]
	e.SetBody(append(e.Body, flow.Block{Runs: gen.Inline("more streamed text")}))
	g.v.Add(gen.Entry{Kind: "assistant", Head: "new", Body: gen.Message(newRand(), 2)})
	g.frame()
	if got2 := g.copy(); got2 != want {
		t.Fatalf("selection changed after scroll and stream:\n%q\n%q", want, got2)
	}
}

func TestShiftClickExtends(t *testing.T) {
	g := newRig(t, 100)
	g.ptr(pointer.Press, g.where(0, 3), 0)
	g.ptr(pointer.Release, g.where(0, 3), 0)
	vis := g.v.Sel.Visible()
	last := vis[2].Idx
	g.ptr(pointer.Press, g.where(last, 5), key.ModShift)
	g.ptr(pointer.Release, g.where(last, 5), key.ModShift)
	if g.v.Sel.Anchor.Item != 0 || g.v.Sel.Focus.Item != last {
		t.Fatalf("anchor %v focus %v", g.v.Sel.Anchor, g.v.Sel.Focus)
	}
}

func TestToggleKeepsItemsAboveStill(t *testing.T) {
	g := newRig(t, 300)
	g.v.List.List.Position.First = 100
	g.v.List.List.Position.Offset = 17
	g.frame()
	tops := map[int]int{}
	for _, s := range g.v.Sel.Visible() {
		tops[s.Idx] = s.Top
	}
	var tc int
	for _, s := range g.v.Sel.Visible()[1:] {
		if g.v.Entries[s.Idx].Collapsible() {
			tc = s.Idx
			break
		}
	}
	if tc == 0 {
		t.Skip("no collapsible item in view")
	}
	g.v.ToggleAt(tc)
	g.frame()
	for _, s := range g.v.Sel.Visible() {
		if s.Idx <= tc && tops[s.Idx] != s.Top {
			t.Fatalf("item %d moved from %d to %d when item %d folded", s.Idx, tops[s.Idx], s.Top, tc)
		}
	}
}
