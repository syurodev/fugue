package kit

import (
	"image"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
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
	// px is the device pixel scale of the frames; zero means 1.
	px float32
	// win is the headless render window of shot, opened on first use and,
	// like every glwin window, never released.
	win *headless.Window
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
	h.frameSize(t, 200, 100)
}

// frameSize lays one frame out at w×h like frame does, for a control wider
// or taller than the default box.
func (h *harness) frameSize(t *testing.T, w, hgt int) {
	t.Helper()
	h.ops.Reset()
	px := max(h.px, 1)
	device := unit.Metric{PxPerDp: px, PxPerSp: px}
	h.gtx = layout.Context{
		Ops:         &h.ops,
		Metric:      h.env.Metric(device),
		Constraints: layout.Exact(image.Pt(w, hgt)),
		Source:      h.router.Source(),
	}
	h.gtx.Now = h.now
	h.env.Begin(h.now)
}

// shot renders the frame's ops through the harness's headless window of size
// w×h (opened on the first call, so every shot of a harness has one size) and
// returns the top-left w×h pixels.
func (h *harness) shot(t *testing.T, w, hgt int) *image.RGBA {
	t.Helper()
	if h.win == nil {
		win, err := glwin.Open(w, hgt)
		if err != nil {
			t.Fatalf("open a headless window: %v", err)
		}
		h.win = win
	}
	if err := h.win.Frame(&h.ops); err != nil {
		t.Fatalf("render a frame: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, w, hgt))
	if err := h.win.Screenshot(img); err != nil {
		t.Fatalf("read the frame back: %v", err)
	}
	return img
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

// tap queues one key press and its release, the way a keystroke arrives.
func (h *harness) tap(name key.Name, mod key.Modifiers) {
	h.router.Queue(key.Event{Name: name, Modifiers: mod, State: key.Press})
	h.router.Queue(key.Event{Name: name, Modifiers: mod, State: key.Release})
}
