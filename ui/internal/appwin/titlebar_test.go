package appwin

import (
	"image"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/input"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestNativeChromePlatformsKeepNativeDecorations pins FUG-58's platform
// switch: macOS (and Windows, mobile, web) get no option and no app-drawn
// bar, whatever the platform reports; the Linux family opts out of Gio's
// Material fallback and draws its own only when the platform gives none.
func TestNativeChromePlatformsKeepNativeDecorations(t *testing.T) {
	t.Parallel()
	for _, goos := range []string{"darwin", "windows", "android", "ios", "js"} {
		if opts := windowOptions(goos); len(opts) != 0 {
			t.Errorf("%s: got %d window options, want none (native title bar)", goos, len(opts))
		}
		bar := newTitleBar(goos)
		bar.applyConfig(app.Config{Decorated: false})
		if bar.enabled {
			t.Errorf("%s: custom title bar enabled; native decorations must stay", goos)
		}
	}
	if opts := windowOptions("linux"); len(opts) != 1 {
		t.Errorf("linux: got %d window options, want the Decorated(false) opt-out", len(opts))
	}
	for _, tc := range []struct {
		name string
		cfg  app.Config
		want bool
	}{
		{"server-side decorations granted", app.Config{Decorated: true}, false},
		{"no decorations (GNOME Wayland)", app.Config{Decorated: false}, true},
		{"fullscreen", app.Config{Decorated: false, Mode: app.Fullscreen}, false},
	} {
		bar := newTitleBar("linux")
		bar.applyConfig(tc.cfg)
		if bar.enabled != tc.want {
			t.Errorf("linux %s: enabled = %v, want %v", tc.name, bar.enabled, tc.want)
		}
	}
}

// titleBarHarness lays the bar out through a real input router.
type titleBarHarness struct {
	t      *testing.T
	env    *kit.Env
	bar    *titleBar
	router input.Router
	ops    op.Ops
	size   image.Point
	now    time.Time
}

const testTitleBarTitle = "Fugue"

func newTitleBarHarness(t *testing.T, maximized bool) *titleBarHarness {
	t.Helper()
	h := &titleBarHarness{t: t, env: newInboxTestEnv(t), bar: newTitleBar("linux"), size: image.Pt(900, 400), now: time.Unix(1_700_000_000, 0)}
	cfg := app.Config{Title: testTitleBarTitle, Focused: true}
	if maximized {
		cfg.Mode = app.Maximized
	}
	h.bar.applyConfig(cfg)
	h.frame()
	return h
}

func (h *titleBarHarness) frame() {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Metric: h.env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(h.size), Source: h.router.Source(), Now: h.now}
	h.env.Begin(h.now)
	h.bar.layout(h.env, &gtx)
	h.env.FrameEnd(&gtx)
	h.router.Frame(&h.ops)
	h.now = h.now.Add(time.Second / 60)
}

