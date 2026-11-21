package transcriptview

import (
	"fmt"
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func makeWorkToolItem(id string, seq uint64, turnID, tool, status, inputJSON string, exit int32) *session.Item {
	return &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: id, TurnID: turnID, Kind: kindToolCall, Sequence: seq, Revision: 1,
		Metadata: client.TranscriptMetadata{
			Tool: tool, Status: status, ExitCode: int32Ptr(exit), DurationMillis: 250,
		},
		View: client.TranscriptView{Input: inputJSON},
	}}
}

func buildFortyToolCallTurn() []*session.Item {
	items := make([]*session.Item, 0, 41)
	specs := []struct {
		count int
		tool  string
		tmpl  string
	}{
		{20, "read_file", `{"file_path":"pkg/file_%02d.go"}`},
		{10, "edit_file", `{"file_path":"pkg/file_%02d.go"}`},
		{10, "bash", `{"command":"go test ./p%d"}`},
	}
	seq := 0
	for _, sp := range specs {
		for range sp.count {
			seq++
			items = append(items, makeWorkToolItem(
				fmt.Sprintf("tc-%02d", seq), uint64(seq), "turn-40",
				sp.tool, statusDone, fmt.Sprintf(sp.tmpl, seq), 0,
			))
		}
	}
	items = append(items, message("msg-after", 41, "All 40 calls done."))
	return items
}

// TestFortyToolCallsFoldIntoOneWorkedForLine verifies DA-E12 / §5.4:
// 40 consecutive work items in a turn fold into one "Worked for …" row
// collapsed when finished, open with same-kind sub-groups when running.
func TestFortyToolCallsFoldIntoOneWorkedForLine(t *testing.T) {
	t.Parallel()
	items := buildFortyToolCallTurn()
	rows := buildDisplayRows(items)
	if len(rows) != 2 {
		t.Fatalf("buildDisplayRows(40 tool calls + 1 msg) = %d rows, want 2", len(rows))
	}
	grp := verifyFoldedFortyGroup(t, rows[0].group)

	h := newHarness(t, 760, 800, items)
	h.frame()
	if h.st.Rendered() != 2 || h.st.list.Expanded(grp.key) {
		t.Fatalf("finished work group Rendered=%d Expanded=%v, want 2/false", h.st.Rendered(), h.st.list.Expanded(grp.key))
	}
	items[39].Metadata.Status = statusRunning
	items[39].Revision = 2
	h.frame()
	if !h.st.list.Expanded(grp.key) {
		t.Fatal("running 40-tool-call work group must start open")
	}
	if got := workGroupHeaderText(buildDisplayRows(items)[0].group); got != "go test ./p40" {
		t.Fatalf("running header = %q, want active command title", got)
	}
}

func verifyFoldedFortyGroup(t *testing.T, grp *workGroup) *workGroup {
	t.Helper()
	if grp == nil || len(grp.items) != 40 || len(grp.subGroups) != 3 {
		t.Fatalf("work group = %+v, want 40 items / 3 sub-groups", grp)
	}
	wantTitles := []string{"Read 20 files", "Edited 10 files", "Ran 10 commands"}
	for i, want := range wantTitles {
		if grp.subGroups[i].title != want {
			t.Fatalf("subGroup[%d] title = %q, want %q", i, grp.subGroups[i].title, want)
		}
	}
	if got := workGroupHeaderText(grp); got != "Worked for 10s" {
		t.Fatalf("header = %q, want %q", got, "Worked for 10s")
	}
	return grp
}

func renderItemsPixels(t *testing.T, win *headless.Window, size image.Point, th *theme.Theme, items []*session.Item) *image.RGBA {
	t.Helper()
	return renderItemsPixelsExpanded(t, win, size, th, items, nil)
}

func renderItemsPixelsExpanded(t *testing.T, win *headless.Window, size image.Point, th *theme.Theme, items []*session.Item, openKeys []string) *image.RGBA {
	t.Helper()
	var ops op.Ops
	var router input.Router
	now := time.Unix(1_700_000_000, 0)
	env := &kit.Env{Th: th, Sh: th.Shaper(), ReducedMotion: true}
	st := NewState(kit.NewVirtualHeightCache())
	st.FollowOff()
	for _, k := range openKeys {
		st.SetExpanded(k, true)
	}
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
		Layout(env, gtx, st, items)
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

func colorDistanceSq(a, b color.NRGBA) int {
	dr := int(a.R) - int(b.R)
	dg := int(a.G) - int(b.G)
	db := int(a.B) - int(b.B)
	return dr*dr + dg*dg + db*db
}

func countWarmTintPixels(img *image.RGBA) int {
	count := 0
	for i := 0; i < len(img.Pix); i += 4 {
		r, g, b := int(img.Pix[i]), int(img.Pix[i+1]), int(img.Pix[i+2])
		if r-b >= 8 && r > g {
			count++
		}
	}
	return count
}

func countNonBgPixels(img *image.RGBA, bg color.NRGBA) int {
	count := 0
	for i := 0; i < len(img.Pix); i += 4 {
		c := color.NRGBA{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}
		if colorDistanceSq(c, bg) > 9 {
			count++
		}
	}
	return count
}

// TestFailedToolCallChangesTheGroundAndDoneHasNoColour verifies DA-E12 / §5.4:
// a succeeded tool call draws directly on CanvasBg with zero colored ground,
// while a failed tool call draws DangerSoft as the row ground plus reason line.
func TestFailedToolCallChangesTheGroundAndDoneHasNoColour(t *testing.T) {
	t.Parallel()
	const winW, winH = 680, 140
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)

	doneItem := makeWorkToolItem("done-1", 1, "", "read_file", statusDone, `{"file_path":"README.md"}`, 0)
	doneImg := renderItemsPixels(t, win, image.Pt(winW, winH), &th, []*session.Item{doneItem})
	if got := countWarmTintPixels(doneImg); got != 0 {
		t.Fatalf("done tool_call drew %d tinted ground pixels, want 0", got)
	}

	failItem := makeWorkToolItem("fail-1", 1, "", "bash", statusFailed, `{"command":"rm /protected"}`, 1)
	failItem.View.Text = "permission denied"
	failImg := renderItemsPixels(t, win, image.Pt(winW, winH), &th, []*session.Item{failItem})
	if got := countWarmTintPixels(failImg); got < 2000 {
		t.Fatalf("failed tool_call drew only %d DangerSoft ground pixels, want colored ground", got)
	}
}

