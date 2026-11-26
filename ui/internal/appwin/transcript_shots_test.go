package appwin

import (
	"fmt"
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotTranscriptItems = "transcript-items"
	shotTranscriptCards = "transcript-cards"
	transcriptShotSess  = "ses_shot0001"
	shotScale90         = float32(0.9)
	shotScale100        = float32(1.0)
	shotScale125        = float32(1.25)
	shotKindFileChange  = "file_change"
)

func transcriptShotSpecs() []shotSpec {
	themes := []bool{true, false}
	scales := []float32{shotScale90, shotScale100, shotScale125}
	specs := make([]shotSpec, 0, len(themes)*len(scales)*2)
	for _, scene := range []string{shotTranscriptItems, shotTranscriptCards} {
		for _, scale := range scales {
			for _, light := range themes {
				mode := "dark"
				if light {
					mode = "light"
				}
				name := fmt.Sprintf("%s-%s-ui%03d", scene, mode, int(scale*100+0.5))
				specs = append(specs, shotSpec{name: name, scene: scene, light: light, uiScale: scale})
			}
		}
	}
	return specs
}

func shotToolCall(id, turnID, tool, status, input, text string, seq uint64, exit *int32) client.TranscriptItem {
	return client.TranscriptItem{
		ItemID: id, TurnID: turnID, Kind: "tool_call", Sequence: seq, Revision: 1,
		Metadata: client.TranscriptMetadata{Tool: tool, Status: status, ExitCode: exit, DurationMillis: 450},
		View:     client.TranscriptView{Input: input, Text: text},
	}
}

func transcriptShotFixtures(scene string) ([]client.TranscriptItem, []string) {
	if scene == shotTranscriptCards {
		return transcriptCardsScene()
	}
	return transcriptItemsScene()
}

func transcriptCardsScene() ([]client.TranscriptItem, []string) {
	exitOne := int32(1)
	items := []client.TranscriptItem{
		{
			ItemID: "i-plan", Kind: "plan", Sequence: 1, Revision: 1,
			Metadata: client.TranscriptMetadata{CurrentStep: 2},
			View:     client.TranscriptView{Steps: []string{"Inspect parser", "Update codeblock diff renderer", "Regenerate goldens"}},
		},
		{
			ItemID: "i-diff", Kind: shotKindFileChange, Sequence: 2, Revision: 1,
			Metadata: client.TranscriptMetadata{ChangeKind: "modify", LinesAdded: 2, LinesRemoved: 1},
			View: client.TranscriptView{
				Title: "ui/internal/mdview/diff.go",
				Text:  "@@ -12,3 +12,4 @@\n contextLine := true\n-oldCall()\n+newCall()\n+extraLine()\n",
			},
		},
		shotToolCall("i-out", "", "terminal", "failed", `{"command":"go test ./internal/mdview"}`, "FAIL: TestDiffLinesCarryAMarkerColumn\nexpected marker column width > 0", 3, &exitOne),
		shotToolCall("i-commit", "", "git_commit", "succeeded", `{"command":"git commit -m 'feat(ui): diff column'"}`, "[feat/f10.4c 24cd741] feat(ui): diff column", 4, nil),
	}
	return items, []string{"i-diff", "i-out", "i-commit"}
}

