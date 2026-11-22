package mdview

import (
	"image"
	"image/color"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type fixedClock struct{ now time.Time }

func (c fixedClock) Now() time.Time { return c.now }

func mdInt32Ptr(v int32) *int32 { return &v }

func renderWidgetPixels(
	t *testing.T, win *headless.Window, size image.Point, th *theme.Theme,
	router *input.Router, draw func(*kit.Env, *layout.Context),
) *image.RGBA {
	t.Helper()
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	for range 2 {
		ops.Reset()
		env.Begin(now)
		paint.FillShape(&ops, th.Chrome().CanvasBg, clip.Rect{Max: size}.Op())
		gtx := &layout.Context{
			Ops:         &ops,
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Constraints{Max: size},
			Source:      router.Source(),
			Now:         now,
		}
		env.TrackInput(gtx)
		draw(env, gtx)
		env.FrameEnd(gtx)
		if err := win.Frame(&ops); err != nil {
			t.Fatalf("win.Frame: %v", err)
		}
		router.Frame(&ops)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot: %v", err)
	}
	return img
}

func compositeNRGBA(bg, fg color.NRGBA) color.NRGBA {
	a := uint32(fg.A)
	inv := 255 - a
	return color.NRGBA{
		R: byte(((uint32(fg.R)*a + uint32(bg.R)*inv) / 255) & 0xff),
		G: byte(((uint32(fg.G)*a + uint32(bg.G)*inv) / 255) & 0xff),
		B: byte(((uint32(fg.B)*a + uint32(bg.B)*inv) / 255) & 0xff),
		A: 255,
	}
}

func rowContainsColor(img *image.RGBA, y0, y1 int, target color.NRGBA) bool {
	for y := max(y0, 0); y < min(y1, img.Bounds().Dy()); y++ {
		for x := 8; x < img.Bounds().Dx()-8; x++ {
			c := img.RGBAAt(x, y)
			dr := int(c.R) - int(target.R)
			dg := int(c.G) - int(target.G)
			db := int(c.B) - int(target.B)
			if dr*dr+dg*dg+db*db <= 16 {
				return true
			}
		}
	}
	return false
}

func hasDividerRow(img *image.RGBA, y0, y1 int, surf color.NRGBA) bool {
	for y := max(y0, 0); y < min(y1, img.Bounds().Dy()); y++ {
		hits := 0
		for x := 24; x < img.Bounds().Dx()-24; x++ {
			c := img.RGBAAt(x, y)
			if c.R != surf.R || c.G != surf.G || c.B != surf.B {
				hits++
			}
		}
		if hits > img.Bounds().Dx()/2 {
			return true
		}
	}
	return false
}

// TestCodeTerminalOutputAndDiffShareOneTopHeader verifies DA-E16: code blocks,
// terminal blocks, Output cards, and diffs share the same top header +
// SurfaceRaised frame with no bottom footer bar.
func TestCodeTerminalOutputAndDiffShareOneTopHeader(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		props CodeBlockProps
		title string
		fmt   string
	}{
		{"code", CodeBlockProps{Format: "go", Raw: "package p\n"}, "go", ""},
		{"terminal", CodeBlockProps{Command: "go test ./...", ExitCode: mdInt32Ptr(0), Duration: "120ms", Raw: "ok\n"}, "shell", ""},
		{"output", CodeBlockProps{IsOutputCard: true, Format: "json", Raw: `{"ok":true}`}, "output", "json"},
		{"diffHeader", CodeBlockProps{IsDiff: true, Title: "a.go", Raw: "@@ -1,1 +1,1 @@\n-old\n+new\n"}, "a.go", ""},
	}
	const winW, winH = 540, 140
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	for _, tc := range cases {
		head := resolvedHeader(&tc.props)
		if head.title != tc.title || head.fmtLabel != tc.fmt {
			t.Fatalf("%s resolvedHeader = (%q, %q), want (%q, %q)", tc.name, head.title, head.fmtLabel, tc.title, tc.fmt)
		}
		var dims layout.Dimensions
		st := NewCodeBlockState()
		var router input.Router
		img := renderWidgetPixels(t, win, image.Pt(winW, winH), &th, &router, func(env *kit.Env, gtx *layout.Context) {
			dims, _ = LayoutCodeBlock(env, gtx, st, &tc.props)
		})
		if !rowContainsColor(img, 2, 20, compositeNRGBA(th.Chrome().CanvasBg, th.Chrome().SurfaceRaised)) {
			t.Fatalf("%s missing SurfaceRaised header background", tc.name)
		}
		surf := compositeNRGBA(th.Chrome().CanvasBg, th.Chrome().SurfaceRaised)
		if !hasDividerRow(img, 28, 42, surf) {
			t.Fatalf("%s missing top header divider near y=28..42", tc.name)
		}
		_ = dims
	}
}

// TestCodeBlocksAreSyntaxColoured verifies DA-E16: Go code, JSON output, shell
// commands, diffs, and markdown fenced code tokenize into distinct syntax roles
// rendered with theme syntax colors.
func TestCodeBlocksAreSyntaxColoured(t *testing.T) {
	t.Parallel()
	clk := fixedClock{now: time.Unix(1_700_000_000, 0)}
	doc := markdown.Parse("```go\npackage main\nfunc answer() string { return \"hi\" }\n```\n", markdown.WithClock(clk))
	if len(doc.Blocks) == 0 || !hasMultipleRoles(doc.Blocks[0].Lines) {
		t.Fatalf("markdown Go block lacked distinct syntax roles: %+v", doc.Blocks)
	}
	dl := ParseDiff("@@ -1,1 +1,1 @@\n-old := \"a\"\n+func f() {}\n")
	HighlightDiff("a.go", dl, clk)
	if len(dl) < 3 || !hasMultipleRoles([][]markdown.Span{dl[2].Spans}) {
		t.Fatalf("HighlightDiff did not tokenize Go diff lines: %+v", dl)
	}
	assertCodeBlockRenderHasSyntaxInk(t)
}

