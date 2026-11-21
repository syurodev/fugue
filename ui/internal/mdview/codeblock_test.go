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

func assertCopyDiffExcludesMarkers(t *testing.T) {
	t.Helper()
	diff := "diff --git a/a.go b/a.go\nindex 111..222 100644\n--- a/a.go\n+++ b/a.go\n@@ -10,3 +10,3 @@\n ctx()\n-old()\n+new()\n"
	h := newBlockHarness(t, 640, &CodeBlockProps{
		Format:          "diff",
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
		Format:          "diff",
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
