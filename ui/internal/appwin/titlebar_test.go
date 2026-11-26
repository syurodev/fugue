package appwin

import (
	"image"
	"image/color"
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

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
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
		if bar.csd {
			t.Errorf("%s: Linux CSD chrome enabled; native decorations must stay", goos)
		}
		var ops op.Ops
		gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
		if got, want := bar.height(&gtx), gtx.Dp(theme.TopBarHeight); got != want {
			t.Errorf("%s: top bar height = %d, want %d", goos, got, want)
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
		if bar.csd != tc.want {
			t.Errorf("linux %s: csd = %v, want %v", tc.name, bar.csd, tc.want)
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
	h.bar.layout(h.env, &gtx, nil)
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
	y := float32(theme.TopBarHeight) / 2
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
	y := float32(theme.TopBarHeight) / 2
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

// TestTitleBarNoBottomSeam verifies FUG-71: the bottom of the app-drawn
// title bar carries no BorderSubtle separator line in light or dark theme
// (testing across all x outside the adjacent island top-border AA overhang,
// and in the open Rail/gutter/Right-Stripe columns at y = barH - 1).
func TestTitleBarNoBottomSeam(t *testing.T) {
	t.Parallel()
	barH := int(theme.TopBarHeight)
	seamY := barH - 1
	for _, shot := range renderTitleBarShots(t) {
		bg := shot.img.RGBAAt(0, 0)
		w := shot.img.Bounds().Dx()
		assertAppBgSpan(t, shot, seamY-1, 0, w, bg)
		// Directly under the Left Rail and Right Stripe at y = barH - 1, no island stroke sits below.
		assertAppBgSpan(t, shot, seamY, 0, int(theme.RailWidth), bg)
		assertAppBgSpan(t, shot, seamY, w-int(theme.RightStripeWidth), w, bg)
	}
}

func assertAppBgSpan(t *testing.T, shot titleBarShot, y, fromX, toX int, bg color.RGBA) {
	t.Helper()
	for x := fromX; x < toX; x++ {
		if got := shot.img.RGBAAt(x, y); got != bg {
			t.Fatalf("%s: pixel at (%d, %d) = %+v, want AppBg %+v (no bottom seam)", shot.name, x, y, got, bg)
		}
	}
}

// TestTitleBarTopGapMatchesLeftGutter verifies FUG-71 / FUG-95: the islands
// start flush under the 32 dp Top Bar (topGap == 0) while the gutter between
// the Sidebar Island and the slot grid equals IslandGap (4 dp, allowing 1 px
// for the centered BorderSubtle stroke overhang).
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
	if topGap != 0 {
		t.Errorf("scale=%.2f light=%v: top gap = %dpx, want 0px (islands flush under Top Bar)", scale, light, topGap)
	}
	if wantGap := gtx.Dp(theme.IslandGap); leftGutter < wantGap-1 || leftGutter > wantGap {
		t.Errorf("scale=%.2f light=%v: left gutter = %dpx, want %d..%dpx (IslandGap minus centered stroke)", scale, light, leftGutter, wantGap-1, wantGap)
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
	for x := sidebarRight + 1; x < b.Dx(); x++ {
		if img.RGBAAt(x, midY) != bg {
			leftGutter = x - sidebarRight
			break
		}
	}
	return topGap, leftGutter
}

// TestTopBarThemeToggleAndSettings verifies FUG-95: clicking Quick Theme Toggle
// flips ThemeMode and schedules an immediate redraw for the next frame instead of
// mutating env.Th mid-draw, clicking Settings toggles KindSettings, and unsaved
// settings fields surface in the Settings button tooltip.
func TestTopBarThemeToggleAndSettings(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1200, 800)
	now := time.Unix(1_700_000_000, 0)

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}), Constraints: layout.Exact(size)}
	runShellEnvFrame(t, env, sc, router, size, now)

	barH := gtx.Dp(theme.TopBarHeight)
	btnW := gtx.Dp(theme.ControlSM)
	btnGap := gtx.Dp(theme.SpaceXS)
	rightPad := max((gtx.Dp(theme.RightStripeWidth)-btnW)/2, 0)
	if sc.titleBar.csd {
		rightPad = 3*(gtx.Dp(theme.SpaceXXXL)+gtx.Dp(theme.SpaceMD)) + btnGap
	}
	settingsX := float32(size.X - rightPad - btnW/2)
	themeX := settingsX - float32(btnW+btnGap)
	y := float32(barH) / 2

	// Click Theme Toggle -> switches light to dark cleanly for the next frame.
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, f32.Pt(themeX, y))
	if sc.settings.Prefs.Current.ThemeMode != "dark" || !wantsImmediateFrame(env) {
		t.Fatalf("Theme Toggle clicked: themeMode=%q wantsImmediateFrame=%v", sc.settings.Prefs.Current.ThemeMode, wantsImmediateFrame(env))
	}
	if env.Th.Chrome().CanvasBg.R < 128 {
		t.Fatal("env.Th switched mid-draw during Theme Toggle click frame; expected deferred next-frame switch")
	}

	// Dirty settings -> hover Settings button -> "Settings (unsaved changes)".
	sc.settings.Draft.IdleDetachSeconds = 999
	router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(settingsX, y)})
	runShellEnvFrame(t, env, sc, router, size, now.Add(time.Second))
	runShellEnvFrame(t, env, sc, router, size, now.Add(2*time.Second))
	if got := env.TooltipText(); got != "Settings (unsaved changes)" {
		t.Fatalf("Settings tooltip = %q, want \"Settings (unsaved changes)\"", got)
	}

	// Click Settings -> opens KindSettings; click again -> closes KindSettings.
	now = now.Add(3 * time.Second)
	clickShellAt(t, env, sc, router, size, now, f32.Pt(settingsX, y))
	if !sc.hasSlotKind(KindSettings) {
		t.Fatal("clicking Top Bar Settings button did not open KindSettings")
	}
	now = now.Add(16 * time.Millisecond)
	clickShellAt(t, env, sc, router, size, now, f32.Pt(settingsX, y))
	if sc.hasSlotKind(KindSettings) {
		t.Fatal("clicking Top Bar Settings button a second time did not close KindSettings")
	}
}

