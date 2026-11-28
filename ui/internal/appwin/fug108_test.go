package appwin

import (
	"image"
	"testing"

	"gioui.org/gpu/headless"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

// FUG-108: the shell's bars are sized for the xs controls they hold. The
// status bar is HitMin so the Lock control fits its band instead of
// overflowing it, and the slot header is TopBarHeight so an xs control keeps
// ring room above and below inside the island clip. The chrome focus rule
// itself moved to the hover wash in FUG-109; the slot header close button is
// island content, not chrome, and keeps its ring.

// fug108Ring is the px a §3.4 focus ring reaches outside a control.
func fug108Ring(gtx *layout.Context) int {
	return gtx.Dp(theme.FocusStroke) + gtx.Dp(theme.FocusOffset)
}

// fug108BandDiff counts the changed pixels of base→focused on one row of r.
func fug108BandDiff(base, focused *image.RGBA, y, minX, maxX int) int {
	n := 0
	for x := minX; x < maxX; x++ {
		if base.RGBAAt(x, y) != focused.RGBAAt(x, y) {
			n++
		}
	}
	return n
}

// fug108BandInked asserts every row of [y0,y1) holds ring ink between minX and
// maxX, so a row the island or the bar clip cuts fails instead of passing on
// the rows that survived.
func fug108BandInked(t *testing.T, base, focused *image.RGBA, y0, y1, minX, maxX int, what string) {
	t.Helper()
	for y := y0; y < y1; y++ {
		if fug108BandDiff(base, focused, y, minX, maxX) == 0 {
			t.Fatalf("%s: row %d has no focus ring ink in x[%d,%d)", what, y, minX, maxX)
		}
	}
}

// fug108BandClean asserts no row of [y0,y1) holds any changed pixel between
// minX and maxX.
func fug108BandClean(t *testing.T, base, focused *image.RGBA, y0, y1, minX, maxX int, what string) {
	t.Helper()
	for y := y0; y < y1; y++ {
		if n := fug108BandDiff(base, focused, y, minX, maxX); n != 0 {
			t.Fatalf("%s: row %d has %d unexpected changed pixels in x[%d,%d)", what, y, n, minX, maxX)
		}
	}
}

func TestStatusBarCentresTheXSLockControlWithItsFocusWash(t *testing.T) {
	t.Parallel()
	vc := newFakeAppVaultClient()
	vw := vault.NewWorker(func() vault.Client { return vc })
	vw.Start(t.Context())
	t.Cleanup(vw.Stop)

	sc := newFUG65TestScreens(t)
	sc.vaultWorker = vw
	env := newFUG65Env(t, true)

	const w, hgt = 1000, 700
	win := headlessOpen(t, w, hgt)
	base := renderShotFrames(t, win, env, sc, image.Pt(w, hgt), "fug108-status-base")
	env.Preview = kit.Preview{Focus: sc.vaultGate.lockBtn.Tag()}
	focused := renderShotFrames(t, win, env, sc, image.Pt(w, hgt), "fug108-status-focus")

	// Geometry: the bar is the control's band, so centering leaves no
	// overflow — the old 22 dp bar put 1 dp of the 24 dp control past each
	// edge, the bottom one past the window itself.
	var ops op.Ops
	gtx := layout.Context{Ops: &ops, Metric: env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1})}
	statusH := gtx.Dp(theme.StatusBarHeight)
	lockProps := kit.IconButtonProps{Icon: kit.IconLock, Label: labelLockVault, Variant: kit.IconButtonGhost, Size: kit.SizeXS, FocusWash: true}
	side := sc.vaultGate.lockBtn.Measure(env, &gtx, &lockProps).Size.X
	if statusH != side {
		t.Fatalf("status bar %d dp does not equal the xs Lock control %d dp; the control overflows the band", statusH, side)
	}
	if pad := (statusH - side) / 2; 2*pad != statusH-side {
		t.Fatalf("the Lock control is not centred with even padding: %d/%d", pad, statusH-side)
	}

	// The chrome focus rule (§3.4, FUG-109): the focused Lock control shows
	// the hover wash — hundreds of pixels past the noise floor inside the
	// box — and draws no ring ink outside it, at the window's bottom edge.
	lockX := w - gtx.Dp(theme.SpaceMD) - side // no connection dot: the lock is flush right
	barTop := hgt - statusH
	box := image.Rect(lockX, barTop+(statusH-side)/2, lockX+side, barTop+(statusH-side)/2+side)
	fug109WashShown(t, base, focused, box, "status bar Lock")
}

