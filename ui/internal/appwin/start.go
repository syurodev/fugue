package appwin

import (
	"image"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The start flow (F10.4b): the composer's start-session dialog. Q-53 A
// carries the profile values a client may choose — the model, the effort and
// the resume id — and StartSession resolves the working directory from a
// workspace root, so the flow reads the workspace list and the chosen
// workspace's roots (workspace management itself is F10.14). Stage A serves
// Claude only; the vendor select names the probed vendors, and the effort
// options are the values the host's profile allowlist accepts.

// effortOptions are the effort values the host's profile allowlist accepts;
// empty uses the vendor's default and the host rejects anything else. The
// host validates the chosen value, so the picker offers exactly these.
func effortOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: "", Label: "vendor default"},
		{ID: "low", Label: "low"},
		{ID: "medium", Label: "medium"},
		{ID: "high", Label: "high"},
		{ID: "xhigh", Label: "xhigh"},
		{ID: "max", Label: "max"},
	}
}

// newSessionLabel names the composer's start row button.
const newSessionLabel = "New session"

// startFlow is the start-session dialog's state: the fields, the reads they
// drive, and the start post's busy mark.
type startFlow struct {
	dialog        *kit.DialogState
	cancel, start *kit.ButtonState
	newSession    *kit.ButtonState
	workspace     *kit.ComboboxState
	root          *kit.SelectState
	vendor        *kit.SelectState
	model         *kit.ComboboxState
	effort        *kit.SelectState
	// workspaces is the list read; detail the chosen workspace's roots.
	workspaces []client.WorkspaceSummary
	detail     client.Workspace
	// rootSel, vendorSel and effortSel are the selects' chosen values, which
	// the caller owns (UK-02): the kit Select keeps none.
	rootSel, vendorSel, effortSel string
	// wsID is the chosen workspace's id, empty until one is chosen.
	wsID string
	// busy marks a start post in flight, which blocks a repeat submit
	// (§6.17); lastErr is the failure the dialog could not act on.
	busy    bool
	lastErr string
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
	f.lastErr = ""
	sc.requestWorkspaces()
}

// applyWorkspaces folds a workspace list read into the flow.
func (f *startFlow) applyWorkspaces(list []client.WorkspaceSummary) {
	f.workspaces = list
}

// applyDetail folds a workspace read into the flow: the roots the root
// select chooses from, defaulting to the first.
func (f *startFlow) applyDetail(ws client.Workspace) {
	f.detail = ws
	if f.rootSel == "" && len(f.usableRoots()) > 1 {
		f.rootSel = f.usableRoots()[0].Name
	}
}

// startPosted folds a start post's result in: the busy mark clears, a
// failure shows.
func (f *startFlow) startPosted(err error) {
	f.busy = false
	if err != nil {
		f.lastErr = err.Error()
	}
}

// dirty reports a form with input, which refuses Escape (§6.17).
func (f *startFlow) dirty() bool {
	return f.wsID != "" || f.model.Text() != "" || f.effortSel != ""
}

// effortValue is the chosen effort, empty for the vendor's default.
func (f *startFlow) effortValue() string { return f.effortSel }

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
func (f *startFlow) rootName() string {
	if len(f.usableRoots()) <= 1 {
		return ""
	}
	return f.rootSel
}

// usableRoots are the workspace's roots a session can start in: the ones the
// filesystem still has. A missing root would fail the host's own check.
func (f *startFlow) usableRoots() []client.WorkspaceRoot {
	var roots []client.WorkspaceRoot
	for _, r := range f.detail.Roots {
		if !r.Missing {
			roots = append(roots, r)
		}
	}
	return roots
}

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
func (f *startFlow) layout(env *kit.Env, gtx *layout.Context, sc *screens, c *composer) layout.Dimensions {
	res, err := f.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:        kit.DialogForm,
		Title:          "New session",
		Description:    "Starts a vendor session in a workspace root.",
		Cancel:         f.cancel,
		Primary:        f.start,
		PrimaryLabel:   "Start",
		PrimaryVariant: kit.Primary,
		Dirty:          f.dirty(),
		Busy:           f.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			return f.body(env, gtx, bands, sc, c)
		},
	})
	if err != nil {
		sc.fail(err)
	}
	if res.Submitted {
		f.submit(c)
	}
	return layout.Dimensions{Size: res.Size}
}

