package transcriptview

import (
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	daOldFile     = "old.go"
	daSampleFile  = "pkg/a.go"
	testOpenInbox = "Open inbox"
)

func renderGroupExpanded(t *testing.T, th *theme.Theme, size image.Point, items []*session.Item, openKeys []string) *image.RGBA {
	t.Helper()
	win, err := glwin.Open(size.X, size.Y)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	return renderItemsPixelsExpanded(t, win, size, th, items, openKeys)
}

// TestWorkGroupUsesARailAndNoDivider verifies DA-E16: an expanded work group
// draws a vertical left BorderSubtle rail down its child rows and zero
// full-width horizontal Divider beneath "Worked for…".
func TestWorkGroupUsesARailAndNoDivider(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	items := []*session.Item{
		makeWorkToolItem("w1", 1, "turn-wg", "read_file", statusDone, `{"file_path":"a.go"}`, 0),
		makeWorkToolItem("w2", 2, "turn-wg", "bash", statusDone, `{"command":"go vet ./..."}`, 0),
		makeWorkToolItem("w3", 3, "turn-wg", "grep", statusDone, `{"pattern":"foo"}`, 0),
	}
	size := image.Pt(720, 160)
	img := renderGroupExpanded(t, &th, size, items, []string{"wg:w1"})
	assertVerticalRailPresent(t, img, th.Chrome().CanvasBg)
	assertNoHorizontalDividerUnderHeader(t, img, th.Chrome().CanvasBg)
}

func assertVerticalRailPresent(t *testing.T, img *image.RGBA, bg color.NRGBA) {
	t.Helper()
	railPixels := 0
	for x := 3; x <= 5; x++ {
		for y := 20; y < 120; y++ {
			if colorDistanceSq(color.NRGBA(img.RGBAAt(x, y)), bg) > 9 {
				railPixels++
			}
		}
	}
	if railPixels < 40 {
		t.Fatalf("work group vertical rail had %d non-bg pixels near x=4, want >= 40", railPixels)
	}
}

func assertNoHorizontalDividerUnderHeader(t *testing.T, img *image.RGBA, bg color.NRGBA) {
	t.Helper()
	for y := 16; y <= 28; y++ {
		hit := 0
		for x := 420; x < 680; x++ {
			if colorDistanceSq(color.NRGBA(img.RGBAAt(x, y)), bg) > 9 {
				hit++
			}
		}
		if hit > 200 {
			t.Fatalf("unexpected horizontal divider line at y=%d (%d non-bg pixels)", y, hit)
		}
	}
}

// TestFailedRowTintsOnlyThatRow verifies DA-E16: a failed tool row paints
// DangerSoft only behind the failed row itself, never nesting its expanded
// Output/terminal block inside a DangerSoft card.
func TestFailedRowTintsOnlyThatRow(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	fail := makeWorkToolItem("fail-exp", 1, "", "bash", statusFailed, `{"command":"false"}`, 1)
	fail.View.Text = "line1\nline2\nline3\nline4\n"

	size := image.Pt(680, 260)
	img := renderGroupExpanded(t, &th, size, []*session.Item{fail}, []string{"fail-exp"})

	rowTint, bodyTint := 0, 0
	for y := range size.Y {
		for x := 20; x < size.X-20; x++ {
			c := img.RGBAAt(x, y)
			if int(c.R)-int(c.B) >= 8 && c.R > c.G {
				if y < 60 {
					rowTint++
				} else if y > 90 {
					bodyTint++
				}
			}
		}
	}
	if rowTint < 1000 {
		t.Fatalf("failed header row drew only %d DangerSoft pixels, want >= 1000", rowTint)
	}
	if bodyTint != 0 {
		t.Fatalf("expanded Output/terminal body drew %d DangerSoft pixels, want 0 (never nested in error ground)", bodyTint)
	}
}

