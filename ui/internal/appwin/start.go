package appwin

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The start flow (F10.4b): the composer's start-session dialog. Q-53 A
// carries the profile values a client may choose — the model, the effort and
// the resume id — and StartSession resolves the working directory from a
// workspace root, so the flow reads the workspace list and the chosen
// workspace's roots (workspace management itself is F10.14). Stage A serves
// Claude only; the vendor select names the probed vendors, and the effort
// options are the values the host's profile allowlist accepts.

// newSessionLabel names the composer's start row button.
const newSessionLabel = "New session"

// startFlow is the start-session dialog's state: the fields, the workspace
// choice read directly from screens.workspaces, and the start post's busy mark.
type startFlow struct {
	dialog        *kit.DialogState
	cancel, start *kit.ButtonState
	newSession    *kit.ButtonState
	workspace     *kit.ComboboxState
	root          *kit.SelectState
	vendor        *kit.SelectState
	model         *kit.ComboboxState
	effort        *kit.SelectState
	// rootSel, vendorSel and effortSel are the selects' chosen values, which
	// the caller owns (UK-02): the kit Select keeps none.
	rootSel, vendorSel, effortSel string
	// wsID is the chosen workspace's id, empty until one is chosen.
	wsID string
	// busy marks a start post in flight, which blocks a repeat submit
	// (§6.17); lastErr is the failure the dialog could not act on.
	busy    bool
	lastErr string
	// refused marks lastErr as a refusal the user can act on (the session
	// cap), drawn as a warning rather than a fault.
	refused bool
}

// newStartFlow builds the flow's kit state. The flow is shared state the
// window's layout and the actions both reach, so callers hold the pointer.
func newStartFlow() *startFlow {
	return &startFlow{
		dialog:     kit.NewDialogState(),
		cancel:     kit.NewButtonState(),
		start:      kit.NewButtonState(),
		newSession: kit.NewButtonState(),
		workspace:  kit.NewComboboxState(),
		root:       kit.NewSelectState(),
		vendor:     kit.NewSelectState(),
		model:      kit.NewComboboxState(),
		effort:     kit.NewSelectState(),
	}
}

// open opens the dialog and arms the workspace list read.
func (f *startFlow) open(sc *screens) {
	f.dialog.Open()
	f.lastErr, f.refused = "", false
	sc.requestWorkspaces()
}

// reconcile clears a workspace selection that left the shared workspace model
// and updates the root selection only once the workspace's detail has arrived:
// while detail is reloading, any chosen rootSel is preserved.
func (f *startFlow) reconcile(sc *screens) {
	if f.wsID == "" {
		return
	}
	if _, ok := sc.workspaces.Lookup(f.wsID); !ok {
		f.wsID, f.rootSel = "", ""
		return
	}
	if _, hasDet := sc.workspaces.DetailFor(f.wsID); !hasDet {
		return
	}
	roots := f.usableRoots(sc)
	if f.rootSel != "" {
		found := false
		for _, r := range roots {
			if r.Name == f.rootSel {
				found = true
				break
			}
		}
		if !found {
			f.rootSel = ""
		}
	}
	if f.rootSel == "" && len(roots) > 1 {
		f.rootSel = roots[0].Name
	}
}

// detailLoading reports whether a chosen workspace is currently waiting on its
// detail read (for example after an initial selection or a topic drop).
func (f *startFlow) detailLoading(sc *screens) bool {
	if f.wsID == "" {
		return false
	}
	_, ok := sc.workspaces.DetailFor(f.wsID)
	return !ok
}

// canStart reports whether a workspace is chosen and its current detail is
// loaded so Start can submit safely.
func (f *startFlow) canStart(sc *screens) bool {
	return f.wsID != "" && !f.detailLoading(sc)
}

// dirty reports a form with input, which refuses Escape (§6.17).
func (f *startFlow) dirty() bool {
	return f.wsID != "" || f.model.Text() != "" || (f.effortSel != "" && f.effortSel != effortDefaultID)
}

// effortValue is the chosen effort, empty for the vendor's default.
func (f *startFlow) effortValue() string {
	if f.effortSel == effortDefaultID {
		return ""
	}
	return f.effortSel
}

// vendorName is the chosen vendor; an unchosen select names Claude, the
// stage-A vendor.
func (f *startFlow) vendorName() string {
	if f.vendorSel != "" {
		return f.vendorSel
	}
	return client.VendorClaude
}

// rootName is the chosen root, empty when the workspace has exactly one —
// the host resolves a single root itself.
func (f *startFlow) rootName(sc *screens) string {
	roots := f.usableRoots(sc)
	if len(roots) <= 1 {
		return ""
	}
	if f.rootSel == "" {
		return roots[0].Name
	}
	return f.rootSel
}

