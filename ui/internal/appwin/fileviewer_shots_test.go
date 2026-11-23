package appwin

import (
	"fmt"
	"image"
	"strings"
	"testing"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/shottest"
)

const (
	shotViewerFile   = "viewer-file"
	shotViewerDiff   = "viewer-diff"
	shotViewerFolder = "viewer-folder"

	// The path the file and diff scenes open, relative to the root.
	shotViewerGoPath = "internal/viewer/page.go"
	shotViewerGoDir  = "internal/viewer"
)

func viewerShotSpecs() []shotSpec {
	return buildShotMatrix(shotViewerFile, shotViewerDiff, shotViewerFolder)
}

// buildViewerShotScreens fills the viewer with one folded read per scene so
// the shots draw settled content: the file view with its requested range
// tinted, the Edited row's full diff, and a folder listing.
func buildViewerShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	sc := viewerTestScreens(t)
	v := sc.viewer
	switch scene {
	case shotViewerFile:
		sc.transcriptCallbacks().OnOpenFile(mdview.FileRef{Path: shotViewerGoPath, Line: 96, EndLine: 103, Raw: shotViewerGoPath + ":96-103"})
		sc.applyFileViewResults([]fileResult{{Op: fileOpRead, Path: shotViewerGoPath, File: client.WorkspaceFile{
			Content: viewerShotGoBody(), TotalLines: 120, Language: "go",
		}}})
	case shotViewerDiff:
		sc.transcripts["s1"].ApplyNewestPage([]client.TranscriptItem{{
			ItemID: "fc-shot", Kind: "file_change", Sequence: 1, Revision: 1,
			View: client.TranscriptView{Title: shotViewerGoPath, Text: viewerShotDiffBody()},
		}})
		sc.openDiffViewer("fc-shot")
	case shotViewerFolder:
		sc.transcriptCallbacks().OnOpenFile(mdview.FileRef{Path: shotViewerGoDir, Raw: shotViewerGoDir, Folder: true})
		sc.applyFileViewResults([]fileResult{{Op: fileOpList, Path: shotViewerGoDir, Entries: []client.WorkspaceDirEntry{
			{Name: "badge.go", Kind: dirKindFile, Size: 4212},
			{Name: "page.go", Kind: dirKindFile, Size: 18144},
			{Name: "render.go", Kind: dirKindFile, Size: 9307},
			{Name: "state.go", Kind: dirKindFile, Size: 6650},
			{Name: "testdata", Kind: dirKindDir},
		}}})
	default:
		t.Fatalf("unknown viewer shot scene %q", scene)
	}
	if v.errMsg != "" {
		t.Fatalf("viewer shot scene %s recorded %q", scene, v.errMsg)
	}
	openVisibleSlot(sc, SlotRef{Kind: KindDiff, ID: sc.viewer.path})
	return sc
}

func viewerShotGoBody() string {
	rows := make([]string, 0, 120)
	for i := 1; i <= 120; i++ {
		rows = append(rows, fmt.Sprintf("func viewerRow%d(env *kit.Env, gtx *layout.Context) layout.Dimensions {", i))
		rows = append(rows, "\treturn layout.Dimensions{Size: gtx.Constraints.Max}")
		rows = append(rows, "}")
		if len(rows) >= 120 {
			break
		}
	}
	return strings.Join(rows[:120], "\n")
}

func viewerShotDiffBody() string {
	return strings.Join([]string{
		"--- a/internal/viewer/page.go",
		"+++ b/internal/viewer/page.go",
		"@@ -41,9 +41,12 @@ func (v *page) layout(",
		" \tch := env.Th.Chrome()",
		"-\trows := v.lines[:v.visible]",
		"+\trows, ok := v.visibleRows()",
		"+\tif !ok {",
		"+\t\treturn layout.Dimensions{}",
		"+\t}",
		" \tfor i, row := range rows {",
		"-\t\tdrawRow(gtx, row, i)",
		"+\t\tdrawRowTinted(gtx, row, i, ch)",
		" \t}",
		" \treturn dims",
	}, "\n")
}

func renderViewerShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	env := newAccountsTestEnv(t, spec.light, spec.uiScale)
	sc := buildViewerShotScreens(t, spec.scene)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), spec.name)
	if sc.lastErr != "" {
		t.Fatalf("viewer shot %s recorded error: %s", spec.name, sc.lastErr)
	}
	return img
}

// TestViewerShotsRender renders the F10.4e file, diff and folder viewer
// (light and dark at 90/100/125%) headlessly and checks that themes and
// scales produce distinct non-uniform images.
func TestViewerShotsRender(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range viewerShotSpecs() {
		img := renderViewerShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("viewer shot %s rendered uniform/blank image", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotViewerFile, shotViewerDiff, shotViewerFolder} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
		if !imagesDifferRGBA(rendered[scene+"-light-ui090"], rendered[scene+"-light-ui100"]) ||
			!imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-light-ui125"]) {
			t.Fatalf("%s 90%%/100%%/125%% shots must differ", scene)
		}
	}
}
