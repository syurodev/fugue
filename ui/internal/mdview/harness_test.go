//nolint:gocritic // Gio passes layout.Context by value by design; hugeParam flags it.
package mdview

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestMain checks for leaked goroutines; chroma's regexp engine keeps a
// self-stopping clock goroutine that belongs to the dependency.
func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m, goleak.IgnoreAnyFunction("github.com/dlclark/regexp2/v2.runClock"))
}

// frozenClock always reads one instant, so highlighting never spends its
// budget and no real time is read.
type frozenClock struct{}

// Now implements markdown.Clock.
func (frozenClock) Now() time.Time { return time.Unix(0, 0) }

// harness is a headless window: a router for input, one theme and one State.
type harness struct {
	t      *testing.T
	router input.Router
	ops    op.Ops
	th     theme.Theme
	st     State
	doc    *markdown.Doc
	width  int
}

// newHarness parses src at the given width in the light theme.
func newHarness(t *testing.T, src string, width int) *harness {
	t.Helper()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	return &harness{t: t, th: theme.Light(shaper), doc: markdown.Parse(src, markdown.WithClock(frozenClock{})), width: width}
}

// context builds this frame's layout context over the router.
func (h *harness) context() layout.Context {
	return layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(h.width, 1_000_000)},
		Source:      h.router.Source(),
	}
}

// frame lays the document out once and hands the ops to the router.
func (h *harness) frame() layout.Dimensions {
	h.ops.Reset()
	dims := Layout(h.context(), &h.th, h.doc, &h.st)
	h.router.Frame(&h.ops)
	return dims
}

// move puts the mouse at p and runs the frame that sees it.
func (h *harness) move(p image.Point) {
	h.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(p.X), float32(p.Y))})
	h.frame()
}

// click presses and releases the primary button at p.
func (h *harness) click(p image.Point) {
	pos := f32.Pt(float32(p.X), float32(p.Y))
	h.router.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: pos},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: pos},
	)
	h.frame()
}

// link returns the first link of the document.
func (h *harness) link() *markdown.Link {
	h.t.Helper()
	for _, b := range h.doc.Blocks {
		for i := range b.Spans {
			if b.Spans[i].Link != nil {
				return b.Spans[i].Link
			}
		}
	}
	h.t.Fatal("no link in the document")
	return nil
}

// box is the box of the link's first text atom in a one-block document.
func (h *harness) box(lk *markdown.Link) image.Rectangle {
	h.t.Helper()
	for _, f := range h.st.flows {
		for i := range f.lines {
			l := &f.lines[i]
			for k := range l.atoms {
				a := &l.atoms[k]
				if a.span.Link == lk && !a.arrow && a.glyphs != nil {
					base := l.top + l.base
					return image.Rect(a.x, base-a.asc, a.x+a.w, base+a.desc)
				}
			}
		}
	}
	h.t.Fatal("link not laid out")
	return image.Rectangle{}
}

// shown reports whether the link's popover shows after the last frame.
func (h *harness) shown(lk *markdown.Link) bool {
	v := &view{th: &h.th, st: &h.st, shaper: h.th.Shaper()}
	return v.visible(h.context(), lk, h.st.link(lk))
}

// exampleHost is the host of the links the tests use.
const exampleHost = "example.com"

// linkDoc is a paragraph long enough to wrap, with a link low enough that its
// popover has room above.
func linkDoc(link string) string {
	return strings.Repeat("word ", 90) + "[docs](" + link + ") tail"
}

// center is the middle of a rectangle.
func center(r image.Rectangle) image.Point { return r.Min.Add(r.Max).Div(2) }
