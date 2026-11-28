package appwin

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/folderpicker"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The typed host answers and hashes the chain tests script; static, so a
// failure names its fixture.
var errNoChooser = errors.New("no chooser is available")

const (
	testCreatedHash  = "hash-created"
	testAfterAPIHash = "hash-after-api"
	testAfterWebHash = "hash-after-web"
	testAfterOneHash = "hash-after-one"
)

// The FUG-115 source-folder dialog tests: the create dialog chains the
// create and one AddRoot per row on the hashes each step returns, the
// add-folders dialog chains from the detail's hash, a failure keeps the
// dialog open with the error on its row and a retry resumes from that row,
// duplicate paths are refused inline, a cancelled pick changes nothing, the
// typed-path fallback adds the row, and field errors appear only after a
// submit attempt.

// TestCreateDialogChainsAddRootsWithReturnedHashes pins the new-workspace
// flow: create first, then one AddRoot per row in list order, each with the
// hash the previous step returned, and the dialog closes when the last
// root lands.
func TestCreateDialogChainsAddRootsWithReturnedHashes(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{
		createRes: client.Workspace{ID: "w9", ConfigHash: testCreatedHash},
		addRes: []client.Workspace{
			{ID: "w9", ConfigHash: testAfterAPIHash},
			{ID: "w9", ConfigHash: testAfterWebHash},
		},
	}
	startTestMutator(t, sc, rec)
	sc.dlg.create.open(sc)
	sc.dlg.create.name.SetText("fsap")
	sc.dlg.create.folders.addPath("/repo/api")
	sc.dlg.create.folders.addPath("/repo/web")
	sc.dlg.create.submit(sc)

	res := waitMutResult(t, sc.wsMut)
	if res.Mutation.Kind != workspace.MutCreate {
		t.Fatalf("first post = %+v, want the create", res.Mutation)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})

	res = waitMutResult(t, sc.wsMut)
	if call := res.Mutation; call.Kind != workspace.MutAddRoot || call.WorkspaceID != "w9" ||
		call.Name != "api" || call.Path != "/repo/api" || call.Access != "rw" || call.ExpectedHash != testCreatedHash {
		t.Fatalf("second post = %+v, want the first row's add with the created hash", call)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})

	res = waitMutResult(t, sc.wsMut)
	if call := res.Mutation; call.Path != "/repo/web" || call.ExpectedHash != testAfterAPIHash {
		t.Fatalf("third post = %+v, want the second row's add with the hash the first add returned", call)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})

	if sc.dlg.create.dialog.IsOpen() {
		t.Fatal("the create dialog outlived its success")
	}
	if len(rec.calls) != 3 {
		t.Fatalf("posts = %+v, want create then two adds", rec.calls)
	}
}

// TestFoldersRetryResumesFromTheFailingRow pins the partway failure: the
// workspace and the roots already added stay, the dialog stays open with the
// host's typed error on the failing row, and a retry posts that row alone,
// with the hash the add before it returned.
func TestFoldersRetryResumesFromTheFailingRow(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{
		createRes: client.Workspace{ID: "w9", ConfigHash: testCreatedHash},
		addRes:    []client.Workspace{{ID: "w9", ConfigHash: testAfterAPIHash}},
		addErrs:   []error{nil, errRootInvalid},
	}
	startTestMutator(t, sc, rec)
	sc.dlg.create.open(sc)
	sc.dlg.create.name.SetText("fsap")
	sc.dlg.create.folders.addPath("/repo/api")
	sc.dlg.create.folders.addPath("/repo/web")
	sc.dlg.create.submit(sc)

	res := waitMutResult(t, sc.wsMut)
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	res = waitMutResult(t, sc.wsMut)
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	res = waitMutResult(t, sc.wsMut)
	if res.Err == nil {
		t.Fatal("the scripted second add did not fail")
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})

	if !sc.dlg.create.dialog.IsOpen() {
		t.Fatal("the create dialog closed on a partway failure")
	}
	if got := sc.dlg.create.folders.rows[1].err; got == "" {
		t.Fatal("the failing row shows no error")
	}
	if len(rec.calls) != 3 {
		t.Fatalf("posts after the failure = %+v, want create, add, failed add", rec.calls)
	}

	sc.dlg.create.submit(sc)
	res = waitMutResult(t, sc.wsMut)
	if call := res.Mutation; call.Kind != workspace.MutAddRoot || call.Path != "/repo/web" ||
		call.ExpectedHash != testAfterAPIHash {
		t.Fatalf("retry post = %+v, want only the failing row with the hash the first add returned", call)
	}
	if len(rec.calls) != 4 {
		t.Fatalf("posts after the retry = %+v, want the failed row retried alone", rec.calls)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.create.dialog.IsOpen() {
		t.Fatal("the create dialog outlived the retry's success")
	}
}

