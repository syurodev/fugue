package appwin

import (
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The workspace confirmation dialogs (F10.14): deleting a workspace after
// its name is typed again, and the Q-64 card for an out-of-band policy
// change, whose Confirm and Reject both carry the expected hash the detail
// read delivered.

// deleteDialog is the delete-workspace confirm (§6.17): the name typed again
// arms the primary, the safe choice is focused, and the host refuses any
// other name anyway.
type deleteDialog struct {
	dialog          *kit.DialogState
	cancel, confirm *kit.ButtonState
	name            *kit.TextFieldState
	// wsID, hash and wsName are the workspace the dialog is on; hash is the
	// compare-and-swap the deletion posts.
	wsID, hash, wsName string
	busy               bool
	lastErr            string
}

func newDeleteDialog() deleteDialog {
	return deleteDialog{
		dialog:  kit.NewDialogState(),
		cancel:  kit.NewButtonState(),
		confirm: kit.NewButtonState(),
		name:    kit.NewTextFieldState(),
	}
}

// openFor opens the confirm on the workspace's detail; the caller checks the
// detail first, because the deletion needs its hash.
func (d *deleteDialog) openFor(ws *workspace.Detail) {
	d.wsID, d.hash, d.wsName = ws.ID, ws.ConfigHash, ws.Name
	d.name.SetText("")
	d.lastErr = ""
	d.dialog.Open()
}

// applied folds a delete result in; a success closes the dialog, and the
// deleted event drops the workspace from the sidebar.
func (d *deleteDialog) applied(res *workspace.MutResult) {
	d.busy = false
	if res.Err == nil {
		d.dialog.Close()
		return
	}
	d.lastErr = res.Err.Error()
}

func (d *deleteDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogConfirm,
		Title:          "Delete " + d.wsName,
		Description:    "Removes the workspace's directory, configuration and board. Session history stays.",
		Cancel:         d.cancel,
		Primary:        d.confirm,
		PrimaryLabel:   "Delete",
		PrimaryVariant: kit.Danger,
		Dirty:          d.name.Text() != "",
		Busy:           d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newWSBody(env, gtx, bands, sc.fail)
			body.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
				props := &kit.TextFieldProps{
					Label:       "Workspace name",
					Placeholder: d.wsName,
					Helper:      "Type the workspace's name again to arm the deletion.",
					Error:       "Type the name to confirm",
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

func (d *deleteDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	if strings.TrimSpace(d.hash) == "" {
		d.lastErr = errDialogNeedsDetail.Error()
		return
	}
	typed := strings.TrimSpace(d.name.Text())
	if typed == "" {
		d.name.MarkSubmitted()
		return
	}
	if typed != d.wsName {
		d.lastErr = "the name does not match " + d.wsName
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutDelete, WorkspaceID: d.wsID, ExpectedHash: d.hash, ConfirmName: typed})
	d.busy = true
	d.lastErr = ""
}

// policyDialog is the Q-64 card: an out-of-band policy change waits here for
// the user's Confirm or Reject, both carrying the expected hash, until the
// decision the workspace's session starts under is made.
type policyDialog struct {
	dialog                     *kit.DialogState
	cancel, confirm, rejectBtn *kit.ButtonState
	// wsID, hash and wsName are the workspace the card is on; diff is the
	// reviewed change the detail read delivered.
	wsID, hash, wsName string
	diff               []workspace.DiffEntry
	busy               bool
	lastErr            string
}

func newPolicyDialog() policyDialog {
	return policyDialog{
		dialog:    kit.NewDialogState(),
		cancel:    kit.NewButtonState(),
		confirm:   kit.NewButtonState(),
		rejectBtn: kit.NewButtonState(),
	}
}

// openFor opens the card on the workspace's detail; the caller checks the
// detail first, because the diff and the hash ride it.
func (d *policyDialog) openFor(ws *workspace.Detail) {
	d.wsID, d.hash, d.wsName = ws.ID, ws.ConfigHash, ws.Name
	d.diff = ws.Diff
	d.lastErr = ""
	d.dialog.Open()
}

// applied folds a confirm or reject result in: a success closes the card,
// and the topic's confirmed or rejected event refreshes the workspace.
func (d *policyDialog) applied(res *workspace.MutResult) {
	d.busy = false
	if res.Err == nil {
		d.dialog.Close()
		return
	}
	d.lastErr = res.Err.Error()
}

func (d *policyDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:      kit.DialogConfirm,
		Title:        "Policy change in " + d.wsName,
		Description:  "The configuration changed outside Fugue and touches policy keys (Q-64). The change waits for your decision; a session cannot start in this workspace until it is made.",
		Cancel:       d.cancel,
		Primary:      d.confirm,
		PrimaryLabel: "Confirm",
		// The safe choice is Cancel: it decides nothing. Confirm applies the
		// proposed policy; the body's Reject restores the accepted one.
		Busy: d.busy,
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

func (d *policyDialog) body(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, sc *screens) image.Point {
	body := newWSBody(env, gtx, bands, sc.fail)
	if len(d.diff) == 0 {
		body.label("The change's diff is not known yet; the next workspace read fills it in.", env.Th.Chrome().FgMuted)
	}
	for _, e := range d.diff {
		body.label(e.Key+": "+e.OldValue+" → "+e.NewValue, env.Th.Chrome().Fg)
	}
	body.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		props := kit.ButtonProps{Text: "Reject and restore", Variant: kit.Secondary, Size: kit.SizeLG, Busy: d.busy}
		clicked, berr := d.rejectBtn.Layout(env, gtx, props)
		if berr != nil {
			return d.rejectBtn.Measure(env, gtx, props), berr
		}
		if clicked {
			d.reject(sc)
		}
		return d.rejectBtn.Measure(env, gtx, props), nil
	})
	if d.lastErr != "" {
		body.label(d.lastErr, env.Th.Status().Danger)
	}
	return body.size()
}

func (d *policyDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	if strings.TrimSpace(d.hash) == "" {
		d.lastErr = errDialogNeedsDetail.Error()
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutConfirmPolicy, WorkspaceID: d.wsID, ExpectedHash: d.hash})
	d.busy = true
	d.lastErr = ""
}

