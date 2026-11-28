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

func applyWindowOpts(opts []app.Option) app.Config {
	cfg := app.Config{Decorated: true}
	for _, opt := range opts {
		opt(unit.Metric{PxPerDp: 1, PxPerSp: 1}, &cfg)
	}
	return cfg
}

// TestWindowOptionsPerOS pins the per-OS window decoration split (FUG-58,
// FUG-97, FUG-98, FUG-102):
//   - darwin opts out of Gio's fallback Material decorations via Decorated(false)
//     while keeping Linux CSD disabled (macOS uses native traffic lights).
//   - Windows, mobile, and web return no options and never enable Linux CSD.
//   - The Linux/BSD family opts out of Gio's fallback and draws CSD controls
//     only when the platform reports Decorated=false outside fullscreen.
func TestWindowOptionsPerOS(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos      string
		wantOpt   bool
		wantOwn   bool
		wantCSDOn bool
	}{
		{goos: osDarwin, wantOpt: true, wantOwn: false, wantCSDOn: false},
		{goos: "windows", wantOpt: false, wantOwn: false, wantCSDOn: false},
		{goos: "android", wantOpt: false, wantOwn: false, wantCSDOn: false},
		{goos: "ios", wantOpt: false, wantOwn: false, wantCSDOn: false},
		{goos: "js", wantOpt: false, wantOwn: false, wantCSDOn: false},
		{goos: "linux", wantOpt: true, wantOwn: true, wantCSDOn: true},
		{goos: "freebsd", wantOpt: true, wantOwn: true, wantCSDOn: true},
		{goos: "openbsd", wantOpt: true, wantOwn: true, wantCSDOn: true},
		{goos: "netbsd", wantOpt: true, wantOwn: true, wantCSDOn: true},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			t.Parallel()
			assertPlatformWindowOptions(t, tc.goos, tc.wantOpt, tc.wantOwn, tc.wantCSDOn)
		})
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