// usableRoots are the chosen workspace's roots a session can start in: the
// ones the filesystem still has. A missing root would fail the host's own
// check.
func (f *startFlow) usableRoots(sc *screens) []workspace.Root {
	det, ok := sc.workspaces.DetailFor(f.wsID)
	if !ok {
		return nil
	}
	var roots []workspace.Root
	for _, r := range det.Roots {
		if !r.Missing {
			roots = append(roots, r)
		}
	}
	return roots
}

// pausedReason reports the vendor-reported pause of the chosen vendor's
// account, empty when the account is usable or its state is unknown: the
// start dialog shows it before a doomed submit (desktop-app §6 — the reset
// time is the vendor's to report, never a guess).
func (f *startFlow) pausedReason(sc *screens) string {
	acc, ok := sc.accounts.Account(f.vendorName())
	if !ok || acc.State != client.AccountStatePaused {
		return ""
	}
	msg := "The " + f.vendorName() + " account is paused by the vendor"
	if until := formatPausedUntil(acc.PausedUntil); until != "" {
		msg += " until " + until
	}
	return msg + ". The vendor reported this time; start a session once the account resumes."
}

// startBlocked reports a start the flow refuses up front so the button does
// not submit into a typed rejection.
func (f *startFlow) startBlocked(sc *screens) bool { return f.pausedReason(sc) != "" }

// measure is the start row's height at the current width.
func (f *startFlow) measure(env *kit.Env, gtx *layout.Context) int {
	return f.newSession.Measure(env, gtx, kit.ButtonProps{
		Text: newSessionLabel, Variant: kit.Secondary, Size: kit.SizeLG,
	}).Size.Y
}

// row draws the composer's start row, shown while no session is open: the
// new-session button opens the dialog.
func (f *startFlow) row(env *kit.Env, gtx *layout.Context, sc *screens) layout.Dimensions {
	local := *gtx
	return layout.Flex{Axis: layout.Horizontal}.Layout(local,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layout.Inset{Left: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				props := kit.ButtonProps{Text: newSessionLabel, Variant: kit.Secondary, Size: kit.SizeLG}
				clicked, err := f.newSession.Layout(env, &l, props)
				if err != nil {
					sc.fail(err)
				}
				if clicked {
					f.open(sc)
				}
				return f.newSession.Measure(env, &l, props)
			})
		}),
	)
}

// layout runs the dialog's frame, drawing it over the window while open.
func (f *startFlow) layout(env *kit.Env, gtx *layout.Context, sc *screens, c *composer) {
	res, err := f.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:         kit.DialogForm,
		Title:           "New session",
		Description:     "Starts a vendor session in a workspace root.",
		Cancel:          f.cancel,
		Primary:         f.start,
		PrimaryLabel:    "Start",
		PrimaryVariant:  kit.Primary,
		PrimaryDisabled: !f.canStart(sc) || f.startBlocked(sc),
		Dirty:           f.dirty(),
		Busy:            f.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			return f.body(env, gtx, bands, sc, c)
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Submitted {
		f.submit(sc, c)
	}
}

// submit posts the start with the chosen values; the new session arrives on
// the vendor-sessions topic and the screens open it.
func (f *startFlow) submit(sc *screens, c *composer) {
	if f.busy || !f.canStart(sc) {
		switch {
		case f.busy:
		case f.wsID == "":
			f.lastErr = "choose a workspace"
		default:
			f.lastErr = "loading workspace details"
		}
		return
	}
	profile := client.SessionProfile{Model: strings.TrimSpace(f.model.Text()), Effort: f.effortValue()}
	c.poster.StartSession(f.vendorName(), f.wsID, f.rootName(sc), profile)
	f.busy = true
	f.lastErr, f.refused = "", false
}

// fieldLay lays one dialog field out at the body's width and declares its
// focus band, the §6.17 draw-order bookkeeping the body's helpers share.
type fieldLay func(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error))

// body draws the dialog's fields at the body's width and declares their
// focus bands in draw order (§6.17): workspace, root when the workspace has
// several, vendor, model and effort, then the failed start's reason.
func (f *startFlow) body(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, sc *screens, c *composer) image.Point {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceLG)
	y := 0
	lay := func(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error)) {
		local := *gtx
		local.Constraints.Min.X = w
		// Each field paints at its own y: env.Offset records the move so
		// dropdowns know their window coordinates.
		var dims layout.Dimensions
		var err error
		env.Offset(&local, image.Pt(0, y), func(gtx *layout.Context) {
			dims, err = draw(gtx)
		})
		if err != nil {
			sc.fail(err)
		}
		if tag != nil {
			bands.Focusable(tag, y, y+dims.Size.Y)
		}
		y += dims.Size.Y + gap
	}
	f.layWorkspace(env, sc, lay)
	f.layRoot(env, sc, lay)
	f.layVendor(env, c, lay)
	f.layModel(env, lay)
	f.layEffort(env, lay)
	f.layPaused(env, sc, lay)
	f.layError(env, w, lay)
	return image.Pt(w, max(y-gap, 0))
}