// submit posts the start with the chosen values; the new session arrives on
// the vendor-sessions topic and the screens open it.
func (f *startFlow) submit(c *composer) {
	if f.busy || f.wsID == "" {
		if f.wsID == "" {
			f.lastErr = "choose a workspace"
		}
		return
	}
	profile := client.SessionProfile{Model: strings.TrimSpace(f.model.Text()), Effort: f.effortValue()}
	c.poster.StartSession(f.vendorName(), f.wsID, f.rootName(), profile)
	f.busy = true
	f.lastErr = ""
}

// fieldLay lays one dialog field out at the body's width and declares its
// focus band, the §6.17 draw-order bookkeeping the body's helpers share.
type fieldLay func(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error))

// body draws the dialog's fields at the body's width and declares their
// focus bands in draw order (§6.17): workspace, root when the workspace has
// several, vendor, model and effort, then the failed start's reason.
func (f *startFlow) body(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, sc *screens, c *composer) image.Point {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	y := 0
	lay := func(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error)) {
		local := *gtx
		local.Constraints.Min.X = w
		dims, err := draw(&local)
		if err != nil {
			sc.fail(err)
		}
		if tag != nil {
			bands.Focusable(tag, y, y+dims.Size.Y)
		}
		y += dims.Size.Y + gap
	}
	f.layWorkspace(env, sc, lay)
	f.layRoot(env, lay)
	f.layVendor(env, c, lay)
	f.layModel(env, lay)
	f.layEffort(env, lay)
	f.layError(env, w, lay)
	return image.Pt(w, max(y-gap, 0))
}

// layWorkspace draws the workspace picker and arms the roots read on a
// choice.
func (f *startFlow) layWorkspace(env *kit.Env, sc *screens, lay fieldLay) {
	lay(f.workspace.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.ComboboxProps{
			Label:       "Workspace",
			Placeholder: "Pick a workspace",
			Items:       workspaceItems(f.workspaces),
			Loading:     sc.workspacesPending(),
			EmptyText:   "No workspaces",
			NoResText:   "No matching workspace",
		}
		res, err := f.workspace.Layout(env, gtx, props)
		if res.Chosen != "" {
			f.chooseWorkspace(res.Chosen, sc)
		}
		return f.workspace.Measure(env, gtx, props), err
	})
}

// layRoot draws the root picker only for a workspace with several roots: a
// single root is the host's to resolve.
func (f *startFlow) layRoot(env *kit.Env, lay fieldLay) {
	if len(f.usableRoots()) <= 1 {
		return
	}
	lay(f.root.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:       "Root",
			Placeholder: "Pick a root",
			Options:     rootOptions(f.usableRoots()),
			Value:       f.rootSel,
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
			Placeholder: "vendor default",
			Helper:      "The vendor's model name; the host validates it.",
		}
		_, err := f.model.Layout(env, gtx, props)
		return f.model.Measure(env, gtx, props), err
	})
}

// layEffort draws the effort picker.
func (f *startFlow) layEffort(env *kit.Env, lay fieldLay) {
	lay(f.effort.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := &kit.SelectProps{
			Label:   "Effort",
			Options: effortOptions(),
			Value:   f.effortSel,
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
		dims := kit.Paragraph(env, &local, theme.TypeSmall(), env.Th.Status().Danger, f.lastErr)
		return layout.Dimensions{Size: dims.Size}, nil
	})
}

// chooseWorkspace records the choice and arms the roots read.
func (f *startFlow) chooseWorkspace(id string, sc *screens) {
	if f.wsID == id {
		return
	}
	f.wsID = id
	f.detail = client.Workspace{}
	sc.requestWorkspace(id)
}

// workspaceItems maps the workspace list onto the combobox's rows.
func workspaceItems(list []client.WorkspaceSummary) []kit.SuggestItem {
	items := make([]kit.SuggestItem, 0, len(list))
	for _, ws := range list {
		items = append(items, kit.SuggestItem{ID: ws.ID, Label: ws.Name})
	}
	return items
}

// rootOptions maps the workspace's usable roots onto the select's options.
func rootOptions(roots []client.WorkspaceRoot) []kit.SelectOption {
	opts := make([]kit.SelectOption, 0, len(roots))
	for _, r := range roots {
		opts = append(opts, kit.SelectOption{ID: r.Name, Label: r.Name})
	}
	return opts
}

// vendorOptions names the probed vendors, Claude always — the stage-A vendor
// and the one a probe without answers still serves.
func vendorOptions(c *composer) []kit.SelectOption {
	var opts []kit.SelectOption
	if _, ok := c.caps[client.VendorClaude]; !ok {
		opts = append(opts, kit.SelectOption{ID: client.VendorClaude, Label: client.VendorClaude})
	}
	for vendor := range c.caps {
		opts = append(opts, kit.SelectOption{ID: vendor, Label: vendor})
	}
	return opts
}
