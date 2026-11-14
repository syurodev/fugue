package kitspike

import (
	"testing"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// harness is a headless frame loop with an input router, the pattern Gio's
// own widget tests use: lay out, hand the ops to the router, queue events,
// lay out again.
type harness struct {
	router *input.Router
	ops    op.Ops
	env    *Env
	gtx    layout.Context
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the shaper: %v", err)
	}
	th := theme.Dark(sh)
	h := &harness{router: new(input.Router)}
	h.env = &Env{Th: &th, Sh: sh, Driver: &motion.Driver{}}
	return h
}

// frame lays one frame out at the given size and time.
func (h *harness) frame(w, ht int) {
	h.ops.Reset()
	h.gtx = layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(imagePt(w, ht)),
		Source:      h.router.Source(),
	}
	h.env.Begin(frameClock())
	h.gtx.Now = frameClock()
	h.gtx.Source = h.router.Source()
}

// end hands the frame's ops to the router.
func (h *harness) end() {
	h.router.Frame(&h.ops)
	advanceClock()
}

// press queues a key press and release.
func (h *harness) press(n key.Name, mods key.Modifiers) {
	h.router.Queue(key.Event{Name: n, Modifiers: mods, State: key.Press})
	h.router.Queue(key.Event{Name: n, Modifiers: mods, State: key.Release})
}

// click queues a pointer press and release at a window position.
func (h *harness) click(x, y float32) {
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: imageF32Pt(x, y), Buttons: pointer.ButtonPrimary})
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: imageF32Pt(x, y)})
}