// tap presses and releases at p; clicks is the press's click count.
func (h *titleBarHarness) tap(p f32.Point, clicks int) {
	for range clicks {
		h.router.Queue(pointer.Event{Kind: pointer.Press, Position: p, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
		h.router.Queue(pointer.Event{Kind: pointer.Release, Position: p, Source: pointer.Mouse})
		h.frame()
	}
	h.frame()
}

func TestTitleBarActions(t *testing.T) {
	t.Parallel()
	y := float32(theme.Tab) / 2
	for _, tc := range []struct {
		name      string
		maximized bool
		at        f32.Point
		clicks    int
		want      system.Action
	}{
		{"close button", false, f32.Pt(880, y), 1, system.ActionClose},
		{"minimize button", false, f32.Pt(800, y), 1, system.ActionMinimize},
		{"maximize button", false, f32.Pt(840, y), 1, system.ActionMaximize},
		{"restore button when maximized", true, f32.Pt(840, y), 1, system.ActionUnmaximize},
		{"double click on the title maximizes", false, f32.Pt(300, y), 2, system.ActionMaximize},
		{"double click on the title restores", true, f32.Pt(300, y), 2, system.ActionUnmaximize},
	} {
		h := newTitleBarHarness(t, tc.maximized)
		h.tap(tc.at, tc.clicks)
		if got := h.bar.takeActions(); got != tc.want {
			t.Errorf("%s: actions = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestTitleBarMoveAction checks that the title area is registered as the
// window-move area (drag to move), and the buttons are not.
func TestTitleBarMoveAction(t *testing.T) {
	t.Parallel()
	h := newTitleBarHarness(t, false)
	y := float32(theme.Tab) / 2
	if act, ok := h.router.ActionAt(f32.Pt(300, y)); !ok || act != system.ActionMove {
		t.Errorf("title area action = %v (%v), want ActionMove", act, ok)
	}
	if act, ok := h.router.ActionAt(f32.Pt(880, y)); ok && act == system.ActionMove {
		t.Errorf("close button moves the window: %v", act)
	}
}

func TestTitleBarShots(t *testing.T) {
	t.Parallel()
	for _, shot := range renderTitleBarShots(t) {
		if isUniformRGBA(shot.img) {
			t.Fatalf("title bar shot %s rendered a blank image", shot.name)
		}
		shottest.Compare(t, shot.name, shot.img)
	}
}

type titleBarShot struct {
	name string
	img  *image.RGBA
}

// renderTitleBarShots renders the shell under the app-drawn title bar in
// light and dark, the second with maximize shown as restore.
func renderTitleBarShots(t *testing.T) []titleBarShot {
	t.Helper()
	size := image.Pt(shotWindowW, shotWindowH)
	shots := make([]titleBarShot, 0, 2)
	for _, spec := range []struct {
		name      string
		dark      bool
		maximized bool
	}{{"titlebar-light", false, false}, {"titlebar-dark", true, true}} {
		win, err := glwin.Open(size.X, size.Y)
		if err != nil {
			t.Fatalf("open headless window for %s: %v", spec.name, err)
		}
		env := newInboxTestEnv(t)
		if spec.dark {
			th := theme.Dark(env.Sh)
			env.Th = &th
		}
		manager := hostconn.NewManager(&hostconn.Config{})
		sc, _ := newMultiWindowShotScreens(manager)
		sc.titleBar = newTitleBar("linux")
		cfg := app.Config{Title: testTitleBarTitle, Focused: true}
		if spec.maximized {
			cfg.Mode = app.Maximized
		}
		sc.titleBar.applyConfig(cfg)
		shots = append(shots, titleBarShot{name: spec.name, img: renderShotFrames(t, win, env, sc, size, spec.name)})
	}
	return shots
}

// TestTitleBarNoBottomSeam verifies FUG-71: the bottom row of the app-drawn
// title bar (y = barH - 1) carries no BorderSubtle separator line in light or
// dark theme.
func TestTitleBarNoBottomSeam(t *testing.T) {
	t.Parallel()
	seamY := int(theme.Tab) - 1
	for _, shot := range renderTitleBarShots(t) {
		bg := shot.img.RGBAAt(0, 0)
		for x := range shot.img.Bounds().Dx() {
			if got := shot.img.RGBAAt(x, seamY); got != bg {
				t.Fatalf("%s: pixel at (%d, %d) = %+v, want AppBg %+v (no bottom seam)", shot.name, x, seamY, got, bg)
			}
		}
	}
}

// TestTitleBarTopGapMatchesLeftGutter verifies FUG-71: the gap between the
// bottom of the title bar and the top of the slot grid container equals the
// left gutter between the sidebar and the slot grid container in px at both
// 100% and 125% UI scale.
func TestTitleBarTopGapMatchesLeftGutter(t *testing.T) {
	t.Parallel()
	for _, scale := range []float32{1.0, 1.25} {
		for _, light := range []bool{true, false} {
			checkTitleBarShellGaps(t, scale, light)
		}
	}
}

func checkTitleBarShellGaps(t *testing.T, scale float32, light bool) {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: scale, ReducedMotion: true}
	sc, _ := buildFUG65ShotScreens(t, shotFUG65Shell3Slot)
	sc.titleBar = newTitleBar("linux")
	sc.titleBar.applyConfig(app.Config{Title: testTitleBarTitle, Focused: true})

	size := image.Pt(1500, 900)
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window (scale=%.2f, light=%v): %v", scale, light, err)
	}
	img := renderShotFrames(t, win, env, sc, size, "titlebar-gap")

	gtx := layout.Context{Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	barH := sc.titleBar.height(&gtx)
	sidebarRight := gtx.Dp(theme.RailWidth) + gtx.Dp(theme.SidebarWidth)
	topGap, leftGutter := measureGridGaps(img, barH, sidebarRight)
	if topGap <= 0 || leftGutter <= 0 {
		t.Fatalf("scale=%.2f light=%v: failed to locate grid edges (topGap=%d, leftGutter=%d)", scale, light, topGap, leftGutter)
	}
	if topGap != leftGutter {
		t.Errorf("scale=%.2f light=%v: top gap = %dpx, left gutter = %dpx, want equal", scale, light, topGap, leftGutter)
	}
}

func measureGridGaps(img *image.RGBA, barH, sidebarRight int) (topGap, leftGutter int) {
	b := img.Bounds()
	bg := img.RGBAAt(0, 0)
	midX, midY := b.Dx()/2, b.Dy()/2
	topGap, leftGutter = -1, -1
	for y := barH; y < b.Dy(); y++ {
		if img.RGBAAt(midX, y) != bg {
			topGap = y - barH
			break
		}
	}
	for x := sidebarRight; x < b.Dx(); x++ {
		if img.RGBAAt(x, midY) != bg {
			leftGutter = x - sidebarRight
			break
		}
	}
	return topGap, leftGutter
}