// reject posts the decision that restores the accepted configuration (Q-64),
// carrying the reviewed hash the detail read delivered.
func (d *policyDialog) reject(sc *screens) {
	if d.busy {
		return
	}
	if strings.TrimSpace(d.hash) == "" {
		d.lastErr = errDialogNeedsDetail.Error()
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutRejectPolicy, WorkspaceID: d.wsID, ExpectedHash: d.hash})
	d.busy = true
	d.lastErr = ""
}

// removeRootDialog is the remove-root confirm (workspace §4.2): the root's
// grants and seen versions the host drops, the safe choice is focused, and
// the removal is one compare-and-swap on the config hash.
type removeRootDialog struct {
	dialog          *kit.DialogState
	cancel, confirm *kit.ButtonState
	// wsID, hash and root name the dialog is on.
	wsID, hash, root string
	busy             bool
	lastErr          string
}

func newRemoveRootDialog() removeRootDialog {
	return removeRootDialog{
		dialog:  kit.NewDialogState(),
		cancel:  kit.NewButtonState(),
		confirm: kit.NewButtonState(),
	}
}

// openFor opens the confirm on one root of the workspace's detail.
func (d *removeRootDialog) openFor(ws *workspace.Detail, root workspace.Root) {
	d.wsID, d.hash, d.root = ws.ID, ws.ConfigHash, root.Name
	d.lastErr = ""
	d.dialog.Open()
}

// applied folds a remove result in; a success closes the dialog, and the
// topic event plus the detail re-read refresh the sidebar.
func (d *removeRootDialog) applied(res *workspace.MutResult) {
	d.busy = false
	if res.Err == nil {
		d.dialog.Close()
		return
	}
	d.lastErr = res.Err.Error()
}

func (d *removeRootDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogConfirm,
		Title:          "Remove root " + d.root,
		Description:    "Sessions stop reaching the root at their next step, and its grants are dropped (workspace §4.2).",
		Cancel:         d.cancel,
		Primary:        d.confirm,
		PrimaryLabel:   "Remove",
		PrimaryVariant: kit.Danger,
		Busy:           d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newWSBody(env, gtx, bands, sc.fail)
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

func (d *removeRootDialog) submit(sc *screens) {
	if d.busy {
		return
	}
	if strings.TrimSpace(d.hash) == "" {
		d.lastErr = errDialogNeedsDetail.Error()
		return
	}
	sc.wsMut.Do(&workspace.Mutation{Kind: workspace.MutRemoveRoot, WorkspaceID: d.wsID, Name: d.root, ExpectedHash: d.hash})
	d.busy = true
	d.lastErr = ""
}