func assertPlatformWindowOptions(t *testing.T, goos string, wantOpt, wantOwn, wantCSDOn bool) {
	t.Helper()
	if got := drawsOwnChrome(goos); got != wantOwn {
		t.Errorf("drawsOwnChrome(%q) = %v, want %v", goos, got, wantOwn)
	}
	if got := optsOutOfGioDecorations(goos); got != wantOpt {
		t.Errorf("optsOutOfGioDecorations(%q) = %v, want %v", goos, got, wantOpt)
	}
	opts := windowOptions(goos)
	wantLen := 0
	if wantOpt {
		wantLen = 1
	}
	if len(opts) != wantLen {
		t.Fatalf("windowOptions(%q): got %d options, want %d", goos, len(opts), wantLen)
	}
	if cfg := applyWindowOpts(opts); cfg.Decorated == wantOpt {
		t.Errorf("windowOptions(%q): Decorated = %v, want %v", goos, cfg.Decorated, !wantOpt)
	}
	bar := newTitleBar(goos)
	bar.applyConfig(app.Config{Decorated: false})
	if bar.csd != wantCSDOn {
		t.Errorf("%s: csd = %v, want %v", goos, bar.csd, wantCSDOn)
	}
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if got, want := bar.height(&gtx), gtx.Dp(theme.TopBarHeight); got != want {
		t.Errorf("%s: top bar height = %d, want %d", goos, got, want)
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
	// With 24 dp controls, 4 dp gaps, and 8 dp right padding (88 dp cluster)
	// in a 900 dp bar, the three button centers sit at 824, 852, and 880.
	const (
		minX   = 824
		maxX   = 852
		closeX = 880
	)
	for _, tc := range []struct {
		name      string
		maximized bool
		at        f32.Point
		clicks    int
		want      system.Action
	}{
		{"close button", false, f32.Pt(closeX, y), 1, system.ActionClose},
		{"minimize button", false, f32.Pt(minX, y), 1, system.ActionMinimize},
		{"maximize button", false, f32.Pt(maxX, y), 1, system.ActionMaximize},
		{"restore button when maximized", true, f32.Pt(maxX, y), 1, system.ActionUnmaximize},
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
// window-move area (drag to move), and the compact CSD buttons, gaps, and right
// padding are not, while non-CSD platforms reserve no CSD cluster width.
func TestTitleBarMoveAction(t *testing.T) {
	t.Parallel()
	h := newTitleBarHarness(t, false)
	y := float32(theme.TopBarHeight) / 2
	if act, ok := h.router.ActionAt(f32.Pt(300, y)); !ok || act != system.ActionMove {
		t.Errorf("title area action = %v (%v), want ActionMove", act, ok)
	}
	for _, tc := range []struct {
		label string
		x     float32
	}{
		{"minimize button", 824},
		{"first inter-button gap", 838},
		{"maximize button", 852},
		{"second inter-button gap", 866},
		{"close button", 880},
		{"right padding", 896},
	} {
		if act, ok := h.router.ActionAt(f32.Pt(tc.x, y)); ok && act == system.ActionMove {
			t.Errorf("%s (x=%.0f) moves the window: %v", tc.label, tc.x, act)
		}
	}

	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if m := h.bar.computeMetrics(&gtx, nil); m.csdW != 88 {
		t.Errorf("linux CSD cluster width = %d, want 88", m.csdW)
	}
	for _, tc := range []struct {
		goos string
		cfg  app.Config
	}{
		{"linux", app.Config{Decorated: true}},
		{"windows", app.Config{Decorated: false}},
		{osDarwin, app.Config{Decorated: false}},
	} {
		bar := newTitleBar(tc.goos)
		bar.applyConfig(tc.cfg)
		if m := bar.computeMetrics(&gtx, nil); bar.csd || m.csdW != 0 {
			t.Errorf("%s (decorated=%v): csd=%v csdW=%d, want false and 0", tc.goos, tc.cfg.Decorated, bar.csd, m.csdW)
		}
	}
}

func TestTitleBarShots(t *testing.T) {
	t.Parallel()
	for _, shot := range renderTitleBarShots(t) {
		if isUniformRGBA(shot.img) {
			t.Fatalf("title bar shot %s rendered a blank image", shot.name)
		}
		shottest.Compare(t, shot.name, shot.img)
		assertCSDGlyphBounds(t, shot)
	}
}

func assertCSDGlyphBounds(t *testing.T, shot titleBarShot) {
	t.Helper()
	w := shot.img.Bounds().Dx()
	barH := int(theme.TopBarHeight)
	btnW := int(theme.ControlXS)
	gap := int(theme.SpaceXS)
	padR := int(theme.SpaceMD)
	csdW := 3*btnW + 2*gap + padR
	closeBox := glyphBoundingBox(shot.img, image.Rect(w-padR-btnW, 0, w-padR, barH))
	maxBox := glyphBoundingBox(shot.img, image.Rect(w-padR-2*btnW-gap, 0, w-padR-btnW-gap, barH))
	minBox := glyphBoundingBox(shot.img, image.Rect(w-csdW, 0, w-csdW+btnW, barH))
	themeX1 := w - csdW - gap - btnW - gap
	themeBox := glyphBoundingBox(shot.img, image.Rect(themeX1-btnW, 0, themeX1, barH))

	assertGlyphWithin(t, shot.name, "csd-minimize", minBox, themeBox, 12, true)
	assertGlyphWithin(t, shot.name, "csd-close", closeBox, themeBox, 12, true)
	if shot.name == "titlebar-light" || shot.name == "titlebar-normal-dark" {
		assertGlyphWithin(t, shot.name, "csd-maximize", maxBox, themeBox, 12, true)
		return
	}
	// Restore uses IconCopy at IconSM (14 dp), spanning 14x14 px (smaller than 16 dp IconMD).
	assertGlyphWithin(t, shot.name, "csd-restore", maxBox, themeBox, int(theme.IconSM), false)
}

func assertGlyphWithin(t *testing.T, shotName, label string, box, themeBox image.Point, maxSide int, compareTheme bool) {
	t.Helper()
	if box.X == 0 || box.Y == 0 {
		t.Fatalf("%s %s glyph rendered empty: %v", shotName, label, box)
	}
	if box.X > maxSide || box.Y > maxSide {
		t.Errorf("%s %s glyph box = %v, want <= %dx%d px at scale 1.0", shotName, label, box, maxSide, maxSide)
	}
	if compareTheme && (box.X > themeBox.X+1 || box.Y > themeBox.Y+1) {
		t.Errorf("%s %s glyph box = %v exceeds theme icon box %v by >1 px", shotName, label, box, themeBox)
	}
}

func glyphBoundingBox(img *image.RGBA, area image.Rectangle) image.Point {
	bg := img.RGBAAt(0, 0)
	minX, minY := area.Max.X, area.Max.Y
	maxX, maxY := area.Min.X-1, area.Min.Y-1
	for y := area.Min.Y; y < area.Max.Y; y++ {
		for x := area.Min.X; x < area.Max.X; x++ {
			if img.RGBAAt(x, y) != bg {
				minX = min(minX, x)
				minY = min(minY, y)
				maxX = max(maxX, x)
				maxY = max(maxY, y)
			}
		}
	}
	if maxX < minX || maxY < minY {
		return image.Point{}
	}
	return image.Pt(maxX-minX+1, maxY-minY+1)
}

type titleBarShot struct {
	name string
	img  *image.RGBA
}

// renderTitleBarShots renders the shell under the app-drawn Linux CSD title bar
// in normal and maximized states for both light and dark themes.
func renderTitleBarShots(t *testing.T) []titleBarShot {
	t.Helper()
	size := image.Pt(shotWindowW, shotWindowH)
	specs := []struct {
		name      string
		dark      bool
		maximized bool
	}{
		{"titlebar-light", false, false},
		{"titlebar-dark", true, true},
		{"titlebar-normal-dark", true, false},
		{"titlebar-maximized-light", false, true},
	}
	shots := make([]titleBarShot, 0, len(specs))
	for _, spec := range specs {
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
		sc.osAppearance = newShotAppearanceSource(!spec.dark)
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

// TestTitleBarTopGapMatchesLeftGutter verifies FUG-71 / FUG-95 / FUG-97:
// borderless islands start flush under the 32 dp Top Bar (topGap == 0),
// adjacent islands are separated by the exact 4 dp IslandGap (4 px at 1.0,
// 5 px at 1.25) of AppBg, and island edges draw CanvasBg directly with no
// BorderSubtle outline stroke.
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
	sc.osAppearance = newShotAppearanceSource(light)
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
	wantGap := gtx.Dp(theme.IslandGap)
	canvas := color.RGBA{R: th.Chrome().CanvasBg.R, G: th.Chrome().CanvasBg.G, B: th.Chrome().CanvasBg.B, A: 255}
	topGap, leftGutter := measureGridGaps(img, barH, sidebarRight, canvas)
	if topGap != 0 {
		t.Errorf("scale=%.2f light=%v: top gap = %dpx, want 0px (islands flush under Top Bar)", scale, light, topGap)
	}
	if leftGutter != wantGap {
		t.Errorf("scale=%.2f light=%v: left gutter = %dpx, want exact %dpx (borderless islands)", scale, light, leftGutter, wantGap)
	}

	appBg := img.RGBAAt(0, 0)
	midY := img.Bounds().Dy() / 2
	assertIslandEdgeBorderless(t, img, "sidebar-main", sidebarRight, wantGap, midY, canvas, appBg, scale, light)

	gridLeft := sidebarRight + wantGap
	col0 := sc.grid.rects[Pos{Col: 0, Row: 0}.key()]
	col1 := sc.grid.rects[Pos{Col: 1, Row: 0}.key()]
	if colGap := col1.Min.X - col0.Max.X; colGap != wantGap {
		t.Errorf("scale=%.2f light=%v: grid col gap = %dpx, want %dpx", scale, light, colGap, wantGap)
	}
	// Probe inside row 0 well away from col 1's rounded row-split corner near midY.
	row0Y := barH + gtx.Dp(theme.SlotHeaderHeight) + gtx.Dp(theme.SpaceXXL)
	assertIslandEdgeBorderless(t, img, "col0-col1", gridLeft+col0.Max.X, wantGap, row0Y, canvas, appBg, scale, light)
}

func assertIslandEdgeBorderless(t *testing.T, img *image.RGBA, label string, gutterStart, gapPx, y int, canvas, appBg color.RGBA, scale float32, light bool) {
	t.Helper()
	if got := img.RGBAAt(gutterStart-1, y); got != canvas {
		t.Errorf("scale=%.2f light=%v %s left edge (%d,%d) = %+v, want CanvasBg %+v (no BorderSubtle stroke)",
			scale, light, label, gutterStart-1, y, got, canvas)
	}
	for x := gutterStart; x < gutterStart+gapPx; x++ {
		if got := img.RGBAAt(x, y); got != appBg {
			t.Errorf("scale=%.2f light=%v %s gutter (%d,%d) = %+v, want AppBg %+v",
				scale, light, label, x, y, got, appBg)
		}
	}
	if got := img.RGBAAt(gutterStart+gapPx, y); got != canvas {
		t.Errorf("scale=%.2f light=%v %s right edge (%d,%d) = %+v, want CanvasBg %+v (no BorderSubtle stroke)",
			scale, light, label, gutterStart+gapPx, y, got, canvas)
	}
}

func measureGridGaps(img *image.RGBA, barH, sidebarRight int, canvas color.RGBA) (topGap, leftGutter int) {
	b := img.Bounds()
	midX, midY := b.Dx()/2, b.Dy()/2
	topGap, leftGutter = -1, -1
	for y := barH; y < b.Dy(); y++ {
		if img.RGBAAt(midX, y) == canvas {
			topGap = y - barH
			break
		}
	}
	for x := sidebarRight; x < b.Dx(); x++ {
		if img.RGBAAt(x, midY) == canvas {
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
	btnW := gtx.Dp(theme.ControlXS)
	btnGap := gtx.Dp(theme.SpaceXS)
	rightPad := max((gtx.Dp(theme.RightStripeWidth)-btnW)/2, 0)
	if sc.titleBar.csd {
		_, _, csdW := csdButtonMetrics(&gtx)
		rightPad = csdW + btnGap
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

// TestTitleBarApplyViewEventClearsStaleNativeView verifies that an invalid
// ViewEvent clears any previously recorded nativeView handle so a subsequent
// reapplyPlatformChrome cannot pass a freed NSView pointer to CFRetain.
func TestTitleBarApplyViewEventClearsStaleNativeView(t *testing.T) {
	t.Parallel()
	bar := newTitleBar(osDarwin)
	bar.nativeView = 0xdeadbeef
	bar.applyViewEvent(nil)
	if bar.nativeView != 0 {
		t.Fatalf("nativeView = %#x after invalid ViewEvent, want 0", bar.nativeView)
	}
	bar.reapplyPlatformChrome()
}
