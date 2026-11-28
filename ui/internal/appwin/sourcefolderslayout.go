package appwin

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The source-folder list's drawing (FUG-115): the section heading, the rows
// directly on the dialog surface separated by hairlines, the left-aligned
// action row, the typed-path fallback area, and the band declarations the
// dialog's focus trap cycles through. The list's state and rules live in
// sourcefolders.go.

const (
	// addLabel is the primary button's label on Add folders and the typed
	// fallback's Add button.
	addLabel = "Add"
	// addFolderLabel is the folder-picker button's label.
	addFolderLabel = "Add folder…"
	// enterPathLabel is the link button's label that reveals the typed path
	// field.
	enterPathLabel = "Enter a path"
)

// keepDrawErr records the first error of one draw pass, so a frame reports
// one failure however many controls drew.
func keepDrawErr(errp *error, err error) {
	if err != nil && *errp == nil {
		*errp = err
	}
}

// folderStack lays the section's lines out at a manual y on the dialog body
// and declares each line's band on the dialog's focus trap (§6.17).
type folderStack struct {
	env   *kit.Env
	body  *wsBody
	gtx   *layout.Context
	w     int
	gap   int
	baseY int
	y     int
	err   error
}

// line draws one line at the stack's y and advances past it by gap.
func (s *folderStack) line(band func(y0, y1 int), draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	s.lineGap(s.gap, band, draw)
}

// lineGap draws one line at the stack's y and advances past it by gap.
func (s *folderStack) lineGap(gap int, band func(y0, y1 int), draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	local := *s.gtx
	local.Constraints.Min.X, local.Constraints.Max.X = s.w, s.w
	local.Constraints.Min.Y = 0
	var dims layout.Dimensions
	var lerr error
	s.env.Offset(&local, image.Pt(0, s.y), func(gtx *layout.Context) {
		dims, lerr = draw(gtx)
	})
	keepDrawErr(&s.err, lerr)
	if band != nil {
		band(s.baseY+s.y, s.baseY+s.y+dims.Size.Y)
	}
	s.y += dims.Size.Y + gap
}

// layout draws the section: the "Folders" heading, the rows (or empty state)
// directly on the dialog surface, the left-aligned action row, and the
// typed-path fallback when open.
func (f *sourceFolders) layout(env *kit.Env, body *wsBody) {
	body.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		s := &folderStack{
			env: env, body: body, gtx: gtx,
			w: body.w, gap: gtx.Dp(theme.SpaceMD), baseY: body.y,
		}
		s.lineGap(gtx.Dp(theme.SpaceSM), nil, func(gtx *layout.Context) (layout.Dimensions, error) {
			l := *gtx
			l.Constraints.Min.X = s.w
			return kit.Label(env, &l, theme.TypeLabel(), env.Th.Chrome().Fg, "Folders"), nil
		})
		if len(f.rows) == 0 {
			s.line(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
				l := *gtx
				l.Constraints.Min.X = s.w
				return kit.Label(env, &l, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Add a folder on this computer."), nil
			})
		} else {
			f.rowsLines(s)
		}
		s.line(func(y0, y1 int) {
			body.bands.Focusable(f.add.Tag(), y0, y1)
			body.bands.Focusable(f.reveal.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutActions(env, gtx)
		})
		if f.typedOpen {
			s.line(func(y0, y1 int) {
				body.bands.Focusable(f.typed.Tag(), y0, y1)
				body.bands.Focusable(f.typedAdd.Tag(), y0, y1)
			}, func(gtx *layout.Context) (layout.Dimensions, error) {
				return f.layoutTyped(env, gtx)
			})
		}
		return layout.Dimensions{Size: image.Pt(s.w, max(s.y-s.gap, 0))}, s.err
	})
	if f.refused != "" {
		body.label(f.refused, env.Th.Status().Danger)
	}
}

// rowsLines lays one line per row separated by Divider hairlines, plus the
// failing row's error line under it when present.
func (f *sourceFolders) rowsLines(s *folderStack) {
	for i, row := range f.rows {
		if i > 0 {
			s.line(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
				l := *gtx
				l.Constraints.Min.X, l.Constraints.Max.X = s.w, s.w
				return kit.Divider(s.env, &l, kit.DividerProps{Orientation: kit.Horizontal}), nil
			})
		}
		r := row
		idx := i
		s.line(func(y0, y1 int) {
			// The three focusables of one row share its y range: the trap
			// cycles them in draw order (§6.17).
			s.body.bands.Focusable(r.name.Tag(), y0, y1)
			s.body.bands.Focusable(r.access.Tag(), y0, y1)
			s.body.bands.Focusable(r.rm.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutRow(s.env, gtx, idx, r)
		})
	}
}

