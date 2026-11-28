package appwin

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

// FUG-109 (ui-kit §3.4): every app chrome button — Left Rail, Right Stripe
// companions and folded slots, Top Bar theme and settings, Status Bar Lock —
// shows keyboard focus as the variant's hover wash instead of the outer
// ring. The headless rasterizer wobbles opaque pixels by ±1–2 LSB between
// frames, up to 4 near anti-aliased edges, while decisive ink starts at ~8
// (the wash stacked over the rail pill) and reaches ~50 (the old ring), so
// the wash asserts count pixels past the noise floor inside the button's box
// and the ring assert looks for ink past the ink floor outside it.

const (
	// fug109InkFloor is the ink floor for the ring check; the wash count
	// reads anything past 2, since the wash stacked over the rail pill
	// moves dark-theme fills by only ~4-5 LSB.
	fug109InkFloor = 8
	fug109MinInk   = 100
)

func fug109Delta(x, y color.RGBA) int {
	d := [4]int{int(x.R) - int(y.R), int(x.G) - int(y.G), int(x.B) - int(y.B), int(x.A) - int(y.A)}
	m := 0
	for _, v := range d {
		if v < 0 {
			v = -v
		}
		m = max(m, v)
	}
	return m
}

// fug109InkCount counts the pixels inside box whose channels moved past the
// noise floor between a and b — the wash fills the body, so a focused chrome
// button leaves hundreds of them and render noise leaves a handful.
func fug109InkCount(a, b *image.RGBA, box image.Rectangle) int {
	n := 0
	for y := box.Min.Y; y < box.Max.Y; y++ {
		for x := box.Min.X; x < box.Max.X; x++ {
			if fug109Delta(a.RGBAAt(x, y), b.RGBAAt(x, y)) > 2 {
				n++
			}
		}
	}
	return n
}

// fug109RingOutside returns the bounding box of the ink past the ink floor
// outside box, Empty when the render kept every decisive change inside it.
func fug109RingOutside(a, b *image.RGBA, box image.Rectangle) image.Rectangle {
	out := image.Rectangle{}
	for y := range a.Bounds().Dy() {
		for x := range a.Bounds().Dx() {
			if image.Pt(x, y).In(box) {
				continue
			}
			if fug109Delta(a.RGBAAt(x, y), b.RGBAAt(x, y)) > fug109InkFloor {
				out = out.Union(image.Rect(x, y, x+1, y+1))
			}
		}
	}
	return out
}

// fug109WashShown asserts one chrome site's focused render: the wash moved
// hundreds of pixels inside the box and no ring ink escaped it.
func fug109WashShown(t *testing.T, base, focused *image.RGBA, box image.Rectangle, site string) {
	t.Helper()
	if n := fug109InkCount(base, focused, box); n < fug109MinInk {
		t.Fatalf("%s: %d pixels moved past the noise floor inside %v; the focus wash never showed", site, n, box)
	}
	if out := fug109RingOutside(base, focused, box); !out.Empty() {
		t.Fatalf("%s: focus drew ink on %v outside the box %v; that is a ring, not the wash", site, out, box)
	}
}

// fug109Site is one chrome button's focus scene: its state tag, the box the
// chrome layout draws it in, and the window that draws it.
type fug109Site struct {
	name string
	tag  event.Tag
	box  image.Rectangle
	win  *headless.Window
	size image.Point
}

// fug109Fixture builds the shared shell fixture: a vault-unlocked shell
// whose document carries a regular column, so a narrow window folds it into
// the stripe's tab strip. It returns the screens, the env and both windows.
func fug109Fixture(t *testing.T) (*screens, *kit.Env, *headless.Window, *headless.Window) {
	t.Helper()
	vc := newFakeAppVaultClient()
	vw := vault.NewWorker(func() vault.Client { return vc })
	vw.Start(t.Context())
	t.Cleanup(vw.Stop)

	sc := newFUG65TestScreens(t)
	sc.vaultWorker = vw
	// The default document has only the main slot; a regular column gives
	// the narrow window something to fold into the stripe's tab strip.
	sc.grid.doc = kindsFixture(KindInbox)
	env := newFUG65Env(t, true)

	// The folded site's button exists only once a frame drew with a window
	// narrow enough to fold regular columns into the stripe's tab strip.
	const narrowW, narrowH = 900, 700
	nwin := headlessOpen(t, narrowW, narrowH)
	renderShotFrames(t, nwin, env, sc, image.Pt(narrowW, narrowH), "fug109-fold-warmup")
	if len(sc.grid.tabBtns) == 0 {
		t.Fatal("no column folded into the Right Stripe at 900 dp; the folded site has no button")
	}
	const wideW, wideH = 1200, 800
	return sc, env, headlessOpen(t, wideW, wideH), nwin
}