// layPaused draws the chosen vendor's vendor-reported pause, if any, ahead of
// the generic error line (F10.5e).
func (f *startFlow) layPaused(env *kit.Env, sc *screens, lay fieldLay) {
	if f.pausedReason(sc) == "" {
		return
	}
	lay(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		local := *gtx
		local.Constraints.Min.X = gtx.Constraints.Max.X
		dims := kit.Paragraph(env, &local, theme.TypeSmall(), env.Th.Status().Warning, f.pausedReason(sc))
		return layout.Dimensions{Size: dims.Size}, nil
	})
}

// layWorkspace draws the workspace picker from the shared workspace model
// and arms the detail read on a choice when the model holds none yet.
func (f *startFlow) layWorkspace(env *kit.Env, sc *screens, lay fieldLay) {
	lay(f.workspace.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.ComboboxProps{
			Label:       labelWorkspace,
			Placeholder: "Pick a workspace",
			Items:       workspaceItems(sc.workspaces.List()),
			Loading:     sc.workspacesPending(),
			EmptyText:   "No workspaces",
			NoResText:   "No matching workspace",
			Block:       true,
		}
		res, err := f.workspace.Layout(env, gtx, props)
		if res.Chosen != "" {
			f.chooseWorkspace(res.Chosen, sc)
		}
		return f.workspace.Measure(env, gtx, props), err
	})
}

// layRoot draws the root picker only for a workspace with several roots (a
// single root is the host's to resolve), or a loading hint while the chosen
// workspace's detail is reloading.
func (f *startFlow) layRoot(env *kit.Env, sc *screens, lay fieldLay) {
	if f.detailLoading(sc) {
		lay(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
			dims := kit.Paragraph(env, gtx, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Loading workspace details…")
			return layout.Dimensions{Size: dims.Size}, nil
		})
		return
	}
	roots := f.usableRoots(sc)
	if len(roots) <= 1 {
		return
	}
	if f.rootSel == "" {
		f.rootSel = roots[0].Name
	}
	lay(f.root.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:       "Root",
			Placeholder: "Pick a root",
			Options:     rootOptions(roots),
			Value:       f.rootSel,
			Block:       true,
		}
		res, err := f.root.Layout(env, gtx, props)
		if res.Changed {
			f.rootSel = res.Value
		}
		return f.root.Measure(env, gtx, props), err
	})
}

// layVendor names the probed vendors.
func (f *startFlow) layVendor(env *kit.Env, c *composer, lay fieldLay) {
	lay(f.vendor.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:       "Vendor",
			Placeholder: "Pick a vendor",
			Options:     vendorOptions(c),
			Value:       f.vendorSel,
			Block:       true,
		}
		res, err := f.vendor.Layout(env, gtx, props)
		if res.Changed {
			f.vendorSel = res.Value
		}
		return f.vendor.Measure(env, gtx, props), err
	})
}

// layModel draws the model field; the host validates the value.
func (f *startFlow) layModel(env *kit.Env, lay fieldLay) {
	lay(f.model.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.ComboboxProps{
			Label:       "Model",
			Placeholder: labelVendorDefault,
			Helper:      "The vendor's model name; the host validates it.",
			Block:       true,
		}
		_, err := f.model.Layout(env, gtx, props)
		return f.model.Measure(env, gtx, props), err
	})
}

// layEffort draws the effort picker with an explicit unselected placeholder (§6.13).
func (f *startFlow) layEffort(env *kit.Env, lay fieldLay) {
	lay(f.effort.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:       "Effort",
			Placeholder: labelVendorDefault,
			Options:     effortOptions(),
			Value:       f.effortSel,
			Block:       true,
		}
		res, err := f.effort.Layout(env, gtx, props)
		if res.Changed {
			f.effortSel = res.Value
		}
		return f.effort.Measure(env, gtx, props), err
	})
}

// layError draws a failed start's reason; the busy mark has cleared by then,
// so the submit button is live again (§6.17: a close is never reported as a
// cancellation).
func (f *startFlow) layError(env *kit.Env, w int, lay fieldLay) {
	if f.lastErr == "" {
		return
	}
	lay(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		local := *gtx
		local.Constraints.Min.X = w
		tone := env.Th.Status().Danger
		if f.refused {
			tone = env.Th.Status().Warning
		}
		dims := kit.Paragraph(env, &local, theme.TypeSmall(), tone, f.lastErr)
		return layout.Dimensions{Size: dims.Size}, nil
	})
}

// chooseWorkspace records the choice and arms the detail read if needed.
func (f *startFlow) chooseWorkspace(id string, sc *screens) {
	if f.wsID == id {
		return
	}
	f.wsID = id
	f.rootSel = ""
	if _, ok := sc.workspaces.DetailFor(id); !ok {
		sc.requestWorkspace(id)
	} else {
		f.reconcile(sc)
	}
}
