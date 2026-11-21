package mdview

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type blockHarness struct {
	t      *testing.T
	router input.Router
	ops    op.Ops
	th     theme.Theme
	env    *kit.Env
	st     *CodeBlockState
	props  CodeBlockProps
	width  int
	now    time.Time
}

func newBlockHarness(t *testing.T, width int, props *CodeBlockProps) *blockHarness {
	t.Helper()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	now := time.Unix(1_700_000_000, 0)
	return &blockHarness{
		t: t, th: th, env: &kit.Env{Th: &th, Sh: shaper},
		st: NewCodeBlockState(), props: *props, width: width, now: now,
	}
}

func (h *blockHarness) frame() (layout.Dimensions, CodeBlockResult) {
	h.ops.Reset()
	h.env.Begin(h.now)
	gtx := &layout.Context{
		Ops:         &h.ops,
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(h.width, 1_000_000)},
		Source:      h.router.Source(),
		Now:         h.now,
	}
	h.env.TrackInput(gtx)
	dims, res := LayoutCodeBlock(h.env, gtx, h.st, &h.props)
	h.env.FrameEnd(gtx)
	h.router.Frame(&h.ops)
	return dims, res
}

func (h *blockHarness) triggerButton(tag event.Tag) CodeBlockResult {
	h.t.Helper()
	h.router.Source().Execute(key.FocusCmd{Tag: tag})
	h.frame()
	h.router.Queue(
		key.Event{Name: key.NameReturn, State: key.Press},
		key.Event{Name: key.NameReturn, State: key.Release},
	)
	_, r1 := h.frame()
	_, r2 := h.frame()
	return CodeBlockResult{
		Copied:      r1.Copied || r2.Copied,
		WrapToggled: r1.WrapToggled || r2.WrapToggled,
		RequestFull: r1.RequestFull || r2.RequestFull,
	}
}

// TestCodeBlockCopyExcludesLineNumbersAndDiffMarkers verifies v0.9 Copy on
// code blocks and diffs: line numbers and +/-/space diff markers are stripped,
// and the button flashes "Copied" in place.
func TestCodeBlockCopyExcludesLineNumbersAndDiffMarkers(t *testing.T) {
	t.Parallel()
	assertCopyCodeExcludesNumbers(t)
	assertCopyDiffExcludesMarkers(t)
}

func assertCopyCodeExcludesNumbers(t *testing.T) {
	t.Helper()
	h := newBlockHarness(t, 640, &CodeBlockProps{
		Title:           "Output",
		IsOutputCard:    true,
		ShowLineNumbers: true,
		Format:          "go",
		Raw:             "alpha()\nbeta()\ngamma()\n",
	})
	h.frame()
	gtx := &layout.Context{Ops: &h.ops, Source: h.router.Source()}
	if h.st.ActionsVisible(h.env, gtx) {
		t.Fatal("actions must stay hidden when block is neither hovered nor focused")
	}
	h.router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(20, 20)})
	h.frame()
	if !h.st.Hovered() {
		t.Fatal("pointer enter did not mark code block hovered")
	}
	res := h.triggerButton(h.st.copyBtn.Tag())
	if !res.Copied {
		t.Fatal("clicking Copy did not set CodeBlockResult.Copied")
	}
	mime, data, ok := h.router.WriteClipboard()
	if !ok || mime != clipboardMIME {
		t.Fatalf("clipboard write = (%q, %v), want %q", mime, ok, clipboardMIME)
	}
	want := "alpha()\nbeta()\ngamma()\n"
	if string(data) != want {
		t.Fatalf("copied code = %q, want %q (line numbers must be excluded)", string(data), want)
	}
	if h.st.CopiedUntil.IsZero() {
		t.Fatal("Copied confirmation timer was not set")
	}
}

const testFormatDiff = "diff"

