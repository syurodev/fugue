package appwin

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// FUG-40: a companion slot is 300 dp wide at its narrowest. These tests lay
// the approval card and the settings policy card out at that width and check
// that nothing the user must read or press leaves the card.

// testNow is the fixed frame time of these layouts.
func testNow() time.Time { return time.Unix(1_700_000_000, 0) }

const (
	untrustedTail = "UNTRUSTED_TAIL_MARKER_END"
	longTokenSize = 400
)

func slotEnv(t *testing.T, scale float32) *kit.Env {
	t.Helper()
	env := newInboxTestEnv(t)
	env.UIScale = scale
	return env
}

func slotGtx(env *kit.Env, ops *op.Ops, router *input.Router, size image.Point) layout.Context {
	return layout.Context{
		Ops:         ops,
		Metric:      env.Metric(unit.Metric{PxPerDp: 1, PxPerSp: 1}),
		Constraints: layout.Exact(size),
		Source:      router.Source(),
	}
}

// slotCardInnerW is the width the approval card hands its body: the slot, less
// the view's inset and the card's padding on both sides.
func slotCardInnerW(gtx *layout.Context) int {
	return gtx.Dp(theme.SlotMinWidth) - 2*gtx.Dp(theme.SpaceLG) - 2*gtx.Dp(theme.SpaceLG)
}

func TestInboxApprovalCardStaysInsideTheCompanionSlot(t *testing.T) {
	t.Parallel()
	longToken := strings.Repeat("A", longTokenSize) + untrustedTail
	cases := []struct {
		name, detail string
		// minLines is the least number of lines the detail needs at 300 dp.
		minLines func(perLine int) int
	}{
		{"multi-line command", longConfirmDetail(), func(int) int { return strings.Count(longConfirmDetail(), "\n") + 1 }},
		{"one very long unbroken token", longToken, func(perLine int) int { return (len(longToken) + perLine - 1) / perLine }},
	}
	for _, scale := range []float32{1, 1.25} {
		for _, tc := range cases {
			t.Run(tc.name+"/"+scaleName(scale), func(t *testing.T) {
				t.Parallel()
				assertApprovalCardInsideSlot(t, scale, tc.detail, tc.minLines)
			})
		}
	}
}

func scaleName(s float32) string {
	if s == 1 {
		return "100%"
	}
	return "125%"
}

func assertApprovalCardInsideSlot(t *testing.T, scale float32, detail string, minLines func(int) int) {
	t.Helper()
	env := slotEnv(t, scale)
	var runs []image.Rectangle
	env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }
	sc := buildInboxScreensWithDetail(t, nil, detail)
	router := new(input.Router)
	var ops op.Ops
	gtx := slotGtx(env, &ops, router, image.Pt(0, 0))
	slotW := gtx.Dp(theme.SlotMinWidth)
	gtx.Constraints = layout.Exact(image.Pt(slotW, 900))
	inner := slotCardInnerW(&gtx)
	for range 2 {
		runs = runs[:0]
		ops.Reset()
		env.Begin(testNow())
		if dims := sc.inboxView.layout(&gtx, env, sc); dims.Size.X != slotW {
			t.Fatalf("inbox view is %d px wide in a %d px slot", dims.Size.X, slotW)
		}
		env.FrameEnd(&gtx)
		router.Frame(&ops)
	}
	if sc.lastErr != "" {
		t.Fatalf("layout recorded an error: %s", sc.lastErr)
	}

	// Every text run is no wider than the card's inner width, and the
	// detail, the tallest run, is as tall as its wrapped lines need.
	line := kit.MeasureLabel(env, &gtx, theme.TypeCode(), "X")
	tallest := 0
	for _, r := range runs {
		if r.Dx() > inner {
			t.Fatalf("a text run is %d px wide in a card body of %d px", r.Dx(), inner)
		}
		tallest = max(tallest, r.Dy())
	}
	perLine := inner / line.X
	if want := minLines(perLine); tallest/line.Y < want {
		t.Fatalf("full detail laid out in %d lines, want at least %d: it is clipped or overflows", tallest/line.Y, want)
	}

	// Every action button sits inside the card body.
	assertFooterInside(t, env, sc, &gtx, inner)
}