func TestSlotHeaderKeepsTheXSCloseButtonsRingInsideTheIsland(t *testing.T) {
	t.Parallel()
	doc := closeDoc(KindInbox)
	at, ok := findCell(doc, SlotRef{Kind: KindInbox})
	if !ok {
		t.Fatal("the scene has no regular inbox slot")
	}
	s := newCloseScene(t, doc)
	s.h.env.ReducedMotion = true

	var head image.Rectangle
	var side int
	s.frame(t, func(gtx *layout.Context) {
		rect := s.sc.grid.rects[at.key()]
		head = image.Rect(rect.Min.X, rect.Min.Y, rect.Max.X, rect.Min.Y+min(gtx.Dp(theme.SlotHeaderHeight), rect.Dy()))
		props := kit.IconButtonProps{Icon: kit.IconClose, Label: closeLabel, Variant: kit.IconButtonGhost, Size: kit.SizeXS}
		side = s.sc.grid.closeBtns[at.key()].Measure(s.h.env, gtx, &props).Size.X
	})

	// Geometry: the header leaves exactly one ring of room above and below
	// the control, so the island clip cuts none of it. The old 30 dp header
	// left 3 dp and lost the outer row on both edges.
	ring := fug108Ring(&s.h.gtx)
	lockTop := head.Min.Y + (head.Dy()-side)/2
	if head.Dy() != side+2*ring {
		t.Fatalf("slot header %d dp does not equal the xs control %d dp plus a ring above and below (%d); the ring clips at the island", head.Dy(), side, side+2*ring)
	}
	if lockTop-ring < head.Min.Y || lockTop+side+ring > head.Max.Y {
		t.Fatalf("the close button ring [%d,%d] escapes the header band [%d,%d]", lockTop-ring, lockTop+side+ring, head.Min.Y, head.Max.Y)
	}

	cx := head.Max.X - s.h.gtx.Dp(theme.SpaceXS) - side/2
	win := headlessOpen(t, closeAreaW, closeAreaH)
	base := closeSceneShot(t, s, win)
	s.h.env.Preview = kit.Preview{Focus: s.sc.grid.closeBtns[at.key()].Tag()}
	s.frame(t, nil)
	focused := closeSceneShot(t, s, win)

	// The ring's top and bottom segments sit on the header band's own edge
	// rows, inside the island — the rows a 30 dp header lost to the clip.
	fug108BandInked(t, base, focused, lockTop-ring, lockTop-2, cx-6, cx+6, "ring above the close button")
	fug108BandInked(t, base, focused, lockTop+side+2, lockTop+side+ring, cx-6, cx+6, "ring below the close button")
	// And nothing of the ring escapes the island above it.
	fug108BandClean(t, base, focused, head.Min.Y-ring, head.Min.Y, cx-8, cx+8, "island top edge")
}

func headlessOpen(t *testing.T, w, hgt int) *headless.Window {
	t.Helper()
	win, err := glwin.Open(w, hgt)
	if err != nil {
		t.Fatalf("open a headless window: %v", err)
	}
	return win
}

func closeSceneShot(t *testing.T, s *closeScene, win *headless.Window) *image.RGBA {
	t.Helper()
	if err := win.Frame(&s.h.ops); err != nil {
		t.Fatalf("render the grid frame: %v", err)
	}
	img := image.NewRGBA(image.Rect(0, 0, closeAreaW, closeAreaH))
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("read the grid frame back: %v", err)
	}
	return img
}