func assertCopyDiffExcludesMarkers(t *testing.T) {
	t.Helper()
	diff := "diff --git a/a.go b/a.go\nindex 111..222 100644\n--- a/a.go\n+++ b/a.go\n@@ -10,3 +10,3 @@\n ctx()\n-old()\n+new()\n"
	h := newBlockHarness(t, 640, &CodeBlockProps{
		Format:          testFormatDiff,
		IsDiff:          true,
		ShowLineNumbers: true,
		Raw:             diff,
	})
	h.router.Source().Execute(key.FocusCmd{Tag: h.st.FocusTag()})
	h.frame()
	h.triggerButton(h.st.copyBtn.Tag())
	mime, data, ok := h.router.WriteClipboard()
	if !ok || mime != clipboardMIME {
		t.Fatalf("diff clipboard write = (%q, %v)", mime, ok)
	}
	want := "ctx()\nold()\nnew()"
	if string(data) != want {
		t.Fatalf("copied diff = %q, want %q (markers and headers must be excluded)", string(data), want)
	}
}

// TestDiffLinesCarryAMarkerColumn verifies that diff blocks parse and render a
// separate +/- marker column alongside row tint fills rather than tint alone.
func TestDiffLinesCarryAMarkerColumn(t *testing.T) {
	t.Parallel()
	diff := "@@ -4,3 +4,3 @@\n keep := 1\n-drop := 2\n+keep := 3\n"
	lines := ParseDiff(diff)
	if len(lines) != 4 {
		t.Fatalf("ParseDiff returned %d lines, want 4: %+v", len(lines), lines)
	}
	if lines[0].Kind != DiffMeta || lines[1].Kind != DiffContext || lines[2].Kind != DiffDel || lines[3].Kind != DiffAdd {
		t.Fatalf("diff kinds = %+v, want meta/context/del/add", lines)
	}
	if lines[2].Marker != "−" || lines[3].Marker != "+" {
		t.Fatalf("markers = %q / %q, want − / +", lines[2].Marker, lines[3].Marker)
	}
	if lines[2].LineNo != 5 || lines[3].LineNo != 5 {
		t.Fatalf("line numbers = %d / %d, want 5 / 5", lines[2].LineNo, lines[3].LineNo)
	}
	assertDiffPixelsHaveTintsAndMarkerInk(t, diff)
}

func assertDiffPixelsHaveTintsAndMarkerInk(t *testing.T, diff string) {
	t.Helper()
	const winW, winH = 520, 180
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	h := newBlockHarness(t, winW, &CodeBlockProps{
		Format:          testFormatDiff,
		IsDiff:          true,
		ShowLineNumbers: true,
		Raw:             diff,
	})
	img := renderDiffWindow(t, win, h, image.Pt(winW, winH))
	hasNonBg := countDistinctColors(img)
	if hasNonBg < 4 {
		t.Fatalf("diff image had only %d distinct colors; expected bg + add tint + del tint + marker/text ink", hasNonBg)
	}
	_ = win
}

