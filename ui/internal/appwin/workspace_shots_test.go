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
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const (
	shotWindowW = 860
	shotWindowH = 560
	// shotSidebarW is the sidebar scene's width: at 125% the shell must
	// still hold rail, sidebar and the main slot's minimum (808 dp, about
	// 1010 px plus the window insets), which the standard width cannot.
	shotSidebarW    = 1100
	shotSidebar     = "sidebar"
	shotRootDialog  = "root-dialog"
	shotPolicyCard  = "policy-card"
	shotCreateEmpty = "create-dialog"
	shotCreateRows  = "create-folders"
	shotCreateError = "create-row-error"
	shotAddFolders  = "add-folders-dialog"
)

type shotSpec struct {
	name    string
	scene   string
	light   bool
	uiScale float32
}

func workspaceShotSpecs() []shotSpec {
	scenes := []string{shotSidebar, shotRootDialog, shotPolicyCard, shotCreateEmpty, shotCreateRows, shotCreateError, shotAddFolders}
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
			{Key: "session.teams_auto_approve", OldValue: testFalse, NewValue: testTrue},
		},
	}
	sc.workspaces.ApplyDetail(det)
	sc.workspaces.ApplyDetail(&workspace.Detail{
		ID: "w2", Name: "docs", ConfigHash: "hash-docs", ConfigRevision: 1,
		Roots: []workspace.Root{{Name: testMainRoot, Path: "/srv/repos/docs", Access: "rw"}},
	})
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{
		{ID: "s1", Vendor: client.VendorClaude, State: stateRunning, WorkspaceID: "w1"},
		{ID: "s2", Vendor: client.VendorClaude, State: stateReady, WorkspaceID: "w1"},
		{ID: "s3", Vendor: client.VendorCodex, State: stateDetached, WorkspaceID: "w2"},
	}})
	sc.openSession("s1")
	sc.transcripts["s1"].ApplyNewestPage([]client.TranscriptItem{
		{ItemID: "i1", Kind: shotUserMessage, Sequence: 1, Revision: 1, View: client.TranscriptView{Text: "Add the workspace root and confirm policy."}},
		{ItemID: "i2", Kind: shotAssistantMessage, Sequence: 2, Revision: 1, View: client.TranscriptView{Text: "Root `api` is configured; awaiting confirmation on the policy diff."}},
	})

	switch scene {
	case shotRootDialog:
		sc.dlg.root.openFor(det, &det.Roots[0])
		sc.dlg.root.name.SetText("kong")
		sc.dlg.root.path.SetText("/srv/repos/kong")
	case shotPolicyCard:
		sc.dlg.policy.openFor(det)
	case shotCreateEmpty:
		openCreateShot(sc)
	case shotCreateRows:
		openCreateShot(sc)
		sc.dlg.create.folders.addPath("/srv/repos/api")
		sc.dlg.create.folders.addPath("/srv/web")
	case shotCreateError:
		openCreateShot(sc)
		sc.dlg.create.folders.addPath("/srv/repos/api")
		sc.dlg.create.folders.addPath("/srv/web")
		sc.dlg.create.folders.rows[1].err = "the path sits inside another root"
	case shotAddFolders:
		sc.dlg.root.openAdd(sc, det)
		sc.dlg.root.folders.noPicker = false
		sc.dlg.root.folders.typedOpen = false
		sc.dlg.root.folders.addPath("/srv/repos/kong")
	}
	return sc
}

// openCreateShot opens the create dialog with its name and, unlike a desktop
// without a picker, the fallback field closed: the shots show the picker's
// normal look (FUG-115).
func openCreateShot(sc *screens) {
	sc.dlg.create.open(sc)
	sc.dlg.create.name.SetText("fsap")
	sc.dlg.create.folders.noPicker = false
	sc.dlg.create.folders.typedOpen = false
}

func renderWorkspaceShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	size := image.Pt(shotWindowW, shotWindowH)
	if spec.scene == shotSidebar {
		size = image.Pt(shotSidebarW, shotWindowH)
		wide, err := glwin.Open(size.X, size.Y)
		if err != nil {
			t.Fatalf("open headless window: %v", err)
		}
		win = wide
	}
	return renderAppShotSized(t, win, spec, buildShotScreens(t, spec.scene), size)
}

func renderAppShot(t *testing.T, win *headless.Window, spec shotSpec, sc *screens) *image.RGBA {
	t.Helper()
	return renderAppShotSized(t, win, spec, sc, image.Pt(shotWindowW, shotWindowH))
}

func renderAppShotSized(t *testing.T, win *headless.Window, spec shotSpec, sc *screens, size image.Point) *image.RGBA {
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
	injectShotAppearance(sc, spec.light)
	img := renderShotFrames(t, win, env, sc, size, spec.name)
	if sc.lastErr != "" {
		t.Fatalf("shot %s recorded screen error: %s", spec.name, sc.lastErr)
	}
	return img
}

func renderShotFrames(t *testing.T, win *headless.Window, env *kit.Env, sc *screens, size image.Point, name string) *image.RGBA {
	t.Helper()
	injectShotAppearance(sc, env.Th == nil || env.Th.Chrome().CanvasBg.R >= 128)
	router := new(input.Router)
	var ops op.Ops
	now := time.Unix(1_700_000_000, 0)
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
			t.Fatalf("render shot %s: %v", name, err)
		}
		router.Frame(&ops)
		now = now.Add(time.Second / 60)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	if err := win.Screenshot(img); err != nil {
		t.Fatalf("screenshot %s: %v", name, err)
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
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotSidebar, shotRootDialog, shotPolicyCard, shotCreateEmpty, shotCreateRows, shotCreateError, shotAddFolders} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-light-ui125"]) {
			t.Fatalf("%s 100%% and 125%% shots are identical", scene)
		}
	}
}
