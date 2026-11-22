package appwin

import (
	"errors"
	"image"
	"image/color"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The workspace dialogs (F10.14): create a workspace, add or edit a root,
// delete a workspace after its name is typed again, and the Q-64 card for an
// out-of-band policy change. Each is one kit Dialog; the window's single
// dialog slot holds whichever is open, so at most one draws per frame and
// kit refuses a second one while one is up. Every mutation rides the
// workspace mutator; the result lands back in apply.

// errDialogNeedsDetail is the static cause behind an action whose workspace
// detail the app has not read yet; it is wrapped, never constructed, at the
// call site.
var errDialogNeedsDetail = errors.New("the workspace detail has not been read yet")

// workspaceDialogs is the dialogs' long-lived state. It changes only on the
// window goroutine.
type workspaceDialogs struct {
	create createDialog
	root   rootDialog
	del    deleteDialog
	rmRoot removeRootDialog
	policy policyDialog
}

// newWorkspaceDialogs builds the dialogs' kit state.
func newWorkspaceDialogs() *workspaceDialogs {
	return &workspaceDialogs{
		create: newCreateDialog(),
		root:   newRootDialog(),
		del:    newDeleteDialog(),
		rmRoot: newRemoveRootDialog(),
		policy: newPolicyDialog(),
	}
}

// anyOpen reports whether any workspace dialog is on screen, so the shell's
// Escape does not act beneath it (§7).
func (d *workspaceDialogs) anyOpen() bool {
	return d.root.dialog.IsOpen() || d.del.dialog.IsOpen() || d.rmRoot.dialog.IsOpen() || d.policy.dialog.IsOpen() ||
		d.create.dialog.IsOpen()
}

// layout draws the open dialog over the window; every dialog lays itself out
// only while open, so exactly one frame carries a dialog.
func (d *workspaceDialogs) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	d.create.layout(env, gtx, sc)
	d.root.layout(env, gtx, sc)
	d.del.layout(env, gtx, sc)
	d.rmRoot.layout(env, gtx, sc)
	d.policy.layout(env, gtx, sc)
}

// apply folds one mutation result into the dialog it came from.
func (d *workspaceDialogs) apply(res *workspace.MutResult) {
	switch res.Mutation.Kind {
	case workspace.MutCreate:
		d.create.applied(res)
	case workspace.MutAddRoot, workspace.MutRemoveRoot, workspace.MutUpdateRoot:
		if res.Mutation.Kind == workspace.MutRemoveRoot {
			d.rmRoot.applied(res)
		} else {
			d.root.applied(res)
		}
	case workspace.MutDelete:
		d.del.applied(res)
	case workspace.MutConfirmPolicy, workspace.MutRejectPolicy:
		d.policy.applied(res)
	}
}

// wsBody runs one dialog body's fields: it draws each at the body's width,
// declares its focus band in draw order (§6.17), and reports the drawn size.
type wsBody struct {
	env   *kit.Env
	bands *kit.DialogBands
	gtx   *layout.Context
	fail  func(error)
	w     int
	gap   int
	y     int
}

func newWSBody(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, fail func(error)) *wsBody {
	return &wsBody{env: env, bands: bands, gtx: gtx, fail: fail, w: gtx.Constraints.Max.X, gap: gtx.Dp(theme.SpaceMD)}
}

// field draws one field at the body's width and declares its band; a nil tag
// is a row no keyboard traversal reaches, for example a diff line.
func (b *wsBody) field(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	local := *b.gtx
	local.Constraints.Min.X = b.w
	dims, err := draw(&local)
	if err != nil {
		b.fail(err)
	}
	if tag != nil {
		b.bands.Focusable(tag, b.y, b.y+dims.Size.Y)
	}
	b.y += dims.Size.Y + b.gap
}

// label draws a read-only line that no band needs.
func (b *wsBody) label(text string, c color.NRGBA) {
	b.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		l := *gtx
		return kit.Label(b.env, &l, theme.TypeSmall(), c, text), nil
	})
}

// size is the body's drawn size.
func (b *wsBody) size() image.Point { return image.Pt(b.w, max(b.y-b.gap, 0)) }

// createDialog is the new-workspace dialog: the name, typed and validated by
// the host (workspace §3).
type createDialog struct {
	dialog         *kit.DialogState
	cancel, create *kit.ButtonState
	name           *kit.TextFieldState
	// busy marks a create post in flight, which blocks a repeat submit;
	// lastErr is the failure the dialog keeps visible while the typed name
	// stays in the field.
	busy    bool
	lastErr string
}

func newCreateDialog() createDialog {
	return createDialog{
		dialog: kit.NewDialogState(),
		cancel: kit.NewButtonState(),
		create: kit.NewButtonState(),
		name:   kit.NewTextFieldState(),
	}
}

// open opens the dialog with an empty name.
func (d *createDialog) open() {
	d.name.SetText("")
	d.lastErr = ""
	d.dialog.Open()
}

// applied folds a create result in: a failure keeps the dialog open with the
// name, so the user edits it instead of retyping it.
func (d *createDialog) applied(res *workspace.MutResult) {
	d.busy = false
	if res.Err != nil {
		d.lastErr = res.Err.Error()
	}
}

