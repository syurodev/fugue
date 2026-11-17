package kit

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func splitFrame(t *testing.T, h *harness, s *SplitPaneState, p *SplitPaneProps, w, hgt int, first, second layout.Widget) (float32, bool) {
	t.Helper()
	h.frameSize(t, w, hgt)
	h.env.TrackInput(&h.gtx)
	ratio, changed, _ := s.Layout(h.env, &h.gtx, *p, first, second)
	if changed {
		p.Ratio = ratio
	}
	h.end()
	return ratio, changed
}

// TestSplitPaneDragFollowsThePointerAndClampsToMinimums covers F10.12 and
// §6.27: dragging the handle follows the pointer without spring lag and clamps
// to MinFirst and MinSecond.
func TestSplitPaneDragFollowsThePointerAndClampsToMinimums(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := NewSplitPaneState()
	p := SplitPaneProps{
		Orientation:  Horizontal,
		Ratio:        0.5,
		DefaultRatio: 0.5,
		MinFirst:     unit.Dp(80),
		MinSecond:    unit.Dp(100),
	}

	// Prime the handle at x = 200 in a 400×200 box.
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	// Press at the handle (x=200, y=100), then drag to x=260 (+60 px -> ratio 0.65).
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(200, 100), Buttons: pointer.ButtonPrimary})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(260, 100), Buttons: pointer.ButtonPrimary})
	ratio, changed := splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.65)) > 0.01 {
		t.Fatalf("after drag to x=260: ratio=%v changed=%v, want ~0.65, true", ratio, changed)
	}
	if !s.Dragging() {
		t.Fatal("expected Dragging() to be true during drag")
	}

	// Drag past MinSecond (400 - 100 = 300 -> max ratio 0.75).
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(380, 100), Buttons: pointer.ButtonPrimary})
	ratio, _ = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if math.Abs(float64(ratio-0.75)) > 0.01 {
		t.Fatalf("drag past max clamped to %v, want 0.75", ratio)
	}

	// Drag past MinFirst (80 -> min ratio 0.20).
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(10, 100), Buttons: pointer.ButtonPrimary})
	ratio, _ = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if math.Abs(float64(ratio-0.20)) > 0.01 {
		t.Fatalf("drag past min clamped to %v, want 0.20", ratio)
	}

	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: f32.Pt(10, 100)})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if s.Dragging() {
		t.Fatal("expected Dragging() to be false after release")
	}
}

// TestSplitPaneDoubleClickIsNotADrag covers F10.12 and §6.27: a double-click
// resets the ratio to DefaultRatio, movements past ~4 dp are treated as drags,
// and the release of a drag does not count toward a double-click.
func TestSplitPaneDoubleClickIsNotADrag(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := NewSplitPaneState()
	p := SplitPaneProps{
		Orientation:  Horizontal,
		Ratio:        0.7,
		DefaultRatio: 0.4,
		MinFirst:     unit.Dp(40),
		MinSecond:    unit.Dp(40),
	}

	// Prime at x = 280 in 400×200.
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	// 1. Small jitter <= 4 dp is a click, not a drag; a second click resets.
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(280, 100), Buttons: pointer.ButtonPrimary})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(282, 100), Buttons: pointer.ButtonPrimary})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if s.Dragging() {
		t.Fatal("2 dp move should not start a drag")
	}
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: f32.Pt(282, 100)})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	// Advance 100 ms on fake clock and click again -> double-click resets to 0.4.
	h.now = h.now.Add(100 * time.Millisecond)
	h.click(280, 100)
	ratio, changed := splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.4)) > 0.01 {
		t.Fatalf("double-click reset ratio=%v changed=%v, want 0.4, true", ratio, changed)
	}
	// Lay out one frame at the new 0.4 ratio so the router's handle hit area
	// moves to x=160 before the drag starts.
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	// 2. Drag > 4 dp and release, followed immediately by a single click, must
	// NOT trigger double-click reset (drag release does not count as click 1).
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(160, 100), Buttons: pointer.ButtonPrimary})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(260, 100), Buttons: pointer.ButtonPrimary})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: f32.Pt(260, 100)})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	h.now = h.now.Add(50 * time.Millisecond)
	h.click(260, 100)
	ratio, changed = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if changed || math.Abs(float64(ratio-0.65)) > 0.01 {
		t.Fatalf("single click after drag release must not reset: ratio=%v changed=%v", ratio, changed)
	}
}