func renderDiffWindow(t *testing.T, win *headless.Window, h *blockHarness, size image.Point) *image.RGBA {
	t.Helper()
	for range 2 {
		h.ops.Reset()
		h.env.Begin(h.now)
		paint.FillShape(&h.ops, h.th.Chrome().CanvasBg, clip.Rect{Max: size}.Op())
		gtx := &layout.Context{
			Ops:         &h.ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Constraints{Max: size},
			Source:      h.router.Source(),
			Now:         h.now,
		}
		h.env.TrackInput(gtx)
		LayoutCodeBlock(h.env, gtx, h.st, &h.props)
		h.env.FrameEnd(gtx)
		if err := win.Frame(&h.ops); err != nil {
			t.Fatalf("win.Frame: %v", err)
		}
		h.router.Frame(&h.ops)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	return img
}

func countDistinctColors(img *image.RGBA) int {
	seen := map[color.NRGBA]bool{}
	for i := 0; i < len(img.Pix); i += 4 {
		seen[color.NRGBA{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}] = true
	}
	return len(seen)
}

// TestCodeBlockWrapKeyboardAndExpandRules checks unwrapped horizontal scroll by
// default, remembered wrap state, keyboard scrolling, and "Show full output".
func TestCodeBlockWrapKeyboardAndExpandRules(t *testing.T) {
	t.Parallel()
	longLine := strings.Repeat("token_value + ", 20) + "end"
	var sb strings.Builder
	for range 40 {
		sb.WriteString(longLine)
		sb.WriteByte('\n')
	}
	h := newBlockHarness(t, 420, &CodeBlockProps{
		Title:        "Output",
		IsOutputCard: true,
		Raw:          sb.String(),
		Truncated:    true,
	})
	dimsCollapsed, _ := h.frame()
	if h.st.Wrapped {
		t.Fatal("code block must start unwrapped")
	}
	if h.st.maxScrollX <= 0 {
		t.Fatalf("unwrapped wide code block has maxScrollX=%d, want > 0", h.st.maxScrollX)
	}

	h.router.Source().Execute(key.FocusCmd{Tag: h.st.FocusTag()})
	h.frame()
	h.router.Queue(key.Event{Name: key.NameRightArrow, State: key.Press})
	h.frame()
	if h.st.ScrollX <= 0 {
		t.Fatalf("ScrollX after Right arrow = %d, want > 0", h.st.ScrollX)
	}
	h.router.Queue(key.Event{Name: key.NameHome, State: key.Press})
	h.frame()
	if h.st.ScrollX != 0 {
		t.Fatalf("ScrollX after Home = %d, want 0", h.st.ScrollX)
	}

	wrapRes := h.triggerButton(h.st.wrapBtn.Tag())
	if !wrapRes.WrapToggled || !h.st.Wrapped {
		t.Fatal("wrap toggle did not persist Wrapped=true")
	}
	h.frame()
	if !h.st.Wrapped {
		t.Fatal("wrap preference did not survive the next frame")
	}

	expRes := h.triggerButton(h.st.expandBtn.Tag())
	dimsExpanded, _ := h.frame()
	if !h.st.Expanded || !expRes.RequestFull {
		t.Fatalf("Show full output gave Expanded=%v RequestFull=%v, want true/true", h.st.Expanded, expRes.RequestFull)
	}
	if dimsExpanded.Size.Y <= dimsCollapsed.Size.Y {
		t.Fatalf("expanded height %d <= collapsed %d", dimsExpanded.Size.Y, dimsCollapsed.Size.Y)
	}
}

// TestParseChipsPreservesTextAndIgnoresURLs verifies draw-time chip parsing.
func TestParseChipsPreservesTextAndIgnoresURLs(t *testing.T) {
	t.Parallel()
	src := "See ui/internal/mdview/draw.go:42-50, commit 24cd741, task tsk_9f2a1c, and https://example.com/a/b.go:10."
	rebuilt, files, hashes := collectChipResults(ParseChips(src))
	if rebuilt != src {
		t.Fatalf("rebuilt = %q, want %q", rebuilt, src)
	}
	if len(files) != 1 || files[0].Path != "ui/internal/mdview/draw.go" || files[0].Line != 42 || files[0].EndLine != 50 {
		t.Fatalf("file chips = %+v", files)
	}
	if len(hashes) != 2 || hashes[0] != "24cd741" || hashes[1] != "tsk_9f2a1c" {
		t.Fatalf("hash chips = %+v", hashes)
	}
}

func collectChipResults(runs []ChipRun) (rebuilt string, files []FileRef, hashes []string) {
	var sb strings.Builder
	for i := range runs {
		sb.WriteString(runs[i].Text)
		switch runs[i].Kind {
		case ChipFile:
			files = append(files, runs[i].File)
		case ChipHash:
			hashes = append(hashes, runs[i].Text)
		case ChipNone, ChipGitRef:
		}
	}
	return sb.String(), files, hashes
}

// TestDiffHunksAreRoundedAndConcentric verifies v0.11 rounded diff hunks:
// consecutive same-kind rows form one hunk with concentric radius, touching
// different-kind hunks keep a SpaceXXS gap without shifting row heights, and
// scrolled hunks clip cleanly at the viewport edge.
func TestDiffHunksAreRoundedAndConcentric(t *testing.T) {
	t.Parallel()
	assertConcentricRadiusHelper(t)
	assertDiffHunkGroupingAndGap(t)
	assertDiffHunkPixelsRoundedAndGapped(t)
	assertScrolledDiffHunkClipsFlatAtViewport(t)
}

func assertConcentricRadiusHelper(t *testing.T) {
	t.Helper()
	gtx := &layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
	if got, want := ConcentricRadius(gtx, theme.RadiusLG, gtx.Dp(theme.SpaceMD)), 6; got != want {
		t.Fatalf("ConcentricRadius(14dp, 8px) = %d, want %d", got, want)
	}
	if got, want := ConcentricRadius(gtx, theme.RadiusLG, gtx.Dp(theme.SpaceLG)), gtx.Dp(theme.RadiusSM); got != want {
		t.Fatalf("ConcentricRadius(14dp, 12px) = %d, want floor RadiusSM=%d", got, want)
	}
	if got, want := ConcentricRadius(gtx, theme.RadiusLG, gtx.Dp(theme.SpaceXS)), 10; got != want {
		t.Fatalf("ConcentricRadius(14dp, 4px) = %d, want %d", got, want)
	}
}

func newTestView(env *kit.Env, gtx *layout.Context) *view {
	var mdSt State
	ch := env.Th.Chrome()
	mdSt.sync(nil, env.Sh, gtx.Metric, &ch)
	return &view{env: env, th: env.Th, st: &mdSt, shaper: env.Sh, locale: gtx.Locale}
}

func assertDiffHunkGroupingAndGap(t *testing.T) {
	t.Helper()
	diff := "@@ -1,4 +1,4 @@\n-del1\n-del2\n+add1\n+add2\n ctx\n"
	h := newBlockHarness(t, 520, &CodeBlockProps{Format: testFormatDiff, IsDiff: true, Raw: diff})
	st := h.st
	st.syncCache(h.env, &h.props)
	gtx := &layout.Context{
		Ops: &h.ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(520, 400)},
	}
	v := newTestView(h.env, gtx)
	m := v.measureCodeBlock(gtx, st, &h.props)
	hunks := collectDiffHunks(m.diffLines, m.flows, m.visCount, m.pad, 41, m.gutterW+m.markerW+m.codeAvailW, 0, m.lineH, gtx.Dp(theme.SpaceXXS))
	if len(hunks) != 2 {
		t.Fatalf("collectDiffHunks returned %d hunks, want 2: %+v", len(hunks), hunks)
	}
	if hunks[0].kind != DiffDel || hunks[0].start != 1 || hunks[0].end != 3 {
		t.Fatalf("hunk[0] = %+v, want DiffDel [1,3)", hunks[0])
	}
	if hunks[1].kind != DiffAdd || hunks[1].start != 3 || hunks[1].end != 5 {
		t.Fatalf("hunk[1] = %+v, want DiffAdd [3,5)", hunks[1])
	}
	gap := hunks[1].rect.Min.Y - hunks[0].rect.Max.Y
	if want := gtx.Dp(theme.SpaceXXS); gap != want {
		t.Fatalf("gap between touching hunks = %d, want SpaceXXS=%d", gap, want)
	}
	if hunks[0].rect.Min.Y != 41+m.lineH || hunks[1].rect.Max.Y != 41+5*m.lineH {
		t.Fatalf("row positions shifted: h0=%v h1=%v lineH=%d", hunks[0].rect, hunks[1].rect, m.lineH)
	}
}

