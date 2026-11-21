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
			ItemID: "i-diff", Kind: "file_change", Sequence: 2, Revision: 1,
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
	items := []client.TranscriptItem{
		{
			ItemID: "i-u1", Kind: "user_message", Sequence: 1, Revision: 1,
			TurnOrigin: "lead", Metadata: client.TranscriptMetadata{Origin: "lead"},
			View: client.TranscriptView{Text: "Update ui/internal/mdview/codeblock.go:124 and check commit 24cd741."},
		},
		{
			ItemID: "i-r1", Kind: "reasoning", Sequence: 2, Revision: 1,
			Metadata: client.TranscriptMetadata{DurationMillis: 6000},
			View:     client.TranscriptView{Text: "Checking existing layout measurements."},
		},
		shotToolCall("i-w1", "t1", "read_file", "succeeded", `{"file_path":"ui/internal/mdview/codeblock.go"}`, "", 3, nil),
		shotToolCall("i-w2", "t1", "edit_file", "succeeded", `{"file_path":"ui/internal/mdview/diff.go"}`, "", 4, nil),
		{
			ItemID: "i-w3", TurnID: "t1", Kind: "subagent", Sequence: 5, Revision: 1,
			Metadata: client.TranscriptMetadata{Status: "succeeded"},
			View:     client.TranscriptView{Title: "Verify lint clean"},
		},
		{
			ItemID: "i-w4", TurnID: "t1", Kind: "command", Sequence: 6, Revision: 1,
			Metadata: client.TranscriptMetadata{Source: "git", Status: "succeeded"},
			View:     client.TranscriptView{Title: "/review"},
		},
		{
			ItemID: "i-a1", Kind: "assistant" + "_message", Sequence: 7, Revision: 1,
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
