package appwin

import (
	"image"
	"image/color"
	"testing"
	"time"

	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotWindowW    = 860
	shotWindowH    = 560
	shotSidebar    = "sidebar"
	shotRootDialog = "root-dialog"
	shotPolicyCard = "policy-card"
)

type shotSpec struct {
	name    string
	scene   string
	light   bool
	uiScale float32
}

func workspaceShotSpecs() []shotSpec {
	scenes := []string{shotSidebar, shotRootDialog, shotPolicyCard}
	modes := []struct {
		label   string
		light   bool
		uiScale float32
	}{
		{"light-ui100", true, 1.0},
		{"dark-ui100", false, 1.0},
		{"light-ui125", true, 1.25},
		{"dark-ui125", false, 1.25},
	}
	specs := make([]shotSpec, 0, len(scenes)*len(modes))
	for _, s := range scenes {
		for _, m := range modes {
			specs = append(specs, shotSpec{
				name:    s + "-" + m.label,
				scene:   s,
				light:   m.light,
				uiScale: m.uiScale,
			})
		}
	}
	return specs
}

func buildShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	sc, _ := newTestScreens(t)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{
		{ID: "w1", Name: "fsap", ConfigRevision: 2, PendingPolicyConfirmation: true},
		{ID: "w2", Name: "docs", ConfigRevision: 1},
	}))
	det := &workspace.Detail{
		ID:             "w1",
		Name:           "fsap",
		ConfigHash:     "hash-fsap",
		ConfigRevision: 2,
		Roots:          []workspace.Root{{Name: testRootAPI, Path: "/srv/repos/fsap", Access: "rw"}},
		Pending:        true,
		Diff: []workspace.DiffEntry{
			{Key: testPolicyKey, OldValue: testApprovalAsk, NewValue: testApprovalFull},
			{Key: "session.teams_auto_approve", OldValue: "false", NewValue: "true"},
		},
	}
	sc.workspaces.ApplyDetail(det)
	sc.workspaces.ApplyDetail(&workspace.Detail{
		ID: "w2", Name: "docs", ConfigHash: "hash-docs", ConfigRevision: 1,
		Roots: []workspace.Root{{Name: testMainRoot, Path: "/srv/repos/docs", Access: "rw"}},
	})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "s1", Vendor: client.VendorClaude, State: "running", WorkspaceID: "w1"},
		{ID: "s2", Vendor: client.VendorClaude, State: "ready", WorkspaceID: "w1"},
		{ID: "s3", Vendor: client.VendorCodex, State: "detached", WorkspaceID: "w2"},
	}})
	sc.openSession("s1")
	sc.transcripts["s1"].ApplyNewestPage([]client.TranscriptItem{
		{ItemID: "i1", Kind: "user_message", Sequence: 1, Revision: 1, View: client.TranscriptView{Text: "Add the workspace root and confirm policy."}},
		{ItemID: "i2", Kind: "assistant_message", Sequence: 2, Revision: 1, View: client.TranscriptView{Text: "Root `api` is configured; awaiting confirmation on the policy diff."}},
	})

	switch scene {
	case shotRootDialog:
		sc.dlg.root.openAdd(det)
		sc.dlg.root.name.SetText("kong")
		sc.dlg.root.path.SetText("/srv/repos/kong")
	case shotPolicyCard:
		sc.dlg.policy.openFor(det)
	}
	return sc
}

func renderWorkspaceShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatalf("build shaper: %v", err)
	}
	th := theme.Dark(sh)
	if spec.light {
		th = theme.Light(sh)
	}
	env := &kit.Env{Th: &th, Sh: sh, UIScale: spec.uiScale, ReducedMotion: true}
	sc := buildShotScreens(t, spec.scene)
	router := new(input.Router)
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
	size := image.Pt(shotWindowW, shotWindowH)
	for range 3 {
		ops.Reset()
		device := unit.Metric{PxPerDp: 1, PxPerSp: 1}
		gtx := layout.Context{
			Ops:         &ops,
			Metric:      env.Metric(device),
			Constraints: layout.Exact(size),
			Source:      router.Source(),
			Now:         now,
		}
		env.Begin(now)
		env.TrackInput(&gtx)
		drawWindow(&gtx, env, sc)
		env.FrameEnd(&gtx)
		if err := win.Frame(&ops); err != nil {
			t.Fatalf("render shot %s: %v", spec.name, err)
		}
		router.Frame(&ops)
		now = now.Add(time.Second / 60)
	}
	if sc.lastErr != "" {
		t.Fatalf("shot %s recorded screen error: %s", spec.name, sc.lastErr)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", spec.name, err)
	}
	return img
}

func imagesDifferRGBA(a, b *image.RGBA) bool {
	if a.Bounds() != b.Bounds() {
		return true
	}
	for i := range a.Pix {
		if a.Pix[i] != b.Pix[i] {
			return true
		}
	}
	return false
}

func isUniformRGBA(img *image.RGBA) bool {
	if len(img.Pix) < 4 {
		return true
	}
	first := color.NRGBA{R: img.Pix[0], G: img.Pix[1], B: img.Pix[2], A: img.Pix[3]}
	for i := 4; i < len(img.Pix); i += 4 {
		c := color.NRGBA{R: img.Pix[i], G: img.Pix[i+1], B: img.Pix[i+2], A: img.Pix[i+3]}
		if c != first {
			return false
		}
	}
	return true
}

// TestWorkspaceShotsRender renders the F10.14 shots (sidebar, root dialog,
// policy card × light/dark/125%) headlessly and verifies every shot renders
// non-uniform content with distinct light, dark and 125% outputs.
func TestWorkspaceShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range workspaceShotSpecs() {
		img := renderWorkspaceShot(t, win, spec)
		if isUniformRGBA(img) {
			t.Fatalf("shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotSidebar, shotRootDialog, shotPolicyCard} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-light-ui125"]) {
			t.Fatalf("%s 100%% and 125%% shots are identical", scene)
		}
	}
}
