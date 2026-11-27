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
	"github.com/syurodev/fugue/ui/internal/osappearance"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type fakeMacTitleBarPlatform struct {
	metrics       macWindowMetrics
	doubleClick   macDoubleClickAction
	applyCalls    int
	lastView      uintptr
	lastFS        bool
	lastDragRect  image.Rectangle
	skipOnMetrics bool
}

func (f *fakeMacTitleBarPlatform) applyTitleBar(viewPtr uintptr, fullscreen bool, onMetrics func(macWindowMetrics)) {
	f.applyCalls++
	f.lastView = viewPtr
	f.lastFS = fullscreen
	if !f.skipOnMetrics && onMetrics != nil {
		onMetrics(f.metrics)
	}
}

func (f *fakeMacTitleBarPlatform) doubleClickAction() macDoubleClickAction {
	return f.doubleClick
}

func (f *fakeMacTitleBarPlatform) setDragRect(viewPtr uintptr, rect image.Rectangle) {
	f.lastView = viewPtr
	f.lastDragRect = rect
}

func macTitleStripSpec() macWindowMetrics {
	return macWindowMetrics{
		BarHeightDp:     28,
		ZoomButtonFrame: image.Rect(56, 8, 68, 20),
	}
}

func renderMacTitleStripShots(t *testing.T) []titleBarShot {
	t.Helper()
	size := image.Pt(shotWindowW, shotWindowH)
	specs := []struct {
		name string
		dark bool
	}{
		{"mactitlestrip-light", false},
		{"mactitlestrip-dark", true},
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
		sc.titleBar = newTitleBar(osDarwin)
		fake := &fakeMacTitleBarPlatform{metrics: macTitleStripSpec(), doubleClick: macDoubleClickZoom}
		sc.titleBar.platform = fake
		sc.titleBar.nativeView = 0x1001
		sc.titleBar.applyConfig(app.Config{Title: testTitleBarTitle, Decorated: true, Focused: true})
		sc.titleBar.reapplyPlatformChrome()
		shots = append(shots, titleBarShot{name: spec.name, img: renderShotFrames(t, win, env, sc, size, spec.name)})
	}
	return shots
}

// TestMacTitleStripUsesAppBgAndInsetsTrafficLights verifies FUG-70: macOS
// strip height and leading inset come from the platform's contentLayoutRect
// and zoom button frame measurements (not hard-coded 32 dp / 72 dp), sit on
// AppBg in light, dark, and System theme modes, and match golden PNGs.
func TestMacTitleStripUsesAppBgAndInsetsTrafficLights(t *testing.T) {
	t.Parallel()
	spec := macTitleStripSpec()
	wantBarH := int(spec.BarHeightDp)
	wantInset := spec.ZoomButtonFrame.Max.X + int(theme.SpaceMD)

	for _, shot := range renderMacTitleStripShots(t) {
		if isUniformRGBA(shot.img) {
			t.Fatalf("mac title strip shot %s rendered a blank image", shot.name)
		}
		shottest.Compare(t, shot.name, shot.img)
		bg := shot.img.RGBAAt(0, 0)
		for y := range wantBarH {
			assertAppBgSpan(t, shot, y, 0, wantInset, bg)
		}
	}

	// Verify System theme mode resolves AppBg on the macOS strip across OS shifts.
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	light, dark := theme.Light(sh), theme.Dark(sh)
	fakeOS := newFakeAppearanceSource(osappearance.Dark)
	sc, env, _, _ := newAppearanceTestScreens(t, fakeOS, settings.ThemeSystem)
	sc.titleBar = newTitleBar(osDarwin)
	fakePlat := &fakeMacTitleBarPlatform{metrics: spec}
	sc.titleBar.platform = fakePlat
	sc.titleBar.nativeView = 0x2002
	sc.titleBar.reapplyPlatformChrome()
	sc.applyLivePreferences(env, &light, &dark)

	var ops op.Ops
	gtx := layout.Context{
		Ops:         &ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(image.Pt(900, 400)),
	}
	if got := sc.titleBar.layout(env, &gtx, sc); got != wantBarH {
		t.Fatalf("measured mac strip height = %d, want %d", got, wantBarH)
	}
	if got := sc.titleBar.leftInset(&gtx, true); got != wantInset {
		t.Fatalf("measured mac leading inset = %d, want %d", got, wantInset)
	}
	if env.Th.Chrome().AppBg != dark.Chrome().AppBg {
		t.Fatalf("System+Dark AppBg = %+v, want %+v", env.Th.Chrome().AppBg, dark.Chrome().AppBg)
	}
}