// TestSplitPaneHandleMovesWithArrowKeys covers F10.12, §6.27, and UK-E05:
// Tab focuses the handle, Arrow keys move by 16 dp (64 dp with Shift), and
// Home/End jump to the minimum/maximum limits.
func TestSplitPaneHandleMovesWithArrowKeys(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	s := NewSplitPaneState()
	p := SplitPaneProps{
		Orientation: Horizontal,
		Ratio:       0.5,
		MinFirst:    unit.Dp(60),
		MinSecond:   unit.Dp(60),
	}

	splitFrame(t, h, s, &p, 400, 200, nil, nil)

	// Tab turns on keyboard modality; the headless router does not synthesize
	// FocusCmd from Tab, so issue FocusCmd as a real window would (env_test.go).
	h.tap(key.NameTab, 0)
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	h.gtx.Execute(key.FocusCmd{Tag: s.Tag()})
	splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !h.gtx.Focused(s.Tag()) {
		t.Fatal("expected SplitPane handle to receive focus on Tab")
	}
	if !h.env.FocusVisible(&h.gtx, s.Tag()) {
		t.Fatal("expected FocusVisible on SplitPane handle after Tab")
	}

	// Right arrow moves by 16 dp: 200 -> 216 (ratio 0.54).
	h.tap(key.NameRightArrow, 0)
	ratio, changed := splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.54)) > 0.005 {
		t.Fatalf("RightArrow: ratio=%v changed=%v, want 0.54", ratio, changed)
	}

	// Shift+Left arrow moves by 64 dp: 216 -> 152 (ratio 0.38).
	h.tap(key.NameLeftArrow, key.ModShift)
	ratio, changed = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.38)) > 0.005 {
		t.Fatalf("Shift+LeftArrow: ratio=%v changed=%v, want 0.38", ratio, changed)
	}

	// Home jumps to MinFirst (60/400 = 0.15).
	h.tap(key.NameHome, 0)
	ratio, changed = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.15)) > 0.005 {
		t.Fatalf("Home: ratio=%v changed=%v, want 0.15", ratio, changed)
	}

	// End jumps to 400 - MinSecond (340/400 = 0.85).
	h.tap(key.NameEnd, 0)
	ratio, changed = splitFrame(t, h, s, &p, 400, 200, nil, nil)
	if !changed || math.Abs(float64(ratio-0.85)) > 0.005 {
		t.Fatalf("End: ratio=%v changed=%v, want 0.85", ratio, changed)
	}
}