// TestFoldersRemoveFailingRowFinishesTheChain pins the other repair path:
// removing the failing row and retrying finishes the chain from the row that
// took its place.
func TestFoldersRemoveFailingRowFinishesTheChain(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{
		createRes: client.Workspace{ID: "w9", ConfigHash: testCreatedHash},
		addRes:    []client.Workspace{{ID: "w9", ConfigHash: testAfterAPIHash}},
		addErrs:   []error{nil, errRootInvalid},
	}
	startTestMutator(t, sc, rec)
	sc.dlg.create.open(sc)
	sc.dlg.create.name.SetText("fsap")
	sc.dlg.create.folders.addPath("/repo/api")
	sc.dlg.create.folders.addPath("/repo/web")
	sc.dlg.create.submit(sc)
	for range 3 {
		res := waitMutResult(t, sc.wsMut)
		sc.update(nil, nil, nil, []workspace.MutResult{res})
	}

	sc.dlg.create.folders.remove(1)
	sc.dlg.create.submit(sc)
	select {
	case res := <-sc.wsMut.Results():
		t.Fatalf("removing the last failing row still posted %+v", res.Mutation)
	default:
	}
	if sc.dlg.create.dialog.IsOpen() {
		t.Fatal("the create dialog stayed open with no rows left to add")
	}
}

// TestAddFoldersChainsFromTheDetailHash pins the add-folders dialog: the
// chain starts at the workspace detail's hash, the rows are added in order,
// a partway failure keeps the dialog open with the error on its row, and a
// retry resumes from that row and closes when the last root lands.
func TestAddFoldersChainsFromTheDetailHash(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	rec := &recordingMutatorClient{
		addRes: []client.Workspace{{ID: "w1", ConfigHash: testAfterOneHash}},
		addErrs: []error{
			nil,
			errRootInvalid,
			nil,
		},
	}
	startTestMutator(t, sc, rec)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)

	sc.dlg.root.openAdd(sc, det)
	// The test screens carry no picker, so the fallback field opened; force
	// the picker's normal look, which the fallback tests cover separately.
	sc.dlg.root.folders.noPicker = false
	sc.dlg.root.folders.typedOpen = false
	sc.dlg.root.folders.addPath("/repo/one")
	sc.dlg.root.folders.addPath("/repo/two")
	sc.dlg.root.submit(sc)

	res := waitMutResult(t, sc.wsMut)
	if call := res.Mutation; call.Kind != workspace.MutAddRoot || call.WorkspaceID != "w1" ||
		call.Path != "/repo/one" || call.ExpectedHash != det.ConfigHash {
		t.Fatalf("first add = %+v, want the detail's hash", call)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})

	res = waitMutResult(t, sc.wsMut)
	if res.Err == nil {
		t.Fatal("the scripted second add did not fail")
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if !sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the add-folders dialog closed on a partway failure")
	}
	if got := sc.dlg.root.folders.rows[1].err; got == "" {
		t.Fatal("the failing row shows no error")
	}

	sc.dlg.root.submit(sc)
	res = waitMutResult(t, sc.wsMut)
	if call := res.Mutation; call.Path != "/repo/two" || call.ExpectedHash != testAfterOneHash {
		t.Fatalf("retry add = %+v, want the failing row with the hash the first add returned", call)
	}
	sc.update(nil, nil, nil, []workspace.MutResult{res})
	if sc.dlg.root.dialog.IsOpen() {
		t.Fatal("the add-folders dialog outlived its success")
	}
}

// TestSourceFoldersDefaultNameTakesAUniqueSuffix pins the default root name:
// the folder's base name, suffixed with a number when the workspace already
// holds it, including against a root the dialog's workspace has.
func TestSourceFoldersDefaultNameTakesAUniqueSuffix(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.dlg.create.open(sc)
	f := &sc.dlg.create.folders
	f.addPath("/repo/api")
	f.addPath("/srv/api")
	if len(f.rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(f.rows))
	}
	if got := f.rows[0].name.Text(); got != "api" {
		t.Fatalf("first default name = %q, want api", got)
	}
	if got := f.rows[1].name.Text(); got != "api-2" {
		t.Fatalf("second default name = %q, want api-2", got)
	}

	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)
	sc.dlg.root.openAdd(sc, det)
	if got := sc.dlg.root.folders.uniqueName("main"); got != "main-2" {
		t.Fatalf("name against the workspace's root = %q, want main-2", got)
	}
}

