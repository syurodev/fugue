package appwin

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The two source-folder dialogs (FUG-115): the new-workspace dialog, whose
// create turns its rows into roots one AddRoot at a time on the hash each
// step returns, and the workspace's Add folders dialog, whose chain starts
// at the detail's hash. An edit of an existing root keeps its typed form.
// Both ride the shared sourceFolders list; the chain state machine lives
// here, its rows and their drawing in sourcefolders*.go.

// createDialog is the new-workspace dialog (workspace §3, FUG-115): the name
// typed and validated by the host, and the source folders the create turns
// into roots — one AddRoot per row in list order, each a compare-and-swap on
// the hash the step before returned.
type createDialog struct {
	dialog         *kit.DialogState
	cancel, create *kit.ButtonState
	name           *kit.TextFieldState
	folders        sourceFolders
	// chain carries the create-then-add sequence: on marks a create the
	// host accepted with rows still to add, wsID and hash name the created
	// workspace and the hash the next row posts against, and idx is the
	// row that comes next — the failing one on a partway failure.
	chain struct {
		on         bool
		wsID, hash string
		idx        int
	}
	// busy marks a create or add post in flight, which blocks a repeat
	// submit; lastErr is the create failure the dialog keeps visible while
	// the typed name stays in the field.
	busy    bool
	lastErr string
}

func newCreateDialog() createDialog {
	return createDialog{
		dialog:  kit.NewDialogState(),
		cancel:  kit.NewButtonState(),
		create:  kit.NewButtonState(),
		name:    kit.NewTextFieldState(),
		folders: newSourceFolders(),
	}
}

// open opens the dialog with an empty name and an empty list, over the
// window's picker.
func (d *createDialog) open(sc *screens) {
	d.name.SetText("")
	d.lastErr = ""
	d.chain = struct {
		on         bool
		wsID, hash string
		idx        int
	}{}
	d.folders.onRemove = func(i int) { d.removed(i) }
	d.folders.setPicker(sc.folderPicker)
	d.folders.reset(nil)
	d.dialog.Open()
}

// removed drops a row while the chain waits on one: the row that took its
// place keeps the chain's index.
func (d *createDialog) removed(i int) {
	d.folders.remove(i)
	if d.chain.on && i < d.chain.idx {
		d.chain.idx--
	}
}

// applied folds a create or a chained add in. A failed create keeps the
// dialog open with the name, so the user edits it instead of retyping it; a
// failed add puts the host's typed answer on its row and waits for the
// retry; a finished chain closes the dialog.
func (d *createDialog) applied(sc *screens, res *workspace.MutResult) {
	d.busy = false
	switch {
	case res.Mutation.Kind == workspace.MutCreate && res.Err != nil:
		d.lastErr = res.Err.Error()
	case res.Mutation.Kind == workspace.MutCreate:
		d.chain.on, d.chain.wsID, d.chain.hash, d.chain.idx = true, res.NewID, res.NewHash, 0
		if len(d.folders.rows) == 0 {
			d.chain.on = false
			d.dialog.Close()
			return
		}
		d.postRow(sc)
	case res.Err != nil:
		d.folders.rows[d.chain.idx].err = rowErrorMessage(res.Err)
	default:
		d.folders.rows[d.chain.idx].err = ""
		d.chain.hash = res.NewHash
		d.chain.idx++
		if d.chain.idx >= len(d.folders.rows) {
			d.chain.on = false
			d.dialog.Close()
			return
		}
		d.postRow(sc)
	}
}

// postRow posts the next row's add against the hash the step before
// returned.
func (d *createDialog) postRow(sc *screens) {
	row := d.folders.rows[d.chain.idx]
	sc.wsMut.Do(&workspace.Mutation{
		Kind: workspace.MutAddRoot, WorkspaceID: d.chain.wsID,
		Name: strings.TrimSpace(row.name.Text()), Path: row.path,
		Access: row.accessSel, ExpectedHash: d.chain.hash,
	})
	d.busy = true
}

func (d *createDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogForm,
		Title:          "New workspace",
		Description:    "Pick the folders this workspace can use.",
		Cancel:         d.cancel,
		Primary:        d.create,
		PrimaryLabel:   "Create workspace",
		PrimaryVariant: kit.Primary,
		Dirty:          d.name.Text() != "" || d.folders.dirty(),
		Busy:           d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newWSBody(env, gtx, bands, sc.fail)
			body.field(d.name.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
				props := &kit.TextFieldProps{
					Label:       labelName,
					Placeholder: "for example fsap",
					Error:       "Name the workspace",
					Icon:        kit.IconFolder,
				}
				ev, ferr := d.name.Layout(env, gtx, props)
				if ferr == nil && ev.Submitted {
					d.dialog.Submit()
				}
				return d.name.Measure(env, gtx, props), ferr
			})
			d.folders.layout(env, body)
			if d.lastErr != "" {
				body.label(d.lastErr, env.Th.Status().Danger)
			}
			return body.size()
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Cancelled {
		// The user cancelled a chain mid-flight: its results must not fold
		// into this closed dialog afterwards.
		d.chain.on = false
	}
	if res.Submitted {
		d.submit(sc)
	}
}