func assertDiffHunkPixelsRoundedAndGapped(t *testing.T) {
	t.Helper()
	const winW, winH = 520, 220
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	diff := "@@ -1,4 +1,4 @@\n-del1\n-del2\n+add1\n+add2\n ctx\n"
	h := newBlockHarness(t, winW, &CodeBlockProps{Format: testFormatDiff, IsDiff: true, Raw: diff})
	img := renderDiffWindow(t, win, h, image.Pt(winW, winH))
	pad := 8
	bodyTop := 32 + 1 + pad
	lineH := 20
	baseCol := img.RGBAAt(pad, bodyTop)
	delY0 := bodyTop + lineH
	if c := img.RGBAAt(pad, delY0); c != baseCol {
		t.Fatalf("DiffDel top-left corner (%d,%d) = %+v, want rounded corner bg %+v", pad, delY0, c, baseCol)
	}
	if c := img.RGBAAt(400, delY0); c == baseCol {
		t.Fatalf("DiffDel top interior (400,%d) matched bg; expected del tint", delY0)
	}
	if c := img.RGBAAt(pad, delY0+lineH); c == baseCol {
		t.Fatalf("DiffDel mid-hunk seam (%d,%d) matched bg; expected merged single hunk", pad, delY0+lineH)
	}
	if c := img.RGBAAt(400, delY0+2*lineH-1); c != baseCol {
		t.Fatalf("touching boundary gap row y=%d = %+v, want bg %+v", delY0+2*lineH-1, c, baseCol)
	}
	addY0 := delY0 + 2*lineH + 1
	if c := img.RGBAAt(pad, addY0); c != baseCol {
		t.Fatalf("DiffAdd top-left corner (%d,%d) = %+v, want rounded corner bg %+v", pad, addY0, c, baseCol)
	}
	if c := img.RGBAAt(400, addY0); c == baseCol {
		t.Fatalf("DiffAdd top interior (400,%d) matched bg; expected add tint", addY0)
	}
}