func transcriptItemsScene() ([]client.TranscriptItem, []string) {
	exitZero := int32(0)
	items := []client.TranscriptItem{
		{
			ItemID: "i-u1", Kind: shotUserMessage, Sequence: 1, Revision: 1,
			TurnOrigin: proposalRoleLead, Metadata: client.TranscriptMetadata{Origin: proposalRoleLead},
			View: client.TranscriptView{Text: "Update ui/internal/mdview/codeblock.go:124, check commit 24cd741, and review #185."},
		},
		{
			ItemID: "i-r1", Kind: "reasoning", Sequence: 2, Revision: 1,
			Metadata: client.TranscriptMetadata{DurationMillis: 6000},
			View:     client.TranscriptView{Text: "Checking existing layout measurements."},
		},
		shotToolCall("i-w1", "t1", "read_file", "succeeded", `{"file_path":"ui/internal/mdview/codeblock.go","start_line":10,"end_line":40}`, "", 3, nil),
		{
			ItemID: "i-w2", TurnID: "t1", Kind: shotKindFileChange, Sequence: 4, Revision: 1,
			Metadata: client.TranscriptMetadata{ChangeKind: "modify", LinesAdded: 2, LinesRemoved: 1, DurationMillis: 90},
			View:     client.TranscriptView{Title: "ui/internal/mdview/diff.go", Text: "@@ -1,1 +1,1 @@\n-old\n+new\n"},
		},
		{
			ItemID: "i-w3", TurnID: "t1", Kind: shotKindFileChange, Sequence: 5, Revision: 1,
			Metadata: client.TranscriptMetadata{ChangeKind: "create", LinesAdded: 12, DurationMillis: 40},
			View:     client.TranscriptView{Title: "ui/internal/mdview/badges.go"},
		},
		{
			ItemID: "i-w4", TurnID: "t1", Kind: shotKindFileChange, Sequence: 6, Revision: 1,
			Metadata: client.TranscriptMetadata{ChangeKind: "delete", LinesRemoved: 8, DurationMillis: 30},
			View:     client.TranscriptView{Title: "ui/internal/mdview/legacy.go"},
		},
		shotToolCall("i-w5", "t1", "grep", "succeeded", `{"pattern":"PaintBadgeChrome","directory":"ui/internal/mdview"}`, "ui/internal/mdview/badges.go:97: func PaintBadgeChrome\n", 7, nil),
		shotToolCall("i-w6", "t1", "list_dir", "succeeded", `{"directory":"ui/internal/mdview"}`, "badges.go\ncodeblock.go\ndiff.go\n", 8, nil),
		shotToolCall("i-w7", "t1", "bash", "succeeded", `{"command":"go test ./internal/mdview"}`, "ok github.com/syurodev/fugue/ui/internal/mdview\n", 9, &exitZero),
		shotToolCall("i-w8", "t1", "fetch_url", "succeeded", `{"url":"https://github.com/syurodev/fugue/pull/185"}`, "Pull request details", 10, nil),
		shotToolCall("i-w9", "t1", "web_search", "succeeded", `{"query":"gio op.Defer overlay"}`, "https://pkg.go.dev/gioui.org/op\n", 11, nil),
		shotToolCall("i-w10", "t1", "mcp__github__get_pr", "succeeded", `{"number":185}`, `{"state":"open"}`, 12, nil),
		shotToolCall("i-w11", "t1", "git_commit", "succeeded", `{"command":"git commit -m 'polish'"}`, "[feat/f10.4c 24cd741] polish v0.10", 13, nil),
		shotToolCall("i-w12", "t1", "gh_pr", "succeeded", `{"command":"gh pr view 185"}`, "syurodev/fugue#185", 14, nil),
		shotToolCall("i-w13", "t1", "bash", "succeeded", `{"command":"git checkout feat/f10.4c-transcript-items"}`, "Switched to branch 'feat/f10.4c-transcript-items'", 15, &exitZero),
		{
			ItemID: "i-w14", TurnID: "t1", Kind: "subagent", Sequence: 16, Revision: 1,
			Metadata: client.TranscriptMetadata{Status: "succeeded"}, View: client.TranscriptView{Title: "Verify lint clean"},
		},
		shotToolCall("i-w15", "t1", "update_plan", "succeeded", `{"description":"Finish v0.10 row polish"}`, "", 17, nil),
		{
			ItemID: "i-ap", Kind: "approval", Sequence: 18, Revision: 1,
			Metadata: client.TranscriptMetadata{RequestKind: "command", Status: "pending"},
			View:     client.TranscriptView{Title: "go test -tags regenerate ./internal/appwin"},
		},
		{
			ItemID: "i-q1", Kind: "tool_call", Sequence: 19, Revision: 1,
			Metadata: client.TranscriptMetadata{Tool: "ask_user", RequestKind: client.InteractionKindQuestion, Status: "waiting"},
			View:     client.TranscriptView{Title: "Keep PR #185 in one piece?"},
		},
		{
			ItemID: "i-a1", Kind: "assistant" + "_message", Sequence: 20, Revision: 1,
			View: client.TranscriptView{Text: "Updated `ui/internal/mdview/codeblock.go:124` in commit 24cd741.\n\n```go\nfunc Render() int {\n\treturn 42\n}\n```"},
		},
	}
	return items, []string{"wg:i-w1"}
}

func buildTranscriptShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-ts-shot"})

	sess := client.Session{
		ID: transcriptShotSess, Vendor: client.VendorClaude,
		State: stateReady, CreatedAt: time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339),
	}
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{sess}})
	sc.open = transcriptShotSess
	// The grid, not sc.open, picks the main slot: without it the shot shows the
	// empty "Select a session" state instead of the transcript under test.
	sc.grid.openMain(SlotRef{Kind: KindSession, ID: transcriptShotSess})
	tr := session.NewTranscript(transcriptShotSess)
	items, expanded := transcriptShotFixtures(scene)
	tr.ApplyNewestPage(items)
	sc.transcripts[transcriptShotSess] = tr
	view := sc.viewFor(transcriptShotSess)
	view.BackToTop()
	for _, id := range expanded {
		view.SetExpanded(id, true)
	}
	return sc
}

func renderTranscriptShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	env := newAccountsTestEnv(t, spec.light, spec.uiScale)
	sc := buildTranscriptShotScreens(t, spec.scene)
	sc.osAppearance = newShotAppearanceSource(spec.light)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if sc.lastErr != "" {
		t.Fatalf("transcript shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	return img
}

// TestTranscriptItemShotsRender renders both DevKit transcript item scenes in
// light and dark modes at 90/100/125% UI scale and verifies non-blank, distinct output.
func TestTranscriptItemShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range transcriptShotSpecs() {
		img := renderTranscriptShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("transcript shot %s rendered uniform/blank image", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotTranscriptItems, shotTranscriptCards} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
		for _, pair := range [][2]int{{90, 100}, {100, 125}} {
			kA := fmt.Sprintf("%s-light-ui%03d", scene, pair[0])
			kB := fmt.Sprintf("%s-light-ui%03d", scene, pair[1])
			if !imagesDifferRGBA(rendered[kA], rendered[kB]) {
				t.Fatalf("%s %d%% and %d%% shots must differ", scene, pair[0], pair[1])
			}
		}
	}
}