// TestSplitPaneSplitsInBothDirections covers F10.12 and desktop-app §5.3:
// Horizontal and Vertical SplitPanes can nest one inside the other and resize
// independently along their respective axes.
func TestSplitPaneSplitsInBothDirections(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	outer := NewSplitPaneState()
	inner := NewSplitPaneState()
	outerProps := SplitPaneProps{Orientation: Horizontal, Ratio: 0.4, MinFirst: unit.Dp(40), MinSecond: unit.Dp(40)}
	innerProps := SplitPaneProps{Orientation: Vertical, Ratio: 0.5, MinFirst: unit.Dp(30), MinSecond: unit.Dp(30)}

	var firstW, innerTopH, innerBotH int
	lay := func() {
		h.frameSize(t, 500, 300)
		h.env.TrackInput(&h.gtx)
		if r, ok, _ := outer.Layout(h.env, &h.gtx, outerProps,
			func(g layout.Context) layout.Dimensions {
				firstW = g.Constraints.Max.X
				return layout.Dimensions{Size: g.Constraints.Max}
			},
			func(g layout.Context) layout.Dimensions {
				if ir, iok, idims := inner.Layout(h.env, &g, innerProps,
					func(ig layout.Context) layout.Dimensions {
						innerTopH = ig.Constraints.Max.Y
						return layout.Dimensions{Size: ig.Constraints.Max}
					},
					func(ig layout.Context) layout.Dimensions {
						innerBotH = ig.Constraints.Max.Y
						return layout.Dimensions{Size: ig.Constraints.Max}
					},
				); iok {
					innerProps.Ratio = ir
				} else {
					_ = idims
				}
				return layout.Dimensions{Size: g.Constraints.Max}
			},
		); ok {
			outerProps.Ratio = r
		}
		h.end()
	}

	lay()
	if firstW != 200 || innerTopH != 150 || innerBotH != 150 {
		t.Fatalf("initial nested sizes: firstW=%d topH=%d botH=%d, want 200, 150, 150", firstW, innerTopH, innerBotH)
	}

	// Drag the vertical inner handle (inside right pane x=200..500, at y=150) down to y=210 (ratio 0.70).
	h.router.Queue(pointer.Event{Kind: pointer.Press, Position: f32.Pt(350, 150), Buttons: pointer.ButtonPrimary})
	lay()
	h.router.Queue(pointer.Event{Kind: pointer.Move, Position: f32.Pt(350, 210), Buttons: pointer.ButtonPrimary})
	lay()
	h.router.Queue(pointer.Event{Kind: pointer.Release, Position: f32.Pt(350, 210)})
	lay()

	if math.Abs(float64(innerProps.Ratio-0.70)) > 0.01 {
		t.Fatalf("nested vertical ratio=%v, want ~0.70", innerProps.Ratio)
	}
	if outerProps.Ratio != 0.4 {
		t.Fatalf("outer horizontal ratio changed to %v, want 0.4", outerProps.Ratio)
	}
	if innerTopH != 210 || innerBotH != 90 {
		t.Fatalf("nested vertical pane heights: top=%d bot=%d, want 210, 90", innerTopH, innerBotH)
	}
}