func assertScrolledDiffHunkClipsFlatAtViewport(t *testing.T) {
	t.Helper()
	var sb strings.Builder
	sb.WriteString("@@ -1,75 +1,75 @@\n")
	for range 5 {
		sb.WriteString(" ctx\n")
	}
	for range 10 {
		sb.WriteString("+added\n")
	}
	for range 60 {
		sb.WriteString(" tail\n")
	}
	const winW, winH = 520, 420
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	h := newBlockHarness(t, winW, &CodeBlockProps{Format: testFormatDiff, IsDiff: true, Raw: sb.String()})
	h.st.Expanded = true
	h.frame()
	lineH := 20
	h.st.ScrollY = 8 * lineH
	img := renderDiffWindow(t, win, h, image.Pt(winW, winH))

	pad := 8
	bodyTop := 32 + 1 + pad
	hunkBot := bodyTop + 8*lineH
	addTint := img.RGBAAt(400, bodyTop+4)
	if c := img.RGBAAt(pad, bodyTop); c != addTint {
		t.Fatalf("viewport-cut top corner (%d,%d) = %+v, want flat clipped tint %+v", pad, bodyTop, c, addTint)
	}
	if c := img.RGBAAt(pad, hunkBot-1); c == addTint {
		t.Fatalf("real bottom-left corner (%d,%d) = %+v, want rounded bg (not tint)", pad, hunkBot-1, c)
	}
	if c := img.RGBAAt(400, hunkBot-1); c != addTint {
		t.Fatalf("real bottom interior (400,%d) = %+v, want add tint %+v", hunkBot-1, c, addTint)
	}
}