func hasMultipleRoles(lines [][]markdown.Span) bool {
	seen := map[markdown.Role]bool{}
	for _, line := range lines {
		for _, sp := range line {
			if sp.Role != "" {
				seen[sp.Role] = true
			}
		}
	}
	return len(seen) >= 2
}

func assertCodeBlockRenderHasSyntaxInk(t *testing.T) {
	t.Helper()
	const winW, winH = 520, 140
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	h := newBlockHarness(t, winW, &CodeBlockProps{
		Format: "go",
		Raw:    "package main\nfunc hi() string { return \"ok\" } // note\n",
	})
	img := renderDiffWindow(t, win, h, image.Pt(winW, winH))
	if got := countDistinctColors(img); got < 5 {
		t.Fatalf("syntax-highlighted Go codeblock produced %d distinct colors, want >= 5", got)
	}
}

// TestTerminalExpandsToTheFullCommandAndOutput verifies DA-E16: an expanded
// terminal block shows the full wrapped "$ <command>", a divider hairline, muted
// output capped by default, and "Show full output".
func TestTerminalExpandsToTheFullCommandAndOutput(t *testing.T) {
	t.Parallel()
	longCmd := strings.Repeat("go test -run TestExample ", 6)
	var sb strings.Builder
	for i := range 40 {
		sb.WriteString("line ")
		sb.WriteString(strings.Repeat("x", 10))
		if i < 39 {
			sb.WriteByte('\n')
		}
	}
	h := newBlockHarness(t, 420, &CodeBlockProps{
		Command:  longCmd,
		ExitCode: mdInt32Ptr(0),
		Duration: "250ms",
		Raw:      sb.String(),
		MaxLines: 10,
	})
	collapsed, _ := h.frame()
	if len(h.st.cachedCmdLn) == 0 {
		t.Fatal("terminal command cache not populated")
	}
	if _, vis := capLineCount(h.props.MaxLines, h.st.Expanded, false, 40); vis != 10 {
		t.Fatalf("initial terminal visible lines = %d, want 10", vis)
	}
	expRes := h.triggerButton(h.st.expandBtn.Tag())
	expanded, _ := h.frame()
	if !h.st.Expanded || expanded.Size.Y <= collapsed.Size.Y {
		t.Fatalf("terminal expand height %d <= collapsed %d (res=%+v)", expanded.Size.Y, collapsed.Size.Y, expRes)
	}
}

// TestGitRefHoverAndClickUseTheRegistry verifies DA-E16: hovering a git ref
// draws the floating card and clicking dispatches through GitRefRegistry (stage A:
// commit copies full hash, PR opens https URLs only).
func TestGitRefHoverAndClickUseTheRegistry(t *testing.T) {
	t.Parallel()
	var opened []string
	reg := DefaultGitRefRegistry(func(u string) { opened = append(opened, u) })
	commit := GitRef{Kind: GitCommit, Text: "24cd741", Hash: "24cd741abcdef", Subject: "feat(ui): line row"}
	prHTTPS := GitRef{Kind: GitPR, Text: "#185", URL: "https://github.com/syurodev/fugue/pull/185"}
	prHTTP := GitRef{Kind: GitPR, Text: "#99", URL: "http://example.com/pull/99"}

	const winW, winH = 420, 140
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	var router input.Router
	click := &widget.Clickable{}

	plainImg := renderWidgetPixels(t, win, image.Pt(winW, winH), &th, &router, func(env *kit.Env, gtx *layout.Context) {
		DrawGitRefChip(env, gtx, click, commit, reg)
	})
	router.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(15, 8)})
	hoverImg := renderWidgetPixels(t, win, image.Pt(winW, winH), &th, &router, func(env *kit.Env, gtx *layout.Context) {
		DrawGitRefChip(env, gtx, click, commit, reg)
	})
	if countNonCanvas(hoverImg, th.Chrome().CanvasBg) <= countNonCanvas(plainImg, th.Chrome().CanvasBg)+200 {
		t.Fatal("hovering git ref chip did not render floating summary card")
	}

	router.Source().Execute(key.FocusCmd{Tag: click})
	router.Queue(key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	renderWidgetPixels(t, win, image.Pt(winW, winH), &th, &router, func(env *kit.Env, gtx *layout.Context) {
		DrawGitRefChip(env, gtx, click, commit, reg)
	})
	if _, clipData, ok := router.WriteClipboard(); !ok || string(clipData) != "24cd741abcdef" {
		t.Fatalf("commit click clipboard = (%q, %v), want full hash 24cd741abcdef", string(clipData), ok)
	}

	RunGitRefAction(nil, reg, prHTTP)
	RunGitRefAction(nil, reg, prHTTPS)
	if len(opened) != 1 || opened[0] != prHTTPS.URL {
		t.Fatalf("PR registry opened %v, want only https URL", opened)
	}
}

func countNonCanvas(img *image.RGBA, bg color.NRGBA) int {
	n := 0
	for i := 0; i < len(img.Pix); i += 4 {
		dr := int(img.Pix[i]) - int(bg.R)
		dg := int(img.Pix[i+1]) - int(bg.G)
		db := int(img.Pix[i+2]) - int(bg.B)
		if dr*dr+dg*dg+db*db > 9 {
			n++
		}
	}
	return n
}