// submit starts or resumes the chain: a fresh submit posts the create, a
// retry after a partway failure posts the failing row alone.
func (d *createDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	name := strings.TrimSpace(d.name.Text())
	if name == "" {
		d.name.MarkSubmitted()
		return
	}
	if d.chain.on {
		if d.chain.idx >= len(d.folders.rows) {
			d.chain.on = false
			d.dialog.Close()
			return
		}
		d.lastErr = ""
		d.postRow(sc)
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutCreate, CreateName: name})
	d.busy = true
	d.lastErr = ""
}

// rootDialog is the edit-root dialog and the add-folders dialog (workspace
// §4, FUG-115). An edit keeps the typed name, path and access the host
// validates; an add opens the shared source-folder list on the workspace's
// detail, and its rows become roots one AddRoot at a time, each a
// compare-and-swap on the hash the step before returned — starting from the
// detail's.
type rootDialog struct {
	dialog       *kit.DialogState
	cancel, save *kit.ButtonState
	name, path   *kit.TextFieldState
	access       *kit.SelectState
	accessSel    string
	folders      sourceFolders
	// wsID is the workspace the dialog edits and edit the root it opened on,
	// empty for an add; wsName is the workspace's display name for the add
	// description; hash is the compare-and-swap the mutation posts.
	wsID, wsName, edit, hash string
	// chain carries the add sequence: hash is the compare-and-swap the next
	// row posts against and idx is the row that comes next — the failing one
	// on a partway failure.
	chain struct {
		on   bool
		hash string
		idx  int
	}
	busy    bool
	lastErr string
}

func newRootDialog() rootDialog {
	return rootDialog{
		dialog:  kit.NewDialogState(),
		cancel:  kit.NewButtonState(),
		save:    kit.NewButtonState(),
		name:    kit.NewTextFieldState(),
		path:    kit.NewTextFieldState(),
		access:  kit.NewSelectState(),
		folders: newSourceFolders(),
	}
}

// openFor opens the dialog on a named root of a workspace's detail.
func (d *rootDialog) openFor(ws *workspace.Detail, root *workspace.Root) {
	d.wsID, d.wsName, d.edit, d.hash = ws.ID, ws.Name, root.Name, ws.ConfigHash
	d.name.SetText(root.Name)
	d.path.SetText(root.Path)
	d.accessSel = root.Access
	d.lastErr = ""
	d.dialog.Open()
}

// openAdd opens the source-folder list on an empty list; the caller checks
// the workspace's detail first, because the first add's compare-and-swap
// needs its hash.
func (d *rootDialog) openAdd(sc *screens, ws *workspace.Detail) {
	d.wsID, d.wsName, d.edit, d.hash = ws.ID, ws.Name, "", ws.ConfigHash
	d.chain = struct {
		on   bool
		hash string
		idx  int
	}{}
	d.folders.onRemove = func(i int) { d.removed(i) }
	d.folders.setPicker(sc.folderPicker)
	d.folders.reset(ws.Roots)
	d.lastErr = ""
	d.dialog.Open()
}

// removed drops a row while the chain waits on one: the row that took its
// place keeps the chain's index.
func (d *rootDialog) removed(i int) {
	d.folders.remove(i)
	if d.chain.on && i < d.chain.idx {
		d.chain.idx--
	}
}

// applied folds an update or a chained add in; a success closes the dialog,
// and the topic event plus the detail re-read refresh the sidebar. A failed
// add puts the host's typed answer on its row and waits for the retry.
func (d *rootDialog) applied(sc *screens, res *workspace.MutResult) {
	d.busy = false
	if d.edit != "" {
		if res.Err == nil {
			d.dialog.Close()
			return
		}
		d.lastErr = res.Err.Error()
		return
	}
	switch {
	case res.Err != nil:
		d.folders.rows[d.chain.idx].err = rowErrorMessage(res.Err)
	default:
		d.folders.rows[d.chain.idx].err = ""
		d.chain.hash = res.NewHash
		d.chain.idx++
		if d.chain.idx >= len(d.folders.rows) {
			d.chain.on = false
			d.dialog.Close()
			return
		}
		d.postRow(sc)
	}
}