// layoutActions draws the left-aligned action row under the list: the
// Secondary SizeSM "Add folder…" button and the Link "Enter a path" button on
// one baseline.
func (f *sourceFolders) layoutActions(env *kit.Env, gtx *layout.Context) (layout.Dimensions, error) {
	addProps := kit.ButtonProps{Text: addFolderLabel, Icon: kit.IconFolderPlus, Variant: kit.Secondary, Size: kit.SizeSM, Busy: f.picking}
	revealProps := kit.ButtonProps{Text: enterPathLabel, Variant: kit.Link, Size: kit.SizeSM, Disabled: f.typedOpen}
	gap := gtx.Dp(theme.SpaceMD)
	var err error
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Baseline}.Layout(*gtx,
		f.pickChild(env, addProps, &err),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gap, 0)}
		}),
		f.revealChild(env, revealProps, &err),
	)
	return dims, err
}

// revealChild is the "Enter a path" link's flex child: a click opens the
// fallback field, and the link disables itself once it is open.
func (f *sourceFolders) revealChild(env *kit.Env, revealProps kit.ButtonProps, errp *error) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		revealed, lerr := f.reveal.Layout(env, &local, revealProps)
		keepDrawErr(errp, lerr)
		if revealed {
			f.revealTyped()
		}
		return f.reveal.Measure(env, &local, revealProps)
	})
}

// pickChild is the "Add folder…" button's flex child: a click asks the
// window's picker for a folder.
func (f *sourceFolders) pickChild(env *kit.Env, addProps kit.ButtonProps, errp *error) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		clicked, lerr := f.add.Layout(env, &local, addProps)
		keepDrawErr(errp, lerr)
		if clicked {
			f.pick()
		}
		return f.add.Measure(env, &local, addProps)
	})
}

// rowGeom is one row's geometry: where the folder icon, the name field, the
// access pick, the remove button, the path line and any row error sit.
type rowGeom struct {
	w, gap, lineGap, iconW, lineH, twoLineH int
	nameX, nameW, pathX                     int
	selectX, selectW                        int
	rmX, rmW                                int
}

// layoutRow draws one folder row: the folder glyph vertically centred on the
// first two lines, the inline-editable name, the fixed-width access pick and
// the remove button on line 1, the muted middle-truncated path on line 2
// aligned to the name field's text start, and the host error on line 3 when
// present.
func (f *sourceFolders) layoutRow(env *kit.Env, gtx *layout.Context, i int, row *sourceRow) (layout.Dimensions, error) {
	g := f.rowGeom(env, gtx, row)
	selectProps := &kit.SelectProps{Options: accessOptions(), Value: row.accessSel}
	rmProps := &kit.IconButtonProps{Icon: kit.IconClose, Label: "Remove " + row.name.Text(), Size: kit.SizeXS}
	var err error
	f.rowIcon(env, gtx, g, &err)
	f.rowName(env, gtx, row, g, &err)
	f.rowAccess(env, gtx, row, g, selectProps, &err)
	f.rowRemove(env, gtx, i, row, g, rmProps, &err)
	f.rowPath(env, gtx, row, g)
	totalH := g.twoLineH
	if row.err != "" {
		errH := f.rowError(env, gtx, row, g)
		totalH += g.lineGap + errH
	}
	return layout.Dimensions{Size: image.Pt(g.w, totalH)}, err
}

// rowGeom measures one row's fixed boxes: the access pick is sized to its
// longest option ("Read & write") so rows align regardless of selection; the
// remove button sizes to its content; the name field takes the rest.
func (f *sourceFolders) rowGeom(env *kit.Env, gtx *layout.Context, row *sourceRow) rowGeom {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	lineGap := gtx.Dp(theme.SpaceXS)
	iconW := gtx.Dp(theme.IconMD)
	lineH := gtx.Dp(theme.ControlMD)
	pathH := gtx.Sp(theme.TypeSmall().Line)
	opts := accessOptions()
	selectW := 0
	for _, opt := range opts {
		sw := row.access.Measure(env, gtx, &kit.SelectProps{Options: opts, Value: opt.ID}).Size.X
		selectW = max(selectW, sw)
	}
	if selectW == 0 {
		selectW = row.access.Measure(env, gtx, &kit.SelectProps{Options: opts, Value: client.RootAccessRW}).Size.X
	}
	rmW := row.rm.Measure(env, gtx, &kit.IconButtonProps{Icon: kit.IconClose, Label: "Remove " + row.name.Text(), Size: kit.SizeXS}).Size.X
	nameX := iconW + gap
	// TextField draws its editor text at SpaceLG inside the box (no leading icon).
	pathX := nameX + gtx.Dp(theme.SpaceLG)
	return rowGeom{
		w: w, gap: gap, lineGap: lineGap, iconW: iconW, lineH: lineH,
		twoLineH: lineH + lineGap + pathH,
		nameX:    nameX, nameW: max(w-nameX-gap-selectW-gap-rmW, 0),
		pathX:   pathX,
		selectX: w - rmW - gap - selectW, selectW: selectW,
		rmX: w - rmW, rmW: rmW,
	}
}

// rowIcon draws the row's folder glyph, vertically centred on lines 1 and 2.
func (f *sourceFolders) rowIcon(env *kit.Env, gtx *layout.Context, g rowGeom, errp *error) {
	env.Offset(gtx, image.Pt(0, (g.twoLineH-g.iconW)/2), func(gtx *layout.Context) {
		_, ierr := env.Icon(gtx, kit.IconFolder, theme.IconMD, env.Th.Chrome().FgMuted)
		keepDrawErr(errp, ierr)
	})
}

