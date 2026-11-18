package main

import (
	"fmt"
	"image"
	"slices"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"go.uber.org/goleak"

	"github.com/syurodev/fugue/ui/internal/theme"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// newTestGallery builds the gallery over a real env in one mode at one UI
// scale.
func newTestGallery(t *testing.T, light bool, ui float32) *gallery {
	t.Helper()
	env, err := newEnv(light)
	if err != nil {
		t.Fatalf("build the env: %v", err)
	}
	env.UIScale = ui
	return newGallery(env, light)
}

// layFrame lays one frame of page cur out at a pixel scale and the env's
// UI scale in the fixed gallery window, the way a window would, and returns
// the window size and the frame's metric for the bounds checks.
func layFrame(t *testing.T, g *gallery, cur int, px float32) (gtx layout.Context, size image.Point) {
	t.Helper()
	var router input.Router
	var ops op.Ops
	size = windowSize(px)
	g.cur = cur
	gtx = frameContext(&ops, g.env, size, px)
	gtx.Source = router.Source()
	gtx.Now = time.Unix(1_700_000_000, 0)
	if err := g.frame(&gtx); err != nil {
		t.Fatalf("lay %s out: %v", pageNames()[cur], err)
	}
	router.Frame(&ops)
	return gtx, size
}

// expectedCells is the matrix UK-E03 demands for one page: every variant
// × size × state its page presents.
func expectedCells(cur int) map[cell]bool {
	want := map[cell]bool{}
	builders := map[string]func(map[cell]bool){
		"Button":        buttonCells,
		"IconButton":    iconButtonCells,
		"TextField":     textFieldCells,
		"TextArea":      textAreaCells,
		"Menu":          menuCells,
		"Kbd":           kbdCells,
		"Combobox":      comboboxCells,
		"Select":        selectCells,
		"Tooltip":       tooltipCells,
		"ListRow":       listRowCells,
		"SectionHeader": sectionHeaderCells,
		"ScrollArea":    scrollAreaCells,
		"Tag":           tagCells,
		"Progress":      progressAndDividerCells,
		"SplitPane":     splitPaneCells,
		"Popover":       popoverCells,
		"Dialog":        dialogCells,
		"Toast":         toastCells,
	}
	if fn := builders[pageNames()[cur]]; fn != nil {
		fn(want)
	}
	return want
}

// buttonCells are the Button cells: every variant × size × state.
func buttonCells(want map[cell]bool) {
	for vi := range numVariants {
		for si := range numSizes {
			for _, st := range buttonStates() {
				want[cell{compButton, vi, si, st}] = true
			}
		}
	}
}

// iconButtonCells are the IconButton cells: every variant × state.
func iconButtonCells(want map[cell]bool) {
	for vi := range numIconVariants {
		for _, st := range iconStates() {
			want[cell{compIconButton, vi, 0, st}] = true
		}
	}
}

// textFieldCells are the TextField cells: every variant × state.
func textFieldCells(want map[cell]bool) {
	for vi := range numFieldVariants {
		states := fieldStates()
		if vi == 2 {
			states = secretStates()
		}
		for _, st := range states {
			want[cell{compTextField, vi, 0, st}] = true
		}
	}
}

// textAreaCells are the TextArea cells.
func textAreaCells(want map[cell]bool) {
	for _, st := range fieldStates() {
		want[cell{compTextArea, 0, 0, st}] = true
	}
}

// menuCells are the Menu cells: the two specimen variants in every state,
// and the live triggers.
func menuCells(want map[cell]bool) {
	for vi := range numMenuSpecimens {
		for _, st := range menuStates() {
			want[cell{compMenu, vi, 0, st}] = true
		}
	}
	for _, st := range liveStates() {
		want[cell{compMenu, numMenuSpecimens, 0, st}] = true
	}
}

// kbdCells are the Kbd cells: every shortcut of every group in both
// notations.
func kbdCells(want map[cell]bool) {
	for pi := range kbdPlatforms() {
		for gi, g := range kbdGroups() {
			for _, s := range g.keys {
				want[kbdCellKey(pi, gi, s)] = true
			}
		}
	}
}

func comboboxCells(want map[cell]bool) {
	for _, st := range comboboxFieldStates() {
		want[cell{compCombobox, 0, 0, st}] = true
	}
	for _, st := range comboboxListStates() {
		want[cell{compCombobox, 1, 0, st}] = true
	}
	want[cell{compCombobox, 2, 0, cbLive}] = true
}

func selectCells(want map[cell]bool) {
	for vi := range selectVariantNames() {
		for _, st := range selectStates() {
			want[cell{compSelect, vi, 0, st}] = true
		}
	}
	want[cell{compSelect, 2, 0, selOpen}] = true
	want[cell{compSelect, 3, 0, selLive}] = true
}

func tooltipCells(want map[cell]bool) {
	for _, st := range []string{tipPlain, tipKbd, tipFlipped} {
		want[cell{compTooltip, 0, 0, st}] = true
	}
	for _, st := range []string{tipLiveA, tipLiveB} {
		want[cell{compTooltip, 1, 0, st}] = true
	}
}

func listRowCells(want map[cell]bool) {
	for _, st := range listRowStates() {
		want[cell{compListRow, 0, 0, st}] = true
	}
	want[cell{compListRow, 1, 0, lrLive}] = true
}

func sectionHeaderCells(want map[cell]bool) {
	for _, st := range sectionHeaderStates() {
		want[cell{compSectionHeader, 0, 0, st}] = true
	}
	want[cell{compSectionHeader, 1, 0, shLive}] = true
}

func scrollAreaCells(want map[cell]bool) {
	want[cell{compScrollArea, 0, 0, stRest}] = true
	want[cell{compScrollArea, 0, 0, stHover}] = true
	want[cell{compScrollArea, 1, 0, stRest}] = true
	want[cell{compScrollArea, 1, 0, stHover}] = true
	want[cell{compScrollArea, 2, 0, stRest}] = true
}

func tagCells(want map[cell]bool) {
	specs := tagSpecimens()
	groupIdx := map[string]int{"neutral": 0, "status": 1, "hue": 2, "env": 3}
	for i, sp := range specs {
		want[cell{compTag, groupIdx[sp.group], i, sp.props.Text}] = true
	}
}

func progressAndDividerCells(want map[cell]bool) {
	for i := range progressSpecs() {
		want[cell{compProgress, i, 0, "determinate"}] = true
	}
	want[cell{compDivider, 0, 0, "horizontal"}] = true
	want[cell{compDivider, 1, 0, "vertical"}] = true
}

func splitPaneCells(want map[cell]bool) {
	for oi := range 2 {
		for _, st := range splitStateNames() {
			want[cell{compSplitPane, oi, 0, st}] = true
		}
	}
}

// popoverCells are the Popover cells: the three specimens and the live
// trigger.
func popoverCells(want map[cell]bool) {
	for _, st := range popoverStates() {
		if st == povLive {
			want[cell{compPopover, 1, 0, st}] = true
			continue
		}
		want[cell{compPopover, 0, 0, st}] = true
	}
}

// dialogCells are the Dialog cells: the six specimens and the live trigger.
func dialogCells(want map[cell]bool) {
	for _, st := range []string{dlgForm, dlgFormFocus, dlgFormBusy, dlgFormScrim, dlgConfirm, dlgSafeFocus} {
		want[cell{compDialog, 0, 0, st}] = true
	}
	want[cell{compDialog, 1, 0, dlgLive}] = true
}

// toastCells are the Toast cells: the one-toast specimens, the corner
// three-stack, and the live push triggers.
func toastCells(want map[cell]bool) {
	for _, st := range []string{tstInfo, tstAction, tstWarning, tstMerged, tstPaused, tstStack} {
		want[cell{compToast, 0, 0, st}] = true
	}
	for _, st := range toastPushStates() {
		want[cell{compToast, 1, 0, st}] = true
	}
}

// TestGalleryPagesCoverEveryVariantAndState covers UK-E03: every page
// presents its full variant × size × state matrix — and nothing else — in
// light and in dark.
func TestGalleryPagesCoverEveryVariantAndState(t *testing.T) {
	t.Parallel()
	for _, mode := range []struct {
		name  string
		light bool
	}{{"light", true}, {"dark", false}} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()
			for i := range pageNames() {
				g := newTestGallery(t, mode.light, 1)
				layFrame(t, g, i, 1)
				for _, msg := range diffCells(expectedCells(i), g.pgs.cells) {
					t.Errorf("page %s: %s", pageNames()[i], msg)
				}
			}
		})
	}
}