func assertFooterInside(t *testing.T, env *kit.Env, sc *screens, gtx *layout.Context, inner int) {
	t.Helper()
	req := sc.inbox.Items()[0]
	pin := inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
	g := *gtx
	g.Constraints = layout.Constraints{Max: image.Pt(inner, 1<<20)}
	items := confirmFooterItems(&g, env, sc, &req, sc.inboxView.formFor(&req), pin)
	if len(items) != 3 {
		t.Fatalf("confirm footer has %d actions, want Deny, Allow once and Allow for session", len(items))
	}
	gap, rowGap := flowGap(&g)
	rects, size := kit.FlowPlace(inner, gap, rowGap, items)
	body := image.Rect(0, 0, inner, size.Y)
	total := gap * (len(items) - 1)
	for i, r := range rects {
		if !r.In(body) {
			t.Fatalf("action %d is at %v, outside the card body %v", i, r, body)
		}
		total += items[i].Size.X
	}
	wrapped := rects[len(rects)-1].Min.Y > rects[0].Min.Y
	if (total > inner) != wrapped {
		t.Fatalf("actions need %d px of a %d px body but wrapped=%v", total, inner, wrapped)
	}
}

// TestSettingsPendingPolicyCardUsesTheSharedDiffPanel renders the card at the
// slot's narrowest width and finds the diff panel's own pixels inside it:
// the panel the workspace policy dialog draws, at the same place the card
// leaves for it, with every row inside the card.
func TestSettingsPendingPolicyCardUsesTheSharedDiffPanel(t *testing.T) {
	t.Parallel()
	for _, scale := range []float32{1, 1.25} {
		t.Run(scaleName(scale), func(t *testing.T) {
			t.Parallel()
			env := slotEnv(t, scale)
			sc := buildSettingsShotScreens(t, shotSettingsPendingPolicy)
			router := new(input.Router)
			var ops op.Ops
			gtx := slotGtx(env, &ops, router, image.Point{})
			cardW := gtx.Dp(theme.SlotMinWidth) - 2*gtx.Dp(theme.SpaceLG)
			gtx.Constraints = layout.Constraints{Max: image.Pt(cardW, 1<<20)}
			card := renderOps(t, func(g *layout.Context) layout.Dimensions {
				return sc.settingsView.layoutPendingPolicyCard(g, env, sc)
			}, &gtx, env, nil)

			diff := make([]workspace.DiffEntry, 0, len(sc.settings.PendingPolicyDiff))
			for _, d := range sc.settings.PendingPolicyDiff {
				diff = append(diff, workspace.DiffEntry{Key: d.Key, OldValue: d.OldValue, NewValue: d.NewValue})
			}
			pad := gtx.Dp(theme.SpaceLG)
			gtx.Constraints = layout.Constraints{Max: image.Pt(cardW-2*pad, 1<<20)}
			panelBg := env.Th.Chrome().SurfaceBase
			panel := renderOps(t, func(g *layout.Context) layout.Dimensions {
				return layoutDiffPanel(env, g, diff)
			}, &gtx, env, &panelBg)

			at, ok := findImage(card, panel, pad, gtx.Dp(theme.RadiusMD))
			if !ok {
				t.Fatalf("the policy card does not contain the shared diff panel at x=%d", pad)
			}
			if at.X+panel.Bounds().Dx() > card.Bounds().Dx()-pad {
				t.Fatalf("diff panel at %v (%v) leaves the %v card", at, panel.Bounds().Size(), card.Bounds())
			}
		})
	}
}