// TestSourceFoldersRefuseDuplicatePaths pins the inline refusal: a path
// already in the list, or already a root of the workspace, is refused and no
// row is appended; the refusal clears when another folder is added.
func TestSourceFoldersRefuseDuplicatePaths(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)

	sc.dlg.root.openAdd(sc, det)
	f := &sc.dlg.root.folders
	f.addPath("/repo/x")
	f.addPath("/repo/x")
	if len(f.rows) != 1 || f.refused == "" {
		t.Fatalf("a repeated path left %d rows and refusal %q, want one row and a refusal", len(f.rows), f.refused)
	}
	f.addPath(det.Roots[0].Path)
	if len(f.rows) != 1 || f.refused == "" {
		t.Fatalf("a path that is already a root left %d rows and refusal %q, want one row and a refusal", len(f.rows), f.refused)
	}
	f.addPath("/repo/y")
	if len(f.rows) != 2 || f.refused != "" {
		t.Fatalf("after a fresh add the list holds %d rows and refusal %q, want two rows and no refusal", len(f.rows), f.refused)
	}
}

// TestSourceFolderPickResultsLandOnTheList pins the picker's frame-loop
// contract: a request runs off the dialog and appends nothing, a cancelled
// pick changes nothing, a failed pick is refused inline, and a chosen folder
// becomes a row with its base name.
func TestSourceFolderPickResultsLandOnTheList(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.folderPicker = folderpicker.NewPicker()
	sc.dlg.create.open(sc)
	f := &sc.dlg.create.folders

	f.pick()
	if !f.picking {
		t.Fatal("the pick did not start")
	}
	if len(f.rows) != 0 {
		t.Fatalf("rows while picking = %d, want none before the result", len(f.rows))
	}

	f.picked(folderpicker.Result{Cancelled: true})
	if len(f.rows) != 0 || f.refused != "" || f.picking {
		t.Fatalf("a cancelled pick left %d rows, refusal %q, picking %v; want a no-op", len(f.rows), f.refused, f.picking)
	}

	f.picked(folderpicker.Result{Path: "/home/u/notes"})
	if len(f.rows) != 1 || f.rows[0].path != "/home/u/notes" || f.rows[0].name.Text() != "notes" {
		t.Fatalf("rows after the pick = %+v, want one notes row", f.rows)
	}

	f.picked(folderpicker.Result{Err: errNoChooser})
	if f.refused == "" {
		t.Fatal("a failed pick is not refused inline")
	}
}

// TestTypedPathFallbackAddsTheRow pins the typed fallback: the path field is
// hidden until the link reveals it, its error appears only after a submit
// attempt, and a typed path adds the row and clears the field.
func TestTypedPathFallbackAddsTheRow(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.dlg.create.open(sc)
	f := &sc.dlg.create.folders
	f.noPicker = false
	f.typedOpen = false

	if f.typedOpen {
		t.Fatal("the path field is open before the link reveals it")
	}
	f.addTyped()
	if f.typedErr != "" || len(f.rows) != 0 {
		t.Fatalf("an add attempt on the closed field shows %q, want no error and no row", f.typedErr)
	}

	f.revealTyped()
	if !f.typedOpen {
		t.Fatal("the link did not reveal the path field")
	}
	f.addTyped()
	if f.typedErr == "" || len(f.rows) != 0 {
		t.Fatalf("an empty typed path shows %q after the attempt, want an error and no row", f.typedErr)
	}

	f.typed.SetText("/repo/typed")
	f.addTyped()
	if len(f.rows) != 1 || f.rows[0].path != "/repo/typed" || f.typed.Text() != "" || f.typedErr != "" {
		t.Fatalf("rows = %+v, field %q, error %q; want the typed row and a cleared field", f.rows, f.typed.Text(), f.typedErr)
	}
}

// TestFallbackFieldOpensWhenNoPickerExists pins that a desktop with no
// picker opens the dialog with the path field already shown: the unstarted
// test picker has probed nothing, so no helper is available.
func TestFallbackFieldOpensWhenNoPickerExists(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	sc.dlg.create.open(sc)
	if !sc.dlg.create.folders.typedOpen {
		t.Fatal("the fallback path field stayed closed although no picker is available")
	}
}

// TestManageMenuNamesAddFolders pins the renamed menu item.
func TestManageMenuNamesAddFolders(t *testing.T) {
	t.Parallel()
	sc, _ := newTestScreens(t)
	feedSnapshot(sc, topicSnapshot(t, []workspace.Summary{{ID: "w1", Name: "A"}}))
	det := wsTestDetail(nil)
	sc.workspaces.ApplyDetail(det)
	ws, _ := sc.workspaces.Lookup("w1")

	for _, item := range sc.manageItems(ws) {
		if item.ID == "add-folders" {
			if item.Label != "Add folders…" {
				t.Fatalf("add item label = %q, want Add folders…", item.Label)
			}
			return
		}
	}
	t.Fatal("the manage menu has no add-folders item")
}