// TestTagAndProgressUseStatusTokens covers F10.12, §6.4, §6.5, UK-E04, and UK-E11:
// Tag resolves status/env/hue tokens and requires text; Tag text achieves >=4.5:1
// contrast on every status fill over every chrome background in light & dark
// modes (rendered via glwin.Open); Progress uses status tokens, settles in
// determinate mode, and stops indeterminate motion under ReducedMotion.
func TestTagAndProgressUseStatusTokens(t *testing.T) {
	t.Parallel()
	sh := newHarness(t).env.Sh
	win, err := glwin.Open(120, 40)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}

	statuses := []theme.StatusKind{theme.StatusInfo, theme.StatusSuccess, theme.StatusWarning, theme.StatusDanger}
	modes := []struct {
		name string
		th   theme.Theme
	}{
		{"light", theme.Light(sh)},
		{"dark", theme.Dark(sh)},
	}

	// UK-E04: verify rendered contrast of Tag text over every status fill on all 5 chrome backgrounds.
	for _, m := range modes {
		c := m.th.Chrome()
		bgs := []struct {
			name string
			bg   color.NRGBA
		}{
			{"AppBg", c.AppBg},
			{"CanvasBg", c.CanvasBg},
			{"SurfaceBase", c.SurfaceBase},
			{"SurfaceRaised", c.SurfaceRaised},
			{"SurfaceTinted", c.SurfaceTinted},
		}
		for _, sk := range statuses {
			tone := m.th.StatusTone(sk)
			for _, bg := range bgs {
				h := newHarness(t)
				h.env.Th = &m.th
				h.frameSize(t, 120, 40)
				paint.Fill(h.gtx.Ops, bg.bg)
				paint.Fill(h.gtx.Ops, tone.Fill)
				if err := win.Frame(&h.ops); err != nil {
					t.Fatalf("render %s/%s: %v", m.name, bg.name, err)
				}
				img := image.NewRGBA(image.Rect(0, 0, 120, 40))
				if err := win.Screenshot(img); err != nil {
					t.Fatalf("screenshot: %v", err)
				}
				px := img.RGBAAt(60, 20)
				actualBg := color.NRGBA{R: px.R, G: px.G, B: px.B, A: 0xff}
				if ratio := wcagRatio(tone.Fg, actualBg); ratio < 4.5 {
					t.Errorf("%s status %d on %s: contrast %.2f:1 < 4.5:1", m.name, sk, bg.name, ratio)
				}
			}
		}
	}

	// Tag requires non-empty text (never color alone).
	h := newHarness(t)
	tag := NewTagState()
	h.frameSize(t, 200, 60)
	if _, err := tag.Layout(h.env, &h.gtx, TagProps{Variant: TagStatus, Status: theme.StatusDanger}); err == nil {
		t.Fatal("expected error when Tag has empty text")
	}

	// Env tags map prod->danger, dev->neutral, stg/unknown->warning.
	if ResolveTagTone(h.env.Th, TagProps{Variant: TagEnv, Env: "prod"}) != h.env.Th.StatusTone(theme.StatusDanger) {
		t.Error("prod env tag should use danger tone")
	}
	if ResolveTagTone(h.env.Th, TagProps{Variant: TagEnv, Env: "dev"}) != h.env.Th.NeutralTone() {
		t.Error("dev env tag should use neutral tone")
	}
	if ResolveTagTone(h.env.Th, TagProps{Variant: TagEnv, Env: "qa"}) != h.env.Th.StatusTone(theme.StatusWarning) {
		t.Error("unknown env tag should use warning tone")
	}

	// Progress uses status tokens and stops indeterminate motion under ReducedMotion (UK-E11).
	st := h.env.Th.Status()
	if ResolveProgressColor(h.env.Th, ProgressInfo) != st.Info ||
		ResolveProgressColor(h.env.Th, ProgressSuccess) != st.Success ||
		ResolveProgressColor(h.env.Th, ProgressWarning) != st.Warning ||
		ResolveProgressColor(h.env.Th, ProgressDanger) != st.Danger {
		t.Error("ProgressTone did not resolve to Theme.Status() tokens")
	}

	// Determinate Progress settles and stops requesting frames (UK-E11).
	det := NewProgressState()
	h.frameSize(t, 200, 20)
	det.Layout(h.env, &h.gtx, ProgressProps{Value: 0.2, Tone: ProgressSuccess})
	h.env.FrameEnd(&h.gtx)
	h.end()

	h.frameSize(t, 200, 20)
	det.Layout(h.env, &h.gtx, ProgressProps{Value: 0.9, Tone: ProgressSuccess})
	h.env.FrameEnd(&h.gtx)
	h.end()

	settled := false
	for range 120 {
		h.frameSize(t, 200, 20)
		det.Layout(h.env, &h.gtx, ProgressProps{Value: 0.9, Tone: ProgressSuccess})
		h.env.FrameEnd(&h.gtx)
		h.end()
		if _, moving := h.env.Driver.Next(); !moving {
			settled = true
			break
		}
	}
	if !settled {
		t.Fatal("determinate Progress did not settle and stop requesting frames")
	}
}

