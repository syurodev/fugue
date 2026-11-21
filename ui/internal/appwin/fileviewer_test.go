package appwin

import (
	"errors"
	"fmt"
	"image"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/syurodev/fugue/pkg/client"

	hostv1 "github.com/syurodev/fugue/pkg/pb/fugue/host/v1"
	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/vault"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

const viewerTestWS = "ws-viewer"

var errViewerScripted = errors.New("scripted file view error")

// viewerTestScreens builds screens with one open session bound to a workspace
// whose single registered root is main.
func viewerTestScreens(t *testing.T) *screens {
	t.Helper()
	m := hostconn.NewManager(&hostconn.Config{})
	sc := newScreens(m, &fakeFetcher{}, session.NewPoster(m.Client), workspace.NewMutator(func() workspace.MutatorClient { return nil }))
	sc.vault.ApplyStatus(vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-viewer"})
	sc.open = "s1"
	sc.sidebar.ApplyList(client.SessionList{Sessions: []client.Session{{ID: "s1", Vendor: "claude", State: "running", WorkspaceID: viewerTestWS}}})
	sc.workspaces.ApplyList([]workspace.Summary{{ID: viewerTestWS, Name: "fugue"}})
	sc.workspaces.ApplyDetail(&workspace.Detail{ID: viewerTestWS, Name: "fugue", ConfigHash: "h", Roots: []workspace.Root{
		{Name: "main", Path: "/tmp/proj", Access: "rw"},
	}})
	sc.transcripts["s1"] = session.NewTranscript("s1")
	return sc
}

func fileBody(lines int) string {
	rows := make([]string, 0, lines)
	for i := 1; i <= lines; i++ {
		rows = append(rows, fmt.Sprintf("line %d of the sample file", i))
	}
	return strings.Join(rows, "\n")
}

func diffBody() string {
	return strings.Join([]string{
		"--- a/src/notes.md",
		"+++ b/src/notes.md",
		"@@ -1,3 +1,4 @@ line one",
		" line one",
		"-line two old",
		"+line two new",
		" line three",
		"+line four",
	}, "\n")
}

// viewerReadRefusal builds the error shape the client produces for a refused
// read carrying public code, so the viewer's candidate handling sees the
// real wire shape.
func viewerReadRefusal(code string) error {
	cerr := connect.NewError(connect.CodePermissionDenied, errViewerScripted)
	if d, derr := connect.NewErrorDetail(&hostv1.ErrorDetail{Code: code}); derr == nil {
		cerr.AddDetail(d)
	}
	return fmt.Errorf("read the workspace file: %w", &client.WorkspaceFileReadError{Cause: cerr})
}

// TestFileChipOpensTheFileAtTheLine proves the DA-E16 viewer part (F10.4e): a
// file chip carrying a line range opens the read-only viewer in the right
// pane, the host's read folds in, and the view lands on and highlights the
// requested range. The chip click itself fires OnOpenFile with exactly this
// reference, proven in transcriptview's badge-click test.
func TestFileChipOpensTheFileAtTheLine(t *testing.T) {
	t.Parallel()
	sc := viewerTestScreens(t)
	cb := sc.transcriptCallbacks()

	cb.OnOpenFile(mdview.FileRef{Path: "src/notes.md", Line: 10, EndLine: 19, Raw: "src/notes.md:10-19"})
	v := sc.viewer
	if !v.open || v.mode != fileViewFile || v.path != "src/notes.md" || v.line != 10 || v.endLine != 19 {
		t.Fatalf("viewer after chip click = (open %v, mode %d, path %q, line %d, end %d)", v.open, v.mode, v.path, v.line, v.endLine)
	}
	if !v.loading || len(v.pendingRoots) != 1 || v.pendingRoots[0] != "main" {
		t.Fatalf("viewer roots after chip click = (loading %v, roots %v)", v.loading, v.pendingRoots)
	}

	// An outside-root answer moves to the next root candidate, so a path
	// under another root still opens.
	v.pendingRoots = append(v.pendingRoots, "second")
	sc.applyFileViewResults([]fileResult{{Op: fileOpRead, Path: "src/notes.md", Err: viewerReadRefusal(client.WorkspaceCodePathOutsideRoot)}})
	if !v.loading || len(v.pendingRoots) != 1 || v.pendingRoots[0] != "second" || v.errMsg != "" {
		t.Fatalf("viewer after outside-root refusal = (loading %v, roots %v, msg %q)", v.loading, v.pendingRoots, v.errMsg)
	}

	sc.applyFileViewResults([]fileResult{{Op: fileOpRead, Path: "src/notes.md", File: client.WorkspaceFile{Content: fileBody(200), TotalLines: 200, Language: "markdown"}}})
	if v.file == nil || v.file.TotalLines != 200 || v.file.Language != "markdown" || v.errMsg != "" {
		t.Fatalf("viewer after read = (file %+v, msg %q)", v.file, v.errMsg)
	}
	if !v.scrollPending {
		t.Fatal("the viewer did not arm the jump to the requested line")
	}

	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	env := newAccountsTestEnv(t, true, 1.0)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), "viewer-file-evidence")
	if v.scrollPending {
		t.Fatal("the layout did not consume the line jump")
	}
	if v.code.ScrollY <= 0 {
		t.Fatalf("the viewer did not scroll to line 10 (ScrollY %d)", v.code.ScrollY)
	}
	if isUniformRGBA(img) {
		t.Fatal("the file viewer rendered a blank image")
	}
}