// fug109ChromeSites lists the six chrome sites with their button boxes, from
// the same arithmetic the chrome layouts use: both stripes center a 28 dp
// button in a 40 dp column, the top bar centers its 24 dp buttons in the
// 32 dp band, the top-right cluster hugs the Right Stripe's own inset, and
// the folded tab hangs at the stripe's bottom edge.
func fug109ChromeSites(t *testing.T, sc *screens, env *kit.Env, win, nwin *headless.Window) []fug109Site {
	t.Helper()
	const wideW, wideH = 1200, 800
	const narrowW, narrowH = 900, 700
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	barH := gtx.Dp(theme.TopBarHeight)
	statusH := gtx.Dp(theme.StatusBarHeight)
	railSide, stripeSide := gtx.Dp(theme.ControlSM), gtx.Dp(theme.ControlSM)
	xsSide := gtx.Dp(theme.ControlXS)
	stripeX := wideW - gtx.Dp(theme.RightStripeWidth) + (gtx.Dp(theme.RightStripeWidth)-stripeSide)/2
	narrowStripeX := narrowW - gtx.Dp(theme.RightStripeWidth) + (gtx.Dp(theme.RightStripeWidth)-stripeSide)/2
	narrowBodyH := narrowH - barH - statusH
	railBtnGap := gtx.Dp(theme.SpaceXS)
	rightPad := (gtx.Dp(theme.RightStripeWidth) - xsSide) / 2
	clusterW := 2*xsSide + railBtnGap // theme + settings, one gap
	bodyH := wideH - barH - statusH
	var foldKey string
	for k := range sc.grid.tabBtns {
		foldKey = k
	}
	return []fug109Site{
		{
			name: "left rail", tag: sc.grid.railBtn(railChat).Tag(),
			box: image.Rect((gtx.Dp(theme.RailWidth)-railSide)/2, barH+railBtnGap, (gtx.Dp(theme.RailWidth)-railSide)/2+railSide, barH+railBtnGap+railSide),
			win: win, size: image.Pt(wideW, wideH),
		},
		{
			name: "stripe companion", tag: sc.grid.stripeBtn(KindAccounts).Tag(),
			// Accounts is the companions' second row, one button slot down.
			box: image.Rect(stripeX, barH+2*railBtnGap+stripeSide, stripeX+stripeSide, barH+2*railBtnGap+2*stripeSide),
			win: win, size: image.Pt(wideW, wideH),
		},
		{
			name: "folded stripe slot", tag: sc.grid.tabBtns[foldKey].Tag(),
			box: image.Rect(narrowStripeX, barH+narrowBodyH-railBtnGap-stripeSide, narrowStripeX+stripeSide, barH+narrowBodyH-railBtnGap),
			win: nwin, size: image.Pt(narrowW, narrowH),
		},
		{
			name: "top bar theme", tag: sc.titleBar.themeBtn.Tag(),
			box: image.Rect(wideW-rightPad-clusterW, (barH-xsSide)/2, wideW-rightPad-clusterW+xsSide, (barH-xsSide)/2+xsSide),
			win: win, size: image.Pt(wideW, wideH),
		},
		{
			name: "top bar settings", tag: sc.titleBar.settingsBtn.Tag(),
			box: image.Rect(wideW-rightPad-xsSide, (barH-xsSide)/2, wideW-rightPad-xsSide+xsSide, (barH-xsSide)/2+xsSide),
			win: win, size: image.Pt(wideW, wideH),
		},
		{
			name: "status bar lock", tag: sc.vaultGate.lockBtn.Tag(),
			box: image.Rect(wideW-gtx.Dp(theme.SpaceMD)-xsSide, barH+bodyH, wideW-gtx.Dp(theme.SpaceMD), barH+bodyH+xsSide),
			win: win, size: image.Pt(wideW, wideH),
		},
	}
}

// TestChromeButtonsShowFocusWithTheHoverWashNotARing walks every chrome site
// and asserts the wash rule of fug109WashShown on its focused render. The
// sites render sequentially on purpose: they share one screens fixture and
// the env's Preview, and every frame snaps (ReducedMotion), so a render
// leaves no residue for the next one.
func TestChromeButtonsShowFocusWithTheHoverWashNotARing(t *testing.T) {
	t.Parallel()
	sc, env, win, nwin := fug109Fixture(t)
	for _, tc := range fug109ChromeSites(t, sc, env, win, nwin) {
		env.Preview = kit.Preview{}
		base := renderShotFrames(t, tc.win, env, sc, tc.size, "fug109-base")
		env.Preview = kit.Preview{Focus: tc.tag}
		focused := renderShotFrames(t, tc.win, env, sc, tc.size, "fug109-focus")
		fug109WashShown(t, base, focused, tc.box, tc.name)
	}
}