// TestStatusBarBreadcrumbAndVaultLock verifies FUG-95: Top Bar context pill and
// Bottom Status Bar breadcrumb format the active workspace and session, and switch
// cleanly with the Left Rail section.
func TestStatusBarBreadcrumbAndVaultLock(t *testing.T) {
	t.Parallel()
	sc := newFUG65TestScreens(t)
	sc.workspaces.ApplyList([]workspace.Summary{{ID: "ws-1", Name: "alpha"}})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "sess-1", Vendor: client.VendorClaude, WorkspaceID: "ws-1"},
	}})
	sc.openSession("sess-1")
	if got := sc.topBarContext(); got != "alpha / claude" {
		t.Fatalf("topBarContext = %q, want \"alpha / claude\"", got)
	}
	if got := sc.statusBarBreadcrumb(); got != "alpha > claude" {
		t.Fatalf("statusBarBreadcrumb = %q, want \"alpha > claude\"", got)
	}
	sc.grid.selectRail(railTeam)
	if got := sc.topBarContext(); got != teamLabel {
		t.Fatalf("railTeam topBarContext = %q, want %q", got, teamLabel)
	}
	if got := sc.statusBarBreadcrumb(); got != teamLabel {
		t.Fatalf("railTeam statusBarBreadcrumb = %q, want %q", got, teamLabel)
	}
}

// TestDarwinTitleBarTrafficLightInset verifies FUG-96: macOS Top Bar reserves
// 72 dp on the left for native traffic lights in windowed mode and collapses to
// RailWidth in fullscreen mode.
func TestDarwinTitleBarTrafficLightInset(t *testing.T) {
	t.Parallel()
	bar := newTitleBar("darwin")
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	bar.applyConfig(app.Config{Mode: app.Windowed})
	if got, want := bar.leftInset(&gtx, true), gtx.Dp(theme.MacTrafficLightInset); got != want {
		t.Fatalf("darwin windowed leftInset = %d, want %d", got, want)
	}
	bar.applyConfig(app.Config{Mode: app.Fullscreen})
	if got, want := bar.leftInset(&gtx, true), gtx.Dp(theme.RailWidth); got != want {
		t.Fatalf("darwin fullscreen leftInset = %d, want %d", got, want)
	}
}

// TestDarwinTitleBarMoveAction verifies windowed macOS Top Bar registers
// system.ActionMove across the empty drag span (for native Gio/AppKit drag)
// while leaving traffic-light insets and fullscreen mode un-registered.
func TestDarwinTitleBarMoveAction(t *testing.T) {
	t.Parallel()
	bar := newTitleBar(osDarwin)
	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1000, 32)
	var ops op.Ops

	render := func(cfg app.Config) {
		bar.applyConfig(cfg)
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Source:      router.Source(),
			Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
			Constraints: layout.Exact(size),
		}
		bar.layout(env, &gtx, nil)
		router.Frame(gtx.Ops)
	}

	render(app.Config{Mode: app.Windowed, Decorated: true, Focused: true, Title: "Fugue"})
	if act, ok := router.ActionAt(f32.Pt(float32(size.X/2), 16)); !ok || act != system.ActionMove {
		t.Fatalf("windowed darwin middle Top Bar ActionAt = (%v, %v), want (ActionMove, true)", act, ok)
	}
	if act, ok := router.ActionAt(f32.Pt(10, 16)); ok && act == system.ActionMove {
		t.Fatalf("windowed darwin traffic-light inset should not register ActionMove, got (%v, %v)", act, ok)
	}

	render(app.Config{Mode: app.Fullscreen, Decorated: true, Focused: true, Title: "Fugue"})
	if act, ok := router.ActionAt(f32.Pt(float32(size.X/2), 16)); ok && act == system.ActionMove {
		t.Fatalf("fullscreen darwin Top Bar should not register ActionMove, got (%v, %v)", act, ok)
	}
}