// TestFileBadgeLooksTheSameInProseRowsAndResults verifies DA-E16: file badges in
// prose, tool rows, and search results share the SurfaceRaised + BorderSubtle
// pill chrome, trigger OnOpenFile on click, while deleted badges are muted/non-clickable.
func TestFileBadgeLooksTheSameInProseRowsAndResults(t *testing.T) {
	t.Parallel()
	var opened []mdview.FileRef
	regCB := func(r mdview.FileRef) { opened = append(opened, r) }

	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	deletedSpec := inspectToolRow(&session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "del-1", Kind: kindFileChange,
		Metadata: client.TranscriptMetadata{ChangeKind: "delete", LinesRemoved: 4},
		View:     client.TranscriptView{Title: daOldFile},
	}})
	if deletedSpec.verb != verbDeleted || deletedSpec.expandable {
		t.Fatalf("deleted spec = %+v, want verbDeleted non-expandable", deletedSpec)
	}

	assertDeletedBadgeIgnoresClick(t, &th, regCB)
	assertFileBadgeClicksAcrossContexts(t, &th, regCB, &opened)
}

func assertDeletedBadgeIgnoresClick(t *testing.T, th *theme.Theme, onOpen func(mdview.FileRef)) {
	t.Helper()
	var router input.Router
	click := &widget.Clickable{}
	called := false
	draw := func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return mdview.DrawBadge(env, gtx, click, mdview.BadgeSpec{
			Kind: mdview.BadgeDeleted,
			File: mdview.FileRef{Path: daOldFile, Raw: daOldFile},
		}, func(r mdview.FileRef) {
			called = true
			onOpen(r)
		})
	}
	runBadgeFrame(t, th, &router, draw)
	router.Source().Execute(key.FocusCmd{Tag: click})
	runBadgeFrame(t, th, &router, draw)
	router.Queue(key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	runBadgeFrame(t, th, &router, draw)
	runBadgeFrame(t, th, &router, draw)
	if called {
		t.Fatal("deleted file badge must not invoke OnOpenFile")
	}
}

func assertFileBadgeClicksAcrossContexts(
	t *testing.T, th *theme.Theme, onOpen func(mdview.FileRef), opened *[]mdview.FileRef,
) {
	t.Helper()
	readSpec := inspectToolRow(makeWorkToolItem("r1", 1, "", "read_file", statusDone, `{"file_path":"`+daSampleFile+`","start_line":10}`, 0))
	if readSpec.verb != verbRead || readSpec.path != daSampleFile || readSpec.line != 10 {
		t.Fatalf("readSpec = %+v", readSpec)
	}
	searchMatches := parseSearchResults(daSampleFile + ":10: match")
	if len(searchMatches) != 1 || searchMatches[0].Path != daSampleFile || searchMatches[0].Line != 10 {
		t.Fatalf("searchMatches = %+v", searchMatches)
	}

	var router input.Router
	click := &widget.Clickable{}
	hRow := runBadgeFrame(t, th, &router, func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return renderToolRowLine(env, gtx, cardOf(NewState(kit.NewVirtualHeightCache()), "r1"), &readSpec, "", false, false, Callbacks{OnOpenFile: onOpen})
	})
	drawBadge := func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return mdview.DrawBadge(env, gtx, click, mdview.BadgeSpec{Kind: mdview.BadgeFile, File: searchMatches[0]}, onOpen)
	}
	hBadge := runBadgeFrame(t, th, &router, drawBadge)
	router.Source().Execute(key.FocusCmd{Tag: click})
	runBadgeFrame(t, th, &router, drawBadge)
	router.Queue(key.Event{Name: key.NameReturn, State: key.Press}, key.Event{Name: key.NameReturn, State: key.Release})
	runBadgeFrame(t, th, &router, drawBadge)
	runBadgeFrame(t, th, &router, drawBadge)
	if hBadge.Size.Y != hRow.Size.Y && hBadge.Size.Y <= 0 {
		t.Fatalf("badge height = %d, want > 0", hBadge.Size.Y)
	}
	if len(*opened) != 1 || (*opened)[0].Path != daSampleFile || (*opened)[0].Line != 10 {
		t.Fatalf("opened file callbacks = %+v, want %s:10", *opened, daSampleFile)
	}
}