// diffCells lists the cells missing from got and the ones it has beyond want.
func diffCells(want, got map[cell]bool) []string {
	var out []string
	for c := range want {
		if !got[c] {
			out = append(out, fmt.Sprintf("missing cell %+v", c))
		}
	}
	for c := range got {
		if !want[c] {
			out = append(out, fmt.Sprintf("unexpected cell %+v", c))
		}
	}
	return out
}

// checkPageBounds asserts UK-E09 for one laid-out frame in the fixed
// window: everything inside the window, everything of the page inside its
// column — no sideways overflow — every hit area at least size.hit.min, no
// overlap between text runs, between controls (their focus rings included:
// a ring needs its room) or between a control and a page text run; and
// every run a control drew — label, placeholder, helper, error — sits
// inside its control's box, so a run drawn wider than the slot it was
// given, clipped on the way, cannot pass just because the clip hid it.
func checkPageBounds(t *testing.T, g *gallery, gtx *layout.Context, size image.Point) {
	t.Helper()
	win := image.Rectangle{Max: size}
	column := image.Rect(g.pgs.origin.X, 0, g.pgs.origin.X+pageW(gtx), size.Y)
	ring := gtx.Dp(theme.FocusOffset) + gtx.Dp(theme.FocusStroke)
	hit := gtx.Dp(theme.HitMin)

	all := make([]image.Rectangle, 0, len(g.pgs.runs)+len(g.pgs.ctl))
	for _, r := range g.pgs.runs {
		checkInside(t, "text run", r, r, win, column, g.pgs.origin.X)
		all = append(all, r)
	}
	for _, r := range g.pgs.ctl {
		if r.Dx() < hit || r.Dy() < hit {
			t.Fatalf("control at %v is %v×%v px, below the %v×%v hit minimum", r.Min, r.Dx(), r.Dy(), hit, hit)
		}
		checkInside(t, "control", r, r.Inset(-ring), win, column, g.pgs.origin.X)
		all = append(all, r.Inset(-ring))
	}
	checkRunsInsideControls(t, g.pgs.ctlRuns, g.pgs.ctl)
	for a := range all {
		for b := a + 1; b < len(all); b++ {
			if all[a].Overlaps(all[b]) {
				t.Fatalf("items overlap: %v and %v", all[a], all[b])
			}
		}
	}
}

