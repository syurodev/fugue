package appwin

import (
	"errors"
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/layout"

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

// apply folds one mutation result into the dialog it came from. An add
// result belongs to the create dialog's chain while that chain runs — only
// one workspace dialog is open at a time, and the mutator answers one post
// at a time.
func (d *workspaceDialogs) apply(sc *screens, res *workspace.MutResult) {
	switch res.Mutation.Kind {
	case workspace.MutCreate:
		d.create.applied(sc, res)
	case workspace.MutAddRoot, workspace.MutUpdateRoot:
		if d.create.chain.on {
			d.create.applied(sc, res)
		} else {
			d.root.applied(sc, res)
		}
	case workspace.MutRemoveRoot:
		d.rmRoot.applied(res)
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
	// ring is the dialog's focus-ring allowance: the kit insets the body by
	// it on each side, so fields draw that far out to share the title's and
	// the description's left edge.
	ring int
	// rows is every drawn row's box in body coordinates, in draw order.
	rows []image.Rectangle
}

func newWSBody(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, fail func(error)) *wsBody {
	ring := gtx.Dp(theme.FocusStroke) + gtx.Dp(theme.FocusOffset)
	gap := gtx.Dp(theme.SpaceLG)
	// The body opens one gap below the description, so the first label does
	// not touch it; the dialog frame adds no space of its own there.
	return &wsBody{
		env: env, bands: bands, gtx: gtx, fail: fail, ring: ring,
		w: gtx.Constraints.Max.X + 2*ring, gap: gap, y: gap,
	}
}

// field draws one field at the body's width and declares its band; a nil tag
// is a row no keyboard traversal reaches, for example a diff line.
func (b *wsBody) field(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	local := *b.gtx
	local.Constraints.Min.X, local.Constraints.Max.X = b.w, b.w
	// Each row is drawn at its own y: env.Offset records the move so dropdowns
	// inside the dialog (the Access Select) know their window coordinates.
	var dims layout.Dimensions
	var err error
	b.env.Offset(&local, image.Pt(-b.ring, b.y), func(gtx *layout.Context) {
		dims, err = draw(gtx)
	})
	if err != nil {
		b.fail(err)
	}
	if tag != nil {
		b.bands.Focusable(tag, b.y, b.y+dims.Size.Y)
	}
	b.rows = append(b.rows, image.Rectangle{Min: image.Pt(0, b.y), Max: image.Pt(b.w, b.y+dims.Size.Y)})
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