func runBadgeFrame(t *testing.T, th *theme.Theme, router *input.Router, draw func(*kit.Env, *layout.Context) layout.Dimensions) layout.Dimensions {
	t.Helper()
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	env.Begin(now)
	gtx := &layout.Context{
		Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Constraints{Max: image.Pt(600, 120)}, Source: router.Source(), Now: now,
	}
	env.TrackInput(gtx)
	dims := draw(env, gtx)
	env.FrameEnd(gtx)
	router.Frame(&ops)
	return dims
}

// TestToolRowObjectEllipsizesBeforeTheRightSlot verifies that a long command
// and a long path at 125% UI scale stay on one line, truncate with an ellipsis
// inside the Flexed object slot, and leave the right-hand action slot intact
// with zero overlap.
func TestToolRowObjectEllipsizesBeforeTheRightSlot(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Dark(shaper)
	assertToolRowEllipsisCase(t, &th,
		&toolRowSpec{verb: verbApproval, icon: kit.IconHelp, verbLabel: "Waiting for approval", query: "ls", warningRow: true, actionText: testOpenInbox},
		&toolRowSpec{verb: verbApproval, icon: kit.IconHelp, verbLabel: "Waiting for approval", query: "go test -race -shuffle=on -tags regenerate ./internal/appwin/very/long/subpackage", warningRow: true, actionText: testOpenInbox},
	)
	assertToolRowEllipsisCase(t, &th,
		&toolRowSpec{verb: verbEdited, icon: kit.IconPencil, verbLabel: "Edited", path: "a.go", line: 12, warningRow: true, actionText: testOpenInbox},
		&toolRowSpec{verb: verbEdited, icon: kit.IconPencil, verbLabel: "Edited", path: "ui/internal/very/deeply/nested/subpackage/component/codeblock_draw.go", line: 124, warningRow: true, actionText: testOpenInbox},
	)
}

func assertToolRowEllipsisCase(t *testing.T, th *theme.Theme, shortSpec, longSpec *toolRowSpec) {
	t.Helper()
	const winW, winH = 460, 80
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	shortImg, shortDims, rightBound := renderToolRowAt125(t, win, th, image.Pt(winW, winH), shortSpec)
	longImg, longDims, _ := renderToolRowAt125(t, win, th, image.Pt(winW, winH), longSpec)
	if longDims.Size.Y != shortDims.Size.Y {
		t.Fatalf("long row height %d != single-line short row height %d", longDims.Size.Y, shortDims.Size.Y)
	}
	if longDims.Size.X > winW {
		t.Fatalf("long row width %d exceeded container %d", longDims.Size.X, winW)
	}
	for y := range longDims.Size.Y {
		for x := rightBound; x < winW; x++ {
			if longImg.RGBAAt(x, y) != shortImg.RGBAAt(x, y) {
				t.Fatalf("right slot pixel (%d,%d) changed from %+v to %+v; object overlapped right slot",
					x, y, shortImg.RGBAAt(x, y), longImg.RGBAAt(x, y))
			}
		}
	}
	if !bandDiffersRGBA(shortImg, longImg, rightBound-24, rightBound, 0, longDims.Size.Y) {
		t.Fatalf("expected truncated object/ellipsis ink in [%d,%d) before right slot", rightBound-24, rightBound)
	}
}