// checkRunsInsideControls asserts every run a control drew sits inside one
// of the frame's control boxes: a run wider than the slot its control was
// given — clipped on the way — cannot pass just because the clip hid it
// (UK-E09).
func checkRunsInsideControls(t *testing.T, runs, ctls []image.Rectangle) {
	t.Helper()
	for _, r := range runs {
		if !slices.ContainsFunc(ctls, r.In) {
			t.Fatalf("a control's text run %v sits outside every control box, so the control drew it wider than its slot or clipped it", r)
		}
	}
}

// checkInside asserts one item — its drawn extent, with the focus ring for
// a control — stays in the window and, when it belongs to the page column
// rather than the nav, in the column.
func checkInside(t *testing.T, what string, item, extent, win, column image.Rectangle, columnX int) {
	t.Helper()
	if !extent.In(win) {
		t.Fatalf("%s %v with its extent %v is outside the window %v", what, item, extent, win)
	}
	if item.Min.X >= columnX && !item.In(column) {
		t.Fatalf("%s %v runs past the page column %v", what, item, column)
	}
}

// TestEveryPageLaysOutAtEveryScale covers UK-E09: in the one fixed window —
// the same dp size at every UI scale, so zooming really shrinks the room —
// at 90%, 100% and 125% and both pixel scales, nothing runs past the page
// column or the window, no control or text overlaps another, and every hit
// area keeps size.hit.min; blocks wrap, so a zoomed page is taller.
func TestEveryPageLaysOutAtEveryScale(t *testing.T) {
	t.Parallel()
	for _, px := range []float32{1, 1.25} {
		for i := range pageNames() {
			t.Run(fmt.Sprintf("%s-px%v", pageNames()[i], px), func(t *testing.T) {
				t.Parallel()
				heights := make([]int, 0, 3)
				for _, ui := range []float32{0.9, 1, 1.25} {
					heights = append(heights, layoutAtScale(t, i, px, ui))
				}
				if heights[2] <= heights[0] {
					t.Errorf("page heights at 90%%/100%%/125%% = %v: a zoomed page must take more room", heights)
				}
			})
		}
	}
}

// layoutAtScale lays page i out at one scale pair, runs the bounds checks
// and returns the page's bottom edge.
func layoutAtScale(t *testing.T, i int, px, ui float32) int {
	t.Helper()
	g := newTestGallery(t, false, ui)
	gtx, size := layFrame(t, g, i, px)
	if size != windowSize(px) {
		t.Fatalf("the window is %v at UI scale %v, want the fixed %v", size, ui, windowSize(px))
	}
	if got, want := g.pgs.ctl[0].Dy(), gtx.Dp(theme.ControlMD); got != want {
		t.Fatalf("UI scale %v: the nav button is %v px tall, want %v px of a zoomed control.md", ui, got, want)
	}
	checkPageBounds(t, g, &gtx, size)
	return bottom(g)
}

// bottom is the lowest pixel the frame's recorded items reach.
func bottom(g *gallery) int {
	y := 0
	for _, r := range append(append([]image.Rectangle{}, g.pgs.runs...), g.pgs.ctl...) {
		y = max(y, r.Max.Y)
	}
	return y
}

// TestSlowMotionScalesEveryFrameStep covers the slow-motion mode of
// ui-kit §8: with S pressed the env's time scale is a quarter, so every
// spring and the spinner advance a quarter as far per frame.
func TestSlowMotionScalesEveryFrameStep(t *testing.T) {
	t.Parallel()
	g := newTestGallery(t, false, 1)
	g.setSlow(true)
	const step = 16 * time.Millisecond
	now := time.Unix(1_700_000_000, 0)
	g.env.Begin(now)
	g.env.Begin(now.Add(step))
	if got, want := g.env.DT(), step/4; got != want {
		t.Fatalf("slow-motion frame step %v, want %v", got, want)
	}
	g.setSlow(false)
	g.env.Begin(now.Add(2 * step))
	if got := g.env.DT(); got != step {
		t.Fatalf("normal frame step %v, want %v", got, step)
	}
}