// postRow posts the next row's add against the hash the step before
// returned.
func (d *rootDialog) postRow(sc *screens) {
	row := d.folders.rows[d.chain.idx]
	sc.wsMut.Do(&workspace.Mutation{
		Kind: workspace.MutAddRoot, WorkspaceID: d.wsID,
		Name: strings.TrimSpace(row.name.Text()), Path: row.path,
		Access: row.accessSel, ExpectedHash: d.chain.hash,
	})
	d.busy = true
}

func (d *rootDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	addDesc := "Pick folders on this computer to add to this workspace."
	if d.wsName != "" {
		addDesc = "Pick folders on this computer to add to " + d.wsName + "."
	}
	title, primary, description := "Add folders", addLabel, addDesc
	if d.edit != "" {
		title, primary, description = "Edit root "+d.edit, "Save", "Fugue checks the path against this workspace's rules."
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogForm,
		Title:          title,
		Description:    description,
		Cancel:         d.cancel,
		Primary:        d.save,
		PrimaryLabel:   primary,
		PrimaryVariant: kit.Primary,
		Dirty:          d.name.Text() != "" || d.path.Text() != "" || d.folders.dirty(),
		Busy:           d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			return d.body(env, gtx, bands, sc)
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Submitted {
		d.submit(sc)
	}
}

// body draws the dialog: the source-folder list for an add, the typed root
// form for an edit, and the host's answer when one failed.
func (d *rootDialog) body(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, sc *screens) image.Point {
	body := newWSBody(env, gtx, bands, sc.fail)
	if d.edit == "" {
		d.folders.layout(env, body)
	} else {
		d.layName(env, body)
		d.layPath(env, body)
		d.layAccess(env, body)
	}
	if d.lastErr != "" {
		body.label(d.lastErr, env.Th.Status().Danger)
	}
	return body.size()
}

// layName draws the root's name field; Enter submits the dialog.
func (d *rootDialog) layName(env *kit.Env, body *wsBody) {
	body.field(d.name.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.TextFieldProps{
			Label: labelName, Placeholder: "for example kong",
			Helper: "How the root is listed in this workspace.", Error: "Name the root",
		}
		ev, ferr := d.name.Layout(env, gtx, props)
		if ferr == nil && ev.Submitted {
			d.dialog.Submit()
		}
		return d.name.Measure(env, gtx, props), ferr
	})
}

// layPath draws the path field the host validates; Enter submits the dialog.
func (d *rootDialog) layPath(env *kit.Env, body *wsBody) {
	body.field(d.path.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.TextFieldProps{
			Label:       "Path",
			Placeholder: "/absolute/path/of/the/root",
			Helper:      "An absolute path outside the Fugue home.",
			Error:       "Type the root path",
		}
		ev, ferr := d.path.Layout(env, gtx, props)
		if ferr == nil && ev.Submitted {
			d.dialog.Submit()
		}
		return d.path.Measure(env, gtx, props), ferr
	})
}

// layAccess draws the read/write pick; the host holds the root's access
// against the profile rules (workspace §2.2).
func (d *rootDialog) layAccess(env *kit.Env, body *wsBody) {
	body.field(d.access.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:   "Access",
			Options: accessOptions(),
			Value:   d.accessSel,
			Block:   true,
		}
		sres, serr := d.access.Layout(env, gtx, props)
		if serr == nil && sres.Changed {
			d.accessSel = sres.Value
		}
		return d.access.Measure(env, gtx, props), serr
	})
}

// submit posts the edit's update, or starts and resumes the add chain: a
// fresh submit posts the first row against the detail's hash, a retry after
// a partway failure posts the failing row alone.
func (d *rootDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	if d.edit != "" {
		if strings.TrimSpace(d.hash) == "" {
			d.lastErr = errDialogNeedsDetail.Error()
			return
		}
		name, path := strings.TrimSpace(d.name.Text()), strings.TrimSpace(d.path.Text())
		if name == "" {
			d.name.MarkSubmitted()
			return
		}
		if path == "" {
			d.path.MarkSubmitted()
			return
		}
		sc.wsMut.Do(&workspace.Mutation{
			Kind: workspace.MutUpdateRoot, WorkspaceID: d.wsID, Name: name, NewName: name,
			Path: path, Access: d.accessSel, ExpectedHash: d.hash,
		})
		d.busy = true
		d.lastErr = ""
		return
	}
	if strings.TrimSpace(d.hash) == "" {
		d.lastErr = errDialogNeedsDetail.Error()
		return
	}
	if !d.chain.on {
		d.chain.on, d.chain.hash, d.chain.idx = true, d.hash, 0
	}
	if d.chain.idx >= len(d.folders.rows) {
		d.chain.on = false
		d.dialog.Close()
		return
	}
	d.lastErr = ""
	d.postRow(sc)
}