// rowName draws the row's inline-editable root name.
func (f *sourceFolders) rowName(env *kit.Env, gtx *layout.Context, row *sourceRow, g rowGeom, errp *error) {
	env.Offset(gtx, image.Pt(g.nameX, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = g.nameW, g.nameW
		_, nerr := row.name.Layout(env, &local, &kit.TextFieldProps{Placeholder: "Name the root"})
		keepDrawErr(errp, nerr)
	})
}

// rowAccess draws the row's read/write pick; the host holds the root's
// access against the profile rules (workspace §2.2).
func (f *sourceFolders) rowAccess(env *kit.Env, gtx *layout.Context, row *sourceRow, g rowGeom, selectProps *kit.SelectProps, errp *error) {
	env.Offset(gtx, image.Pt(g.selectX, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = g.selectW, g.selectW
		res, serr := row.access.Layout(env, &local, selectProps)
		if serr == nil && res.Changed {
			row.accessSel = res.Value
		}
		keepDrawErr(errp, serr)
	})
}

// rowRemove draws the row's remove button; a click drops the row through the
// dialog's own step, so its add chain follows the row that takes this one's
// place.
func (f *sourceFolders) rowRemove(env *kit.Env, gtx *layout.Context, i int, row *sourceRow, g rowGeom, rmProps *kit.IconButtonProps, errp *error) {
	env.Offset(gtx, image.Pt(g.rmX, (g.lineH-g.rmW)/2), func(gtx *layout.Context) {
		local := *gtx
		clicked, rerr := row.rm.Layout(env, &local, rmProps)
		keepDrawErr(errp, rerr)
		if clicked {
			if f.onRemove != nil {
				f.onRemove(i)
			} else {
				f.remove(i)
			}
		}
	})
}

// rowPath draws the row's absolute path, muted and truncated in the middle,
// aligned to the text start inside the name field.
func (f *sourceFolders) rowPath(env *kit.Env, gtx *layout.Context, row *sourceRow, g rowGeom) {
	env.Offset(gtx, image.Pt(g.pathX, g.lineH+g.lineGap), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = 0, max(g.w-g.pathX, 0)
		kit.MiddleEllipsisLabel(env, &local, theme.TypeSmall(), env.Th.Chrome().FgMuted, row.path)
	})
}

// rowError draws the row's host error as line 3 in danger text style.
func (f *sourceFolders) rowError(env *kit.Env, gtx *layout.Context, row *sourceRow, g rowGeom) int {
	var dims layout.Dimensions
	env.Offset(gtx, image.Pt(g.pathX, g.twoLineH+g.lineGap), func(gtx *layout.Context) {
		local := *gtx
		maxW := max(g.w-g.pathX, 0)
		local.Constraints.Min.X, local.Constraints.Max.X = maxW, maxW
		dims = kit.Paragraph(env, &local, theme.TypeSmall(), env.Th.Status().Danger, row.err)
	})
	return dims.Size.Y
}

// layoutTyped draws the fallback area: the short note when no picker exists,
// then the absolute path field and its Add button, split evenly like §4.6's
// multi-control rows, the button aligned with the field's box. Enter adds
// the row.
func (f *sourceFolders) layoutTyped(env *kit.Env, gtx *layout.Context) (layout.Dimensions, error) {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	props := &kit.TextFieldProps{
		Label:       "Path",
		Placeholder: "/absolute/path/of/the/folder",
		Helper:      "An absolute path outside the Fugue home.",
		Error:       f.typedErr,
	}
	y := 0
	if f.noPicker {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = w, w
		kit.Label(env, &local, theme.TypeSmall(), env.Th.Chrome().FgMuted,
			"No folder picker is available on this computer — type the absolute path.")
		y = gtx.Sp(theme.TypeSmall().Line) + gap
	}
	var err error
	fieldW := (w - gap) / 2
	env.Offset(gtx, image.Pt(0, y), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = fieldW, fieldW
		ev, terr := f.typed.Layout(env, &local, props)
		if terr == nil && ev.Submitted {
			f.addTyped()
		}
		keepDrawErr(&err, terr)
	})
	// The button shares the field's box line: the label and its gap sit it
	// that far above the box.
	boxY := gtx.Sp(theme.TypeSmall().Line) + gtx.Dp(theme.SpaceXS)
	env.Offset(gtx, image.Pt(fieldW+gap, y+boxY), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = w-fieldW-gap, w-fieldW-gap
		clicked, aerr := f.typedAdd.Layout(env, &local, kit.ButtonProps{
			Text: addLabel, Variant: kit.Secondary, Size: kit.SizeMD, Block: true,
		})
		keepDrawErr(&err, aerr)
		if clicked {
			f.addTyped()
		}
	})
	dims := f.typed.Measure(env, gtx, props)
	return layout.Dimensions{Size: image.Pt(w, y+dims.Size.Y)}, err
}