// renderOps lays fn out, renders it headless and returns the image, on bg
// when given.
func renderOps(t *testing.T, fn func(*layout.Context) layout.Dimensions, gtx *layout.Context, env *kit.Env, bg *color.NRGBA) *image.RGBA {
	t.Helper()
	var probe op.Ops
	g := *gtx
	g.Ops = &probe
	env.Begin(testNow())
	dims := fn(&g)
	env.FrameEnd(&g)
	var ops op.Ops
	g = *gtx
	g.Ops = &ops
	if bg != nil {
		paint.FillShape(&ops, *bg, clip.Rect{Max: dims.Size}.Op())
	}
	env.Begin(testNow())
	fn(&g)
	env.FrameEnd(&g)
	win, err := glwin.Open(dims.Size.X, dims.Size.Y)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	if err := win.Frame(&ops); err != nil {
		t.Fatalf("render: %v", err)
	}
	img := image.NewRGBA(image.Rectangle{Max: dims.Size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	return img
}

// findImage looks for sub inside img at column x, comparing everything but
// the rounded corners (inset px from each edge), and returns where it sits.
// A card draws its body through a recorded macro, which can move
// antialiased text edge pixels; a match tolerates one in twenty, far fewer
// than a layout without the panel's fill would differ by.
func findImage(img, sub *image.RGBA, x, inset int) (image.Point, bool) {
	sw, sh := sub.Bounds().Dx(), sub.Bounds().Dy()
	compared := (sw - 2*inset) * (sh - 2*inset)
	for y := 0; y+sh <= img.Bounds().Dy(); y++ {
		bad := 0
		for dy := inset; dy < sh-inset; dy++ {
			for dx := inset; dx < sw-inset; dx++ {
				if img.RGBAAt(x+dx, y+dy) != sub.RGBAAt(dx, dy) {
					bad++
				}
			}
		}
		if bad*20 <= compared {
			return image.Pt(x, y), true
		}
	}
	return image.Point{}, false
}

// TestDiffPanelShowsEveryPolicyKeyWholeAtSlotWidth: a key the column cannot
// hold is stacked above its values and wrapped, never ellipsized, so the user
// reads the whole key a change touches.
func TestDiffPanelShowsEveryPolicyKeyWholeAtSlotWidth(t *testing.T) {
	t.Parallel()
	keys := []string{
		"policy.approvals.auto_approve_for_environment_tag",
		"policy." + strings.Repeat("k", 120),
	}
	for _, scale := range []float32{1, 1.25} {
		for _, key := range keys {
			t.Run(scaleName(scale)+"/"+strconv.Itoa(len(key)), func(t *testing.T) {
				t.Parallel()
				assertKeyShownWhole(t, slotEnv(t, scale), key)
			})
		}
	}
}

func assertKeyShownWhole(t *testing.T, env *kit.Env, key string) {
	t.Helper()
	var runs []image.Rectangle
	env.RunRecorder = func(r image.Rectangle) { runs = append(runs, r) }
	var ops op.Ops
	gtx := slotGtx(env, &ops, new(input.Router), image.Point{})
	panelW := gtx.Dp(theme.SlotMinWidth) - 4*gtx.Dp(theme.SpaceLG)
	gtx.Constraints = layout.Constraints{Max: image.Pt(panelW, 1<<20)}
	inner := panelW - 2*gtx.Dp(theme.SpaceLG)
	dims := layoutDiffPanel(env, &gtx, []workspace.DiffEntry{{Key: key, OldValue: testApprovalAuto, NewValue: testApprovalFull}})
	if dims.Size.X != panelW {
		t.Fatalf("panel is %d px wide, want %d", dims.Size.X, panelW)
	}
	one := kit.MeasureLabel(env, &gtx, theme.TypeCodeSmall(), "k")
	tallest := 0
	for _, r := range runs {
		if r.Dx() > inner {
			t.Fatalf("a run is %d px wide in an inner width of %d", r.Dx(), inner)
		}
		tallest = max(tallest, r.Dy())
	}
	need := (kit.MeasureLabel(env, &gtx, theme.TypeCodeSmall(), key).X + inner - 1) / inner
	if lines := (tallest + one.Y/2) / one.Y; lines < need {
		t.Fatalf("key %q laid out in %d lines, want at least %d", key, lines, need)
	}
	if dims.Size.Y < tallest+2*gtx.Dp(theme.SpaceLG) {
		t.Fatalf("panel height %d does not hold its key (%d)", dims.Size.Y, tallest)
	}
}
