package kit

import (
	"image"
	"image/color"
	"testing"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

func maxChannelDiff(a, b color.RGBA) int {
	abs := func(x, y uint8) int {
		if x > y {
			return int(x - y)
		}
		return int(y - x)
	}
	return max(abs(a.R, b.R), abs(a.G, b.G), abs(a.B, b.B))
}

func rectCenter(r image.Rectangle) image.Point {
	return image.Pt((r.Min.X+r.Max.X)/2, (r.Min.Y+r.Max.Y)/2)
}

func runDialogShotFrame(t *testing.T, h *harness, d *DialogState, props *DialogProps, backdrop color.NRGBA) DialogResult {
	t.Helper()
	var res DialogResult
	runFrame(t, h, func(gtx *layout.Context) {
		paint.FillShape(gtx.Ops, backdrop, clip.Rect(h.env.windowRect()).Op())
		var err error
		res, err = d.Layout(h.env, gtx, props)
		if err != nil {
			t.Fatalf("lay the dialog out: %v", err)
		}
	})
	return res
}

func capturePartialDialogShot(t *testing.T, h *harness, d *DialogState, props *DialogProps, backdrop color.NRGBA) (float32, *image.RGBA) {
	t.Helper()
	for range 120 {
		runDialogShotFrame(t, h, d, props, backdrop)
		if p := d.open.Progress(); p >= 0.25 && p <= 0.65 {
			return p, h.shot(t, h.env.win.X, h.env.win.Y)
		}
	}
	t.Fatalf("did not observe partial presence in [0.25, 0.65]; last=%v", d.open.Progress())
	return 0, nil
}

func settleDialogShot(t *testing.T, h *harness, d *DialogState, props *DialogProps, backdrop color.NRGBA) (DialogResult, *image.RGBA) {
	t.Helper()
	var res DialogResult
	for range 180 {
		res = runDialogShotFrame(t, h, d, props, backdrop)
		if d.open.Progress() == 1 && !d.scrim.Moving() {
			return res, h.shot(t, h.env.win.X, h.env.win.Y)
		}
	}
	t.Fatalf("dialog did not reach full presence: prog=%v", d.open.Progress())
	return res, nil
}

func assertDialogPhaseFadeAndScale(t *testing.T, phase string, prog float32, mid, full, bg *image.RGBA, fullRes *DialogResult, ring int) {
	t.Helper()
	bodyCenter := rectCenter(fullRes.Boxes.Body)
	// Sample the Primary button's bright surface fill 8 px inside its right edge
	// rather than dead-center, avoiding the dark centered label ink while still
	// remaining inside the scaled button at partial presence.
	primaryInterior := image.Pt(fullRes.Boxes.Primary.Max.X-8, rectCenter(fullRes.Boxes.Primary).Y)
	checks := []struct {
		name      string
		pt        image.Point
		refImg    *image.RGBA
		refPt     image.Point
		minFullD  int
		minOtherD int
		otherDesc string
	}{
		{"body center fade", bodyCenter, bg, bodyCenter, 15, 5, "bg"},
		{"primary footer interior fade", primaryInterior, bg, primaryInterior, 15, 5, "bg"},
		{"body left edge scale", image.Pt(fullRes.Boxes.Body.Min.X+ring+1, bodyCenter.Y), mid, bodyCenter, 40, 25, "scaled interior"},
		{"primary footer right edge scale", image.Pt(fullRes.Boxes.Primary.Max.X-2, primaryInterior.Y), mid, primaryInterior, 40, 25, "scaled interior"},
	}
	for _, tc := range checks {
		midPx := mid.RGBAAt(tc.pt.X, tc.pt.Y)
		fullPx := full.RGBAAt(tc.pt.X, tc.pt.Y)
		if diff := maxChannelDiff(midPx, fullPx); diff < tc.minFullD {
			t.Errorf("%s (prog=%.3f) %s: mid=%v full=%v (delta=%d < %d)",
				phase, prog, tc.name, midPx, fullPx, diff, tc.minFullD)
		}
		otherPx := tc.refImg.RGBAAt(tc.refPt.X, tc.refPt.Y)
		if diff := maxChannelDiff(midPx, otherPx); diff < tc.minOtherD {
			t.Errorf("%s (prog=%.3f) %s too close to %s: mid=%v other=%v (delta=%d < %d)",
				phase, prog, tc.name, tc.otherDesc, midPx, otherPx, diff, tc.minOtherD)
		}
	}
}

// TestDialogBodyAndFooterFadeAndScaleWithChrome proves that a non-inline
// dialog fades and scales its body and footer together with its chrome during
// both opening and closing (§3.7).
func TestDialogBodyAndFooterFadeAndScaleWithChrome(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	cancel, primary := NewButtonState(), NewButtonState()
	backdrop := h.env.Th.Chrome().AppBg
	bodyFill := h.env.Th.Chrome().Fg

	props := &DialogProps{
		Variant:        DialogForm,
		Title:          "New session",
		Description:    "Choose a workspace and model.",
		Cancel:         cancel,
		Primary:        primary,
		PrimaryLabel:   "Start",
		PrimaryVariant: Primary,
		Body: func(gtx *layout.Context, _ *DialogBands) image.Point {
			sz := image.Pt(gtx.Constraints.Max.X, 48)
			paint.FillShape(gtx.Ops, bodyFill, clip.Rect{Max: sz}.Op())
			return sz
		},
	}

	runDialogShotFrame(t, h, d, props, backdrop)
	bgImg := h.shot(t, h.env.win.X, h.env.win.Y)

	d.Open()
	openMidProg, openMidImg := capturePartialDialogShot(t, h, d, props, backdrop)
	fullRes, fullImg := settleDialogShot(t, h, d, props, backdrop)

	d.Close()
	closeMidProg, closeMidImg := capturePartialDialogShot(t, h, d, props, backdrop)

	ring := dialogRing(&h.gtx)
	assertDialogPhaseFadeAndScale(t, "open", openMidProg, openMidImg, fullImg, bgImg, &fullRes, ring)
	assertDialogPhaseFadeAndScale(t, "close", closeMidProg, closeMidImg, fullImg, bgImg, &fullRes, ring)
}

// TestDialogSettledSelectMenuNotDimmed confirms that once the dialog reaches
// prog=1, a Select menu opened from inside the dialog body draws at full
// contrast above the dialog without being dimmed.
func TestDialogSettledSelectMenuNotDimmed(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	d := NewDialogState()
	sel := NewSelectState()
	cancel, primary := NewButtonState(), NewButtonState()
	backdrop := h.env.Th.Chrome().AppBg
	selProps := &SelectProps{
		Label: "Model",
		Value: "model-a",
		Options: []SelectOption{
			{ID: "model-a", Label: "Model Alpha"},
			{ID: "model-b", Label: "Model Beta"},
		},
	}
	props := &DialogProps{
		Variant:      DialogForm,
		Title:        "New session",
		Cancel:       cancel,
		Primary:      primary,
		PrimaryLabel: "Start",
		Body: func(gtx *layout.Context, bands *DialogBands) image.Point {
			dims := sel.Measure(h.env, gtx, selProps)
			if _, err := sel.Layout(h.env, gtx, selProps); err != nil {
				t.Errorf("lay select out: %v", err)
			}
			bands.Focusable(sel.Tag(), 0, dims.Size.Y)
			return dims.Size
		},
	}

	d.Open()
	lastRes, _ := settleDialogShot(t, h, d, props, backdrop)
	sel.menu.Open()
	for range 120 {
		lastRes = runDialogShotFrame(t, h, d, props, backdrop)
		if sel.menu.lv[0].open.Progress() == 1 {
			break
		}
	}
	if !sel.IsOpen() {
		t.Fatal("select menu did not open inside dialog")
	}
	img := h.shot(t, h.env.win.X, h.env.win.Y)
	if sel.menu.lv[0].rect.Empty() {
		t.Fatal("select menu rect is empty")
	}
	menuRect := sel.menu.lv[0].rect.Add(lastRes.Boxes.Body.Min).Add(image.Pt(dialogRing(&h.gtx), 0))
	pt := image.Pt(menuRect.Max.X-16, menuRect.Min.Y+menuRect.Dy()/2)
	got := img.RGBAAt(pt.X, pt.Y)
	want := h.env.Th.Chrome().SurfaceRaised
	if diff := maxChannelDiff(got, color.RGBA{R: want.R, G: want.G, B: want.B, A: 255}); diff > 2 {
		t.Fatalf("settled select menu pixel at %v = %v, want ~%v (diff=%d)", pt, got, want, diff)
	}
}