// TestEditedRowOpensTheFullDiff proves DA-E16 (F10.4e): clicking an Edited
// row's file name opens the full diff from the file_change item itself, with
// no file read.
func TestEditedRowOpensTheFullDiff(t *testing.T) {
	t.Parallel()
	sc := viewerTestScreens(t)
	sc.transcripts["s1"].ApplyNewestPage([]client.TranscriptItem{{
		ItemID: "fc1", Kind: "file_change", Sequence: 1, Revision: 1,
		View: client.TranscriptView{Title: "src/notes.md", Text: diffBody()},
	}})
	sc.openDiffViewer("fc1")
	v := sc.viewer
	if !v.open || v.mode != fileViewDiff || v.diffRaw != diffBody() || v.diffTitle != "notes.md" {
		t.Fatalf("viewer after Edited click = (open %v, mode %d, title %q, raw len %d)", v.open, v.mode, v.diffTitle, len(v.diffRaw))
	}
	if v.loading || v.file != nil {
		t.Fatalf("a diff opens with no file read (loading %v, file %+v)", v.loading, v.file)
	}
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	env := newAccountsTestEnv(t, true, 1.0)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), "viewer-diff-evidence")
	if isUniformRGBA(img) {
		t.Fatal("the diff viewer rendered a blank image")
	}
}

// TestFolderBadgeListsTheDirectory proves DA-E16 (F10.4e): a folder badge
// opens the directory listing as file and folder badges, and clicking an
// entry opens it in the viewer.
func TestFolderBadgeListsTheDirectory(t *testing.T) {
	t.Parallel()
	sc := viewerTestScreens(t)
	sc.transcriptCallbacks().OnOpenFile(mdview.FileRef{Path: "src", Raw: "src", Folder: true})
	v := sc.viewer
	if !v.open || v.mode != fileViewFolder || v.path != "src" {
		t.Fatalf("viewer after folder click = (open %v, mode %d, path %q)", v.open, v.mode, v.path)
	}
	sc.applyFileViewResults([]fileResult{{Op: fileOpList, Path: "src", Entries: []client.WorkspaceDirEntry{
		{Name: "notes.md", Kind: "file", Size: 42},
		{Name: "nested", Kind: "dir"},
	}}})
	if len(v.entries) != 2 || v.errMsg != "" {
		t.Fatalf("viewer after listing = (%d entries, msg %q)", len(v.entries), v.errMsg)
	}

	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	env := newAccountsTestEnv(t, true, 1.0)
	img := renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), "viewer-folder-evidence")
	if len(v.entries) != 2 || v.entryClicks == nil || v.entryClicks["notes.md"] == nil {
		t.Fatalf("the folder view did not render clickable badges (entries %d, clicks %v)", len(v.entries), v.entryClicks)
	}
	if isUniformRGBA(img) {
		t.Fatal("the folder viewer rendered a blank image")
	}

	// Clicking the file entry opens it through the same wiring.
	v.entryClicks["notes.md"].Click()
	img = renderShotFrames(t, win, env, sc, image.Pt(shotWindowW, shotWindowH), "viewer-folder-open")
	if v.mode != fileViewFile || v.path != "src/notes.md" || !v.loading {
		t.Fatalf("entry click did not open the file (mode %d, path %q, loading %v)", v.mode, v.path, v.loading)
	}
}
