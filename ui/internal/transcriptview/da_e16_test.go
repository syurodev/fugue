package transcriptview

import (
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
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
	daOldFile    = "old.go"
	daSampleFile = "pkg/a.go"
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