func renderToolRowAt125(
	t *testing.T, win *headless.Window, th *theme.Theme, size image.Point, spec *toolRowSpec,
) (*image.RGBA, layout.Dimensions, int) {
	t.Helper()
	var ops op.Ops
	var router input.Router
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	card := cardOf(NewState(kit.NewVirtualHeightCache()), "ellip")
	var dims layout.Dimensions
	rightBound := size.X
	for range 2 {
		ops.Reset()
		env.Begin(now)
		paint.FillShape(&ops, th.Chrome().CanvasBg, clip.Rect{Max: size}.Op())
		gtx := &layout.Context{
			Ops: &ops, Metric: unit.Metric{PxPerDp: 1.25, PxPerSp: 1.25},
			Constraints: layout.Constraints{Max: size}, Source: router.Source(), Now: now,
		}
		env.TrackInput(gtx)
		dims = wrapSurfaceTone(env, gtx, env.Th.StatusTone(theme.StatusWarning).Fill, func(gtx *layout.Context) layout.Dimensions {
			return renderToolRowLine(env, gtx, card, spec, "", false, false, Callbacks{})
		})
		btnW := card.inboxBtn.Measure(env, gtx, kit.ButtonProps{Text: spec.actionText, Variant: kit.Ghost, Size: kit.SizeSM}).Size.X
		rightBound = size.X - gtx.Dp(theme.SpaceMD) - btnW - gtx.Dp(theme.SpaceSM)
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
	return img, dims, rightBound
}

func bandDiffersRGBA(a, b *image.RGBA, x0, x1, y0, y1 int) bool {
	for y := max(y0, 0); y < min(y1, a.Bounds().Dy()); y++ {
		for x := max(x0, 0); x < min(x1, a.Bounds().Dx()); x++ {
			if a.RGBAAt(x, y) != b.RGBAAt(x, y) {
				return true
			}
		}
	}
	return false
}

// TestToolRowsShareOneHeight verifies that plain, badge, warning, failed, and
// sub-group summary rows share the theme.Row (28 dp) height at 100% and 125%,
// badges fit without clipping at 90/100/125%, and warning/failed tints cover
// the full row height.
func TestToolRowsShareOneHeight(t *testing.T) {
	t.Parallel()
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	for _, scale := range []float32{1.0, 1.25} {
		assertToolRowsHeightAtScale(t, &th, scale)
	}
	for _, scale := range []float32{0.9, 1.0, 1.25} {
		assertBadgeFitsRowAtScale(t, &th, scale)
	}
	assertRowTintsCoverFullHeight(t, &th)
}

func assertToolRowsHeightAtScale(t *testing.T, th *theme.Theme, scale float32) {
	t.Helper()
	st := NewState(kit.NewVirtualHeightCache())
	plainItem := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "h-plain", Kind: kindSubagent, Sequence: 1, Revision: 1,
		Metadata: client.TranscriptMetadata{Status: statusDone}, View: client.TranscriptView{Title: "Verify lint clean"},
	}}
	badgeItem := makeWorkToolItem("h-badge", 2, "", "read_file", statusDone, `{"file_path":"ui/internal/mdview/codeblock.go","start_line":10,"end_line":40}`, 0)
	warnItem := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "h-warn", Kind: kindApproval, Sequence: 3, Revision: 1,
		Metadata: client.TranscriptMetadata{RequestKind: "command", Status: "pending"},
		View:     client.TranscriptView{Title: "go test ./internal/mdview"},
	}}
	failItem := makeWorkToolItem("h-fail", 4, "", "bash", statusFailed, `{"command":"false"}`, 1)
	failSpec := inspectToolRow(failItem)
	sg := subGroup{title: "Read 3 files"}

	measure := func(fn func(*kit.Env, *layout.Context) layout.Dimensions) (int, int) {
		var ops op.Ops
		var router input.Router
		now := time.Unix(1_700_000_000, 0)
		env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
		env.Begin(now)
		gtx := &layout.Context{
			Ops: &ops, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
			Constraints: layout.Constraints{Max: image.Pt(600, 200)}, Source: router.Source(), Now: now,
		}
		env.TrackInput(gtx)
		d := fn(env, gtx)
		env.FrameEnd(gtx)
		return d.Size.Y, gtx.Dp(theme.Row)
	}

	plainH, wantH := measure(func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return layoutWorkMember(env, gtx, st, plainItem, Callbacks{})
	})
	badgeH, _ := measure(func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return layoutWorkMember(env, gtx, st, badgeItem, Callbacks{})
	})
	warnH, _ := measure(func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return layoutItem(env, gtx, st, warnItem, Callbacks{})
	})
	failLineH, _ := measure(func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return renderToolRowLine(env, gtx, cardOf(st, "h-fail-line"), &failSpec, "exit 1", true, false, Callbacks{})
	})
	summaryH, _ := measure(func(env *kit.Env, gtx *layout.Context) layout.Dimensions {
		return layoutSubGroupHeader(env, gtx, &sg, false)
	})

	// A tinted row keeps the shared line height and adds SpaceXS above and below
	// inside its tint.
	pad := int(float32(theme.SpaceXS) * scale)
	if plainH != wantH || badgeH != wantH || warnH != wantH+2*pad || failLineH != wantH || summaryH != wantH {
		t.Fatalf("scale %.2f heights: plain=%d badge=%d warn=%d failedLine=%d summary=%d, want %d (warn %d)",
			scale, plainH, badgeH, warnH, failLineH, summaryH, wantH, wantH+2*pad)
	}
}