func (d *createDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogForm,
		Title:          "New workspace",
		Description:    "The host makes the directory, its fugue.toml and its WORKSPACE.md.",
		Cancel:         d.cancel,
		Primary:        d.create,
		PrimaryLabel:   "Create",
		PrimaryVariant: kit.Primary,
		Dirty:          d.name.Text() != "",
		Busy:           d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newWSBody(env, gtx, bands, sc.fail)
			body.field(d.name.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
				props := &kit.TextFieldProps{
					Label:       "Name",
					Placeholder: "for example fsap",
					Helper:      "The host validates the name.",
					Error:       "Name the workspace",
				}
				ev, ferr := d.name.Layout(env, gtx, props)
				if ferr == nil && ev.Submitted {
					d.dialog.Submit()
				}
				return d.name.Measure(env, gtx, props), ferr
			})
			if d.lastErr != "" {
				body.label(d.lastErr, env.Th.Status().Danger)
			}
			return body.size()
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Submitted {
		d.submit(sc)
	}
}

func (d *createDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	name := strings.TrimSpace(d.name.Text())
	if name == "" {
		d.name.MarkSubmitted()
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutCreate, CreateName: name})
	d.busy = true
	d.lastErr = ""
}

// rootDialog is the add-root and edit-root dialog (workspace §4): the name,
// the path typed in a text field and validated by the host, and the access.
// The path is never chosen from a native folder picker in stage A (Q-96 A).
type rootDialog struct {
	dialog       *kit.DialogState
	cancel, save *kit.ButtonState
	name, path   *kit.TextFieldState
	access       *kit.SelectState
	accessSel    string
	// wsID is the workspace the dialog edits and edit the root it opened on,
	// empty for an add; hash is the compare-and-swap the mutation posts.
	wsID, edit, hash string
	busy             bool
	lastErr          string
}

func newRootDialog() rootDialog {
	return rootDialog{
		dialog: kit.NewDialogState(),
		cancel: kit.NewButtonState(),
		save:   kit.NewButtonState(),
		name:   kit.NewTextFieldState(),
		path:   kit.NewTextFieldState(),
		access: kit.NewSelectState(),
	}
}

// openFor opens the dialog on a workspace's detail: root nil adds a root,
// a named root prefills the fields for an edit.
func (d *rootDialog) openFor(ws *workspace.Detail, root *workspace.Root) {
	if root == nil {
		d.wsID, d.edit, d.hash = ws.ID, "", ws.ConfigHash
		d.name.SetText("")
		d.path.SetText("")
		d.accessSel = client.RootAccessRW
	} else {
		d.wsID, d.edit, d.hash = ws.ID, root.Name, ws.ConfigHash
		d.name.SetText(root.Name)
		d.path.SetText(root.Path)
		d.accessSel = root.Access
	}
	d.lastErr = ""
	d.dialog.Open()
}

// applied folds an add, update or remove result in; a success closes the
// dialog, and the topic event plus the detail re-read refresh the sidebar.
func (d *rootDialog) applied(res *workspace.MutResult) {
	d.busy = false
	if res.Err == nil {
		d.dialog.Close()
		return
	}
	d.lastErr = res.Err.Error()
}

// openAdd arms the add form; the caller checks the workspace's detail first,
// because the mutation needs its hash.
func (d *rootDialog) openAdd(ws *workspace.Detail) {
	d.openFor(ws, nil)
}

func (d *rootDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	title, primary := "Add root", "Add"
	if d.edit != "" {
		title, primary = "Edit root "+d.edit, "Save"
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogForm,
		Title:          title,
		Description:    "The host validates the path against the workspace rules (workspace §2).",
		Cancel:         d.cancel,
		Primary:        d.save,
		PrimaryLabel:   primary,
		PrimaryVariant: kit.Primary,
		Dirty:          d.name.Text() != "" || d.path.Text() != "",
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

// body draws the root form: the name, the typed path, the access, and the
// host's answer when one failed.
func (d *rootDialog) body(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, sc *screens) image.Point {
	body := newWSBody(env, gtx, bands, sc.fail)
	d.layName(env, body)
	d.layPath(env, body)
	d.layAccess(env, body)
	if d.lastErr != "" {
		body.label(d.lastErr, env.Th.Status().Danger)
	}
	return body.size()
}

// layName draws the root's name field; Enter submits the dialog.
func (d *rootDialog) layName(env *kit.Env, body *wsBody) {
	body.field(d.name.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.TextFieldProps{Label: "Name", Placeholder: "for example kong", Error: "Name the root"}
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
			Helper:      "An absolute path outside the Fugue home; the host validates it.",
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
			Options: []kit.SelectOption{{ID: client.RootAccessRW, Label: "read and write"}, {ID: client.RootAccessRO, Label: "read only"}},
			Value:   d.accessSel,
		}
		sres, serr := d.access.Layout(env, gtx, props)
		if serr == nil && sres.Changed {
			d.accessSel = sres.Value
		}
		return d.access.Measure(env, gtx, props), serr
	})
}

func (d *rootDialog) submit(sc *screens) {
	if d.busy {
		return
	}
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
	mut := workspace.Mutation{WorkspaceID: d.wsID, Name: name, Path: path, Access: d.accessSel, ExpectedHash: d.hash}
	if d.edit != "" {
		mut.Kind, mut.NewName = workspace.MutUpdateRoot, name
	} else {
		mut.Kind = workspace.MutAddRoot
	}
	sc.wsMut.Do(&mut)
	d.busy = true
	d.lastErr = ""
}