func runMacDragStripHarness(t *testing.T, maximized, fullscreen bool, action macDoubleClickAction) (*titleBar, *input.Router, *fakeMacTitleBarPlatform, f32.Point) {
	t.Helper()
	fake := &fakeMacTitleBarPlatform{metrics: macTitleStripSpec(), doubleClick: action}
	bar := newTitleBar(osDarwin)
	bar.platform = fake
	bar.nativeView = 0x3003
	env := newFUG65Env(t, true)
	router := new(input.Router)
	size := image.Pt(1000, 400)
	now := time.Unix(1_700_000_000, 0)

	cfg := app.Config{Title: testTitleBarTitle, Decorated: true, Focused: true}
	if maximized {
		cfg.Mode = app.Maximized
	}
	if fullscreen {
		cfg.Mode = app.Fullscreen
	}
	bar.applyConfig(cfg)
	bar.reapplyPlatformChrome()

	var ops op.Ops
	frame := func() {
		ops.Reset()
		gtx := layout.Context{
			Ops:         &ops,
			Source:      router.Source(),
			Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
			Constraints: layout.Exact(size),
			Now:         now,
		}
		env.Begin(now)
		bar.layout(env, &gtx, nil)
		env.FrameEnd(&gtx)
		router.Frame(&ops)
		now = now.Add(16 * time.Millisecond)
	}
	frame()
	dragPt := f32.Pt(float32(size.X/2), float32(macTitleStripSpec().BarHeightDp)/2)
	for range 2 {
		router.Queue(pointer.Event{Kind: pointer.Press, Position: dragPt, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
		router.Queue(pointer.Event{Kind: pointer.Release, Position: dragPt, Source: pointer.Mouse})
		frame()
	}
	frame()
	return bar, router, fake, dragPt
}

type macDragCase struct {
	name       string
	maximized  bool
	fullscreen bool
	setting    macDoubleClickAction
	wantMove   bool
	wantAction system.Action
}

func assertMacDragCase(t *testing.T, tc macDragCase) {
	t.Helper()
	bar, router, fake, dragPt := runMacDragStripHarness(t, tc.maximized, tc.fullscreen, tc.setting)
	act, ok := router.ActionAt(dragPt)
	if tc.wantMove != (ok && act == system.ActionMove) {
		t.Fatalf("ActionAt(drag) = (%v, %v), wantMove=%v", act, ok, tc.wantMove)
	}
	if insetAct, insetOK := router.ActionAt(f32.Pt(20, dragPt.Y)); insetOK && insetAct == system.ActionMove {
		t.Fatalf("traffic-light inset ActionAt = (%v, %v), want no ActionMove", insetAct, insetOK)
	}
	if tc.wantMove == fake.lastDragRect.Empty() {
		t.Fatalf("platform drag rect = %v, wantMove=%v", fake.lastDragRect, tc.wantMove)
	}
	if got := bar.takeActions(); got != tc.wantAction {
		t.Fatalf("double-click action = %v, want %v", got, tc.wantAction)
	}
}

// TestMacTitleStripDragAndDoubleClick verifies FUG-70: the macOS strip
// registers ActionMove across the open span (excluding the traffic-light inset),
// syncs the drag rect with the platform, honors AppleActionOnDoubleClick
// (Maximize/Unmaximize, Minimize, None), and disables drag in fullscreen.
func TestMacTitleStripDragAndDoubleClick(t *testing.T) {
	t.Parallel()
	for _, tc := range []macDragCase{
		{"windowed zoom maximizes", false, false, macDoubleClickZoom, true, system.ActionMaximize},
		{"maximized zoom unmaximizes", true, false, macDoubleClickZoom, true, system.ActionUnmaximize},
		{"minimize action", false, false, macDoubleClickMinimize, true, system.ActionMinimize},
		{"none action", false, false, macDoubleClickNone, true, 0},
		{"fullscreen disables drag and double-click", false, true, macDoubleClickZoom, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assertMacDragCase(t, tc)
		})
	}
}
