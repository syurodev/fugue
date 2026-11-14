package kit

import (
	"image"
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
	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// frameStep is the fake frame interval, one 60 Hz frame.
const frameStep = time.Second / 60

// harness is a headless frame loop with an input router, the pattern Gio's
// own widget tests use: lay out, hand the ops to the router, queue events,
// lay out again (ui-guide §10).
type harness struct {
	router *input.Router
	ops    op.Ops
	env    *Env
	gtx    layout.Context
	now    time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build the shaper: %v", err)
	}
	th := theme.Dark(sh)
	return &harness{
		router: new(input.Router),
		env:    &Env{Th: &th, Sh: sh},
		now:    time.Unix(1_700_000_000, 0),
	}
}

// frame lays one frame out at 200×100, applying the env's UI scale to the
// device metric exactly as a window would.
func (h *harness) frame(t *testing.T) {
	t.Helper()
	h.ops.Reset()
	device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	h.gtx = layout.Context{
		Ops:         &h.ops,
		Metric:      h.env.Metric(device),
		Constraints: layout.Exact(image.Pt(200, 100)),
		Source:      h.router.Source(),
	}
	h.gtx.Now = h.now
	h.env.Begin(h.now)
}

// end hands the frame's ops to the router and advances the fake clock.
func (h *harness) end() {
	h.router.Frame(&h.ops)
	h.now = h.now.Add(frameStep)
}

// click queues a pointer press and release at a window position.
func (h *harness) click(x, y float32) {
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(x, y), Buttons: pointer.ButtonPrimary})
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: f32.Pt(x, y)})
}