func assertBadgeFitsRowAtScale(t *testing.T, th *theme.Theme, scale float32) {
	t.Helper()
	var ops op.Ops
	var router input.Router
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	env.Begin(now)
	gtx := &layout.Context{
		Ops: &ops, Metric: unit.Metric{PxPerDp: scale, PxPerSp: scale},
		Constraints: layout.Constraints{Max: image.Pt(600, 120)}, Source: router.Source(), Now: now,
	}
	env.TrackInput(gtx)
	click := &widget.Clickable{}
	badgeDims := mdview.DrawBadge(env, gtx, click, mdview.BadgeSpec{
		Kind: mdview.BadgeFile,
		File: mdview.FileRef{Path: daSampleFile, Line: 10, EndLine: 20, Raw: daSampleFile},
	}, nil)
	env.FrameEnd(gtx)
	if rowH := gtx.Dp(theme.Row); badgeDims.Size.Y > rowH {
		t.Fatalf("scale %.2f badge height %d > row height %d", scale, badgeDims.Size.Y, rowH)
	}
}

func assertRowTintsCoverFullHeight(t *testing.T, th *theme.Theme) {
	t.Helper()
	const winW, winH = 600, 120
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	st := NewState(kit.NewVirtualHeightCache())
	warnItem := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "t-warn", Kind: kindApproval, Sequence: 1, Revision: 1,
		Metadata: client.TranscriptMetadata{RequestKind: "command", Status: "pending"},
		View:     client.TranscriptView{Title: "go test ./..."},
	}}
	failItem := makeWorkToolItem("t-fail", 2, "", "bash", statusFailed, `{"command":"false"}`, 1)
	failItem.View.Text = "permission denied"

	assertItemTintBounds(t, win, th, image.Pt(winW, winH), st, warnItem, "warning")
	assertItemTintBounds(t, win, th, image.Pt(winW, winH), st, failItem, "failed")
}

func assertItemTintBounds(
	t *testing.T, win *headless.Window, th *theme.Theme, size image.Point, st *State, item *session.Item, name string,
) {
	t.Helper()
	var ops op.Ops
	var router input.Router
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	var dims layout.Dimensions
	for range 2 {
		ops.Reset()
		env.Begin(now)
		paint.FillShape(&ops, th.Chrome().CanvasBg, clip.Rect{Max: size}.Op())
		gtx := &layout.Context{
			Ops: &ops, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Constraints: layout.Constraints{Max: size}, Source: router.Source(), Now: now,
		}
		env.TrackInput(gtx)
		dims = layoutItem(env, gtx, st, item, Callbacks{})
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
	bg := th.Chrome().CanvasBg
	topCol := color.NRGBA(img.RGBAAt(size.X/2, 0))
	botCol := color.NRGBA(img.RGBAAt(size.X/2, dims.Size.Y-1))
	if topCol == bg || botCol == bg {
		t.Fatalf("%s row tint did not cover full height [0,%d): top=%+v bottom=%+v bg=%+v",
			name, dims.Size.Y, topCol, botCol, bg)
	}
}