// TestAssistantMessageHasNoBubble verifies DA-E12 / §5.4: assistant messages
// draw markdown as a document page directly on CanvasBg with no bubble or border,
// whereas user messages draw a right-aligned FillSoft pill with origin labels.
func TestAssistantMessageHasNoBubble(t *testing.T) {
	t.Parallel()
	const winW, winH = 820, 180
	win, err := glwin.Open(winW, winH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	shaper, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	th := theme.Light(shaper)
	bg := th.Chrome().CanvasBg

	asst := message("asst-1", 1, "Hi.")
	asstImg := renderItemsPixels(t, win, image.Pt(winW, winH), &th, []*session.Item{asst})
	if got := countNonBgPixels(asstImg, bg); got > 400 {
		t.Fatalf("assistant_message painted %d non-CanvasBg pixels for tiny text, want no bubble or border", got)
	}

	usr := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "usr-1", Kind: "user_message", Sequence: 1, Revision: 1,
		TurnOrigin: "lead",
		Metadata:   client.TranscriptMetadata{Origin: "lead"},
		View:       client.TranscriptView{Text: "Run tests"},
	}}
	usrImg := renderItemsPixels(t, win, image.Pt(winW, winH), &th, []*session.Item{usr})
	if got := countNonBgPixels(usrImg, bg); got < 2500 {
		t.Fatalf("user_message drew only %d non-CanvasBg pixels, want right-aligned FillSoft pill", got)
	}
	if got := originLabelFor(usr.Metadata.Origin); got != "From lead" {
		t.Fatalf("originLabelFor(lead) = %q, want %q", got, "From lead")
	}
	if got := originLabelFor("fugue"); got != "Fugue" {
		t.Fatalf("originLabelFor(fugue) = %q, want %q", got, "Fugue")
	}
}

// TestTranscriptItemKindsFormatting covers reasoning labels, commit card parsing,
// plan step states, and file_change path extraction.
func TestTranscriptItemKindsFormatting(t *testing.T) {
	t.Parallel()
	r := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "r1", Kind: kindReasoning,
		Metadata: client.TranscriptMetadata{DurationMillis: 6000},
	}}
	if got := reasoningLabel(r); got != "Thought for 6s" {
		t.Fatalf("reasoningLabel(6s) = %q", got)
	}
	rNoDur := &session.Item{TranscriptItem: client.TranscriptItem{ItemID: "r2", Kind: kindReasoning}}
	if got := reasoningLabel(rNoDur); got != "Thought" {
		t.Fatalf("finished reasoningLabel(0) = %q, never Thinking…", got)
	}

	commitItem := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "c1", Kind: kindToolCall,
		Metadata: client.TranscriptMetadata{Tool: "bash", Status: statusDone},
		View:     client.TranscriptView{Text: "[feat/f10.4c 24cd741] feat(ui): quota line row"},
	}}
	eff, ok := detectCommitEffect(commitItem)
	if !ok || eff.Hash != "24cd741" || eff.Message != "feat(ui): quota line row" {
		t.Fatalf("detectCommitEffect = (%+v, %v)", eff, ok)
	}

	planItem := &session.Item{TranscriptItem: client.TranscriptItem{
		ItemID: "p1", Kind: "plan",
		Metadata: client.TranscriptMetadata{CurrentStep: 2},
		View:     client.TranscriptView{Steps: []string{"Inspect files", "Edit code", "Verify tests"}},
	}}
	steps := planSteps(planItem)
	if s1, l1 := parsePlanStep(steps[0], 1, 2); s1 != statusDone || l1 != "Inspect files" {
		t.Fatalf("step 1 = (%q, %q)", s1, l1)
	}
	if s2, _ := parsePlanStep(steps[1], 2, 2); s2 != statusRunning {
		t.Fatalf("step 2 = %q, want running", s2)
	}
	if s3, _ := parsePlanStep(steps[2], 3, 2); s3 != statusPending {
		t.Fatalf("step 3 = %q, want pending", s3)
	}
}