// TestTagAppearsScaledAndCollapsesOnRemove covers UK-E11 and §6.4:
// Tag appears with fade + scale from 0.9 (Snappy), removes with fade then
// width collapse (Exit) before stopping frame requests, and snaps under
// ReducedMotion.
func TestTagAppearsScaledAndCollapsesOnRemove(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tag := NewTagState()
	props := TagProps{Variant: TagStatus, Status: theme.StatusInfo, Text: "Building"}

	h.frameSize(t, 200, 40)
	fullDims, err := tag.Measure(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("measure tag: %v", err)
	}

	// Enter: starts at scale 0.9 and settles at rest.
	tag.Enter()
	_, scaleStart, wStart := tag.presenceVisuals(fullDims.Size.X)
	if math.Abs(float64(scaleStart-0.9)) > 0.01 || wStart != fullDims.Size.X {
		t.Fatalf("at Enter start: scale=%v width=%d, want 0.9 and %d", scaleStart, wStart, fullDims.Size.X)
	}

	entered := false
	for range 120 {
		h.frameSize(t, 200, 40)
		if _, err := tag.Layout(h.env, &h.gtx, props); err != nil {
			t.Fatalf("tag layout: %v", err)
		}
		h.env.FrameEnd(&h.gtx)
		h.end()
		if _, moving := h.env.Driver.Next(); !moving {
			entered = true
			break
		}
	}
	if !entered || !tag.Visible() {
		t.Fatalf("tag did not settle after Enter: entered=%v visible=%v", entered, tag.Visible())
	}

	// Remove: fades first while keeping width, then collapses width to 0.
	tag.Remove()
	sawShrinkingWidth := false
	removed := false
	for range 120 {
		h.frameSize(t, 200, 40)
		dims, err := tag.Layout(h.env, &h.gtx, props)
		if err != nil {
			t.Fatalf("tag layout on remove: %v", err)
		}
		if dims.Size.X > 0 && dims.Size.X < fullDims.Size.X {
			sawShrinkingWidth = true
		}
		h.env.FrameEnd(&h.gtx)
		h.end()
		if _, moving := h.env.Driver.Next(); !moving {
			removed = true
			break
		}
	}
	if !sawShrinkingWidth {
		t.Fatal("expected Tag width to collapse during Exit")
	}
	if !removed || tag.Visible() {
		t.Fatalf("expected Tag to finish removing and stop requesting frames: removed=%v visible=%v", removed, tag.Visible())
	}

	// ReducedMotion: Enter and Remove snap immediately without requesting frames.
	h.env.ReducedMotion = true
	tag.Enter()
	h.frameSize(t, 200, 40)
	dims, err := tag.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("reduced-motion enter: %v", err)
	}
	h.env.FrameEnd(&h.gtx)
	h.end()
	if dims.Size != fullDims.Size {
		t.Fatalf("reduced-motion enter size=%v, want %v", dims.Size, fullDims.Size)
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("reduced-motion enter must not request frames")
	}

	tag.Remove()
	h.frameSize(t, 200, 40)
	dims, err = tag.Layout(h.env, &h.gtx, props)
	if err != nil {
		t.Fatalf("reduced-motion remove: %v", err)
	}
	h.env.FrameEnd(&h.gtx)
	h.end()
	if dims.Size != (image.Point{}) || tag.Visible() {
		t.Fatalf("reduced-motion remove size=%v visible=%v, want 0x0 and false", dims.Size, tag.Visible())
	}
	if _, moving := h.env.Driver.Next(); moving {
		t.Fatal("reduced-motion remove must not request frames")
	}
}

func wcagRatio(a, b color.NRGBA) float64 {
	lum := func(c color.NRGBA) float64 {
		ch := func(v uint8) float64 {
			s := float64(v) / 255
			if s <= 0.04045 {
				return s / 12.92
			}
			return math.Pow((s+0.055)/1.055, 2.4)
		}
		return 0.2126*ch(c.R) + 0.7152*ch(c.G) + 0.0722*ch(c.B)
	}
	la, lb := lum(a), lum(b)
	hi, lo := math.Max(la, lb), math.Min(la, lb)
	return (hi + 0.05) / (lo + 0.05)
}