// TestCodeBlockShowsLineNumbersOnlyForMultiLineCode verifies v0.11 line
// numbers: enabled for 2+ line markdown code blocks, diffs, and explicit
// file-viewer blocks, and omitted for single-line and terminal/output blocks.
func TestCodeBlockShowsLineNumbersOnlyForMultiLineCode(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	env := &kit.Env{Th: &th, Sh: shaper}
	var ops op.Ops
	gtx := &layout.Context{
		Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(520, 400)},
	}
	v := newTestView(env, gtx)

	multiDoc := markdown.Parse("```go\nalpha := 1\nbeta := 2\n```\n")
	singleDoc := markdown.Parse("```go\nalpha := 1\n```\n")
	if len(multiDoc.Blocks) == 0 || len(singleDoc.Blocks) == 0 {
		t.Fatal("failed to parse markdown code blocks")
	}

	multiProps := &CodeBlockProps{Format: multiDoc.Blocks[0].Language, ShowLineNumbers: len(multiDoc.Blocks[0].Lines) >= 2, Raw: joinCodeSpans(multiDoc.Blocks[0].Lines), Lines: multiDoc.Blocks[0].Lines}
	singleProps := &CodeBlockProps{Format: singleDoc.Blocks[0].Language, ShowLineNumbers: len(singleDoc.Blocks[0].Lines) >= 2, Raw: joinCodeSpans(singleDoc.Blocks[0].Lines), Lines: singleDoc.Blocks[0].Lines}
	termProps := &CodeBlockProps{Command: "go test ./...", Raw: "ok 1\nok 2\nok 3\n"}
	outProps := &CodeBlockProps{IsOutputCard: true, Format: "yaml", Raw: "a: 1\nb: 2\n"}
	diffProps := &CodeBlockProps{IsDiff: true, Raw: "@@ -1,1 +1,1 @@\n-old\n+new\n"}
	viewerProps := &CodeBlockProps{Format: "go", ShowLineNumbers: true, Raw: "package main\nfunc main() {}\n"}

	measureGutter := func(p *CodeBlockProps) codeBlockMetrics {
		st := NewCodeBlockState()
		st.syncCache(env, p)
		return v.measureCodeBlock(gtx, st, p)
	}
	mMulti := measureGutter(multiProps)
	mDiff := measureGutter(diffProps)
	if mMulti.gutterW <= 0 || mMulti.gutterW != mDiff.gutterW {
		t.Fatalf("multi-line gutterW = %d, diff gutterW = %d, want equal > 0", mMulti.gutterW, mDiff.gutterW)
	}
	if got := measureGutter(singleProps).gutterW; got != 0 {
		t.Fatalf("single-line markdown code gutterW = %d, want 0", got)
	}
	if got := measureGutter(termProps).gutterW; got != 0 {
		t.Fatalf("terminal block gutterW = %d, want 0", got)
	}
	if got := measureGutter(outProps).gutterW; got != 0 {
		t.Fatalf("output card gutterW = %d, want 0", got)
	}
	if got := measureGutter(viewerProps).gutterW; got <= 0 {
		t.Fatalf("file viewer gutterW = %d, want > 0", got)
	}
	assertMarkdownCodeLineNumberPixels(t, &th)
}

func assertMarkdownCodeLineNumberPixels(t *testing.T, th *theme.Theme) {
	t.Helper()
	const winW, winH = 420, 140
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	multiDoc := markdown.Parse("```go\n    line1\n    line2\n```\n")
	singleDoc := markdown.Parse("```go\n    line1\n```\n")
	var multiSt, singleSt State
	var r1, r2 input.Router
	multiImg := renderWidgetPixels(t, win, image.Pt(winW, winH), th, &r1, func(env *kit.Env, gtx *layout.Context) {
		LayoutEnv(env, gtx, multiDoc, &multiSt)
	})
	singleImg := renderWidgetPixels(t, win, image.Pt(winW, winH), th, &r2, func(env *kit.Env, gtx *layout.Context) {
		LayoutEnv(env, gtx, singleDoc, &singleSt)
	})
	if multiSt.codeBlock(&multiDoc.Blocks[0]).viewW >= singleSt.codeBlock(&singleDoc.Blocks[0]).viewW {
		t.Fatalf("multi-line viewW=%d >= single-line viewW=%d; expected gutter deduction",
			multiSt.codeBlock(&multiDoc.Blocks[0]).viewW, singleSt.codeBlock(&singleDoc.Blocks[0]).viewW)
	}
	pad, spaceSM, bodyTop := 8, 6, 41
	baseCol := compositeNRGBA(th.Chrome().CanvasBg, th.Chrome().SurfaceBase)
	if !regionHasNonColor(multiImg, pad+spaceSM, pad+spaceSM+8, bodyTop, bodyTop+40, baseCol) {
		t.Fatal("multi-line markdown code block rendered no line-number ink in gutter")
	}
	if regionHasNonColor(singleImg, pad+spaceSM, pad+spaceSM+8, bodyTop, bodyTop+20, baseCol) {
		t.Fatal("single-line markdown code block rendered unexpected ink in gutter")
	}
}

func regionHasNonColor(img *image.RGBA, x0, x1, y0, y1 int, bg color.NRGBA) bool {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := img.RGBAAt(x, y)
			dr, dg, db := int(c.R)-int(bg.R), int(c.G)-int(bg.G), int(c.B)-int(bg.B)
			if dr*dr+dg*dg+db*db > 16 {
				return true
			}
		}
	}
	return false
}
