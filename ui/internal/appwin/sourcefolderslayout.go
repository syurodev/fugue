package appwin

import (
	"errors"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The source-folder list's drawing (FUG-115): the section heading, the card
// with its rows and affordance line, the typed-path fallback area, and the
// band declarations the dialog's focus trap cycles through. The list's state
// and rules live in sourcefolders.go.

// addLabel is the Add button's label, the empty state's and the typed
// fallback's alike.
const addLabel = "Add"

// keepDrawErr records the first error of one draw pass, so a frame reports
// one failure however many controls drew.
func keepDrawErr(errp *error, err error) {
	if err != nil && *errp == nil {
		*errp = err
	}
}

// cardStack lays the box's lines out at a manual y, the way the dialog body
// does, and declares each line's band on the dialog's focus trap (§6.17).
// The card's padding already insets the body, so a band adds the card
// field's y and that padding once, here in cardY.
type cardStack struct {
	env   *kit.Env
	body  *wsBody
	gtx   *layout.Context
	w     int
	gap   int
	cardY int
	y     int
	err   error
}

// line draws one line at the stack's y and advances past it.
func (c *cardStack) line(band func(y0, y1 int), draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	local := *c.gtx
	local.Constraints.Min.X, local.Constraints.Max.X = c.w, c.w
	var dims layout.Dimensions
	var lerr error
	c.env.Offset(&local, image.Pt(0, c.y), func(gtx *layout.Context) {
		dims, lerr = draw(gtx)
	})
	keepDrawErr(&c.err, lerr)
	if band != nil {
		band(c.cardY+c.y, c.cardY+c.y+dims.Size.Y)
	}
	c.y += dims.Size.Y + c.gap
}

// layout draws the section: the heading and the box with its rows, the
// affordance line and the fallback area.
func (f *sourceFolders) layout(env *kit.Env, body *wsBody) {
	body.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		l := *gtx
		l.Constraints.Min.X = body.w
		return kit.Label(env, &l, theme.TypeLabel(), env.Th.Chrome().Fg, "Source folders"), nil
	})
	body.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		return f.layoutCard(env, gtx, body)
	})
	if f.refused != "" {
		body.label(f.refused, env.Th.Status().Danger)
	}
}

// layoutCard draws the box: the empty state or the rows, then the affordance
// line, then the fallback area when it is open.
func (f *sourceFolders) layoutCard(env *kit.Env, gtx *layout.Context, body *wsBody) (layout.Dimensions, error) {
	var bodyErr error
	dims, _, cerr := f.card.Layout(env, gtx, kit.CardProps{Padding: theme.SpaceLG}, func(gtx layout.Context) layout.Dimensions {
		d, berr := f.cardBody(env, &gtx, body)
		if berr != nil {
			bodyErr = berr
		}
		return d
	})
	return dims, errors.Join(cerr, bodyErr)
}

// cardBody lays the box's lines out at a manual y, so each row's controls
// know their band's y range.
func (f *sourceFolders) cardBody(env *kit.Env, gtx *layout.Context, body *wsBody) (layout.Dimensions, error) {
	c := &cardStack{
		env: env, body: body, gtx: gtx,
		w: gtx.Constraints.Max.X, gap: gtx.Dp(theme.SpaceMD),
		cardY: body.y + gtx.Dp(theme.SpaceLG),
	}
	if len(f.rows) == 0 {
		c.line(func(y0, y1 int) { body.bands.Focusable(f.add.Tag(), y0, y1) },
			func(gtx *layout.Context) (layout.Dimensions, error) {
				return f.layoutAffordance(env, gtx, f.emptyStateLine)
			})
	}
	f.rowsLines(c)
	c.line(func(y0, y1 int) {
		body.bands.Focusable(f.reveal.Tag(), y0, y1)
		body.bands.Focusable(f.add.Tag(), y0, y1)
	}, func(gtx *layout.Context) (layout.Dimensions, error) {
		return f.layoutAffordance(env, gtx, nil)
	})
	if f.typedOpen {
		c.line(func(y0, y1 int) {
			body.bands.Focusable(f.typed.Tag(), y0, y1)
			body.bands.Focusable(f.typedAdd.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutTyped(env, gtx)
		})
	}
	return layout.Dimensions{Size: image.Pt(c.w, max(c.y-c.gap, 0))}, c.err
}

// rowsLines lays one line per row, plus the failing row's error line under
// it.
func (f *sourceFolders) rowsLines(c *cardStack) {
	for i, row := range f.rows {
		r := row
		idx := i
		c.line(func(y0, y1 int) {
			// The three focusables of one row share its y range: the trap
			// cycles them in draw order (§6.17).
			c.body.bands.Focusable(r.name.Tag(), y0, y1)
			c.body.bands.Focusable(r.access.Tag(), y0, y1)
			c.body.bands.Focusable(r.rm.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutRow(c.env, gtx, idx, r)
		})
		if r.err != "" {
			c.line(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
				l := *gtx
				l.Constraints.Min.X = c.w
				return kit.Paragraph(c.env, &l, theme.TypeSmall(), c.env.Th.Status().Danger, r.err), nil
			})
		}
	}
}

// emptyStateLine is the plain "on this computer" text: no other sources
// exist yet, so there is no dropdown to pick one from.
func (f *sourceFolders) emptyStateLine(env *kit.Env, gtx *layout.Context) (layout.Dimensions, error) {
	h := gtx.Dp(theme.ControlMD)
	dims := kit.Label(env, gtx, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Add a folder on this computer")
	return layout.Dimensions{Size: image.Pt(dims.Size.X, h)}, nil
}

// layoutAffordance draws the list's standing line: the leading widget (the
// empty state's text, when the list is empty) and the trailing fallback link
// and Add button.
func (f *sourceFolders) layoutAffordance(env *kit.Env, gtx *layout.Context, lead func(*kit.Env, *layout.Context) (layout.Dimensions, error)) (layout.Dimensions, error) {
	addProps := kit.ButtonProps{Text: addLabel, Icon: kit.IconFolderPlus, Variant: kit.Secondary, Size: kit.SizeSM, Busy: f.picking}
	revealProps := kit.ButtonProps{Text: "Type a path instead", Variant: kit.Link, Size: kit.SizeSM, Disabled: f.typedOpen}
	addDims := f.add.Measure(env, gtx, addProps)
	var err error
	var children []layout.FlexChild
	if lead != nil {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Min.Y = addDims.Size.Y
			d, lerr := lead(env, &local)
			keepDrawErr(&err, lerr)
			return d
		}))
	}
	if lead != nil || len(f.rows) > 0 {
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, addDims.Size.Y)}
		}))
	}
	children = append(children, f.revealChild(env, revealProps, &err), f.pickChild(env, addProps, &err))
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, children...)
	return dims, err
}

// revealChild is the "Type a path instead" link's flex child: a click opens
// the fallback field, and the link disables itself once it is open.
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

// pickChild is the Add button's flex child: a click asks the window's picker
// for a folder.
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

// rowGeom is one row's line-1 geometry: where the name, the access pick and
// the remove button sit.
type rowGeom struct {
	w, gap, iconW, lineH int
	nameX, nameW         int
	selectX, selectW     int
	rmX, rmW             int
}

// layoutRow draws one folder row: the folder glyph, the inline-editable name,
// the access pick and the remove button on the first line, the muted
// middle-truncated path under them.
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
	totalH := g.lineH + g.gap/2 + gtx.Sp(theme.TypeSmall().Line)
	return layout.Dimensions{Size: image.Pt(g.w, totalH)}, err
}

// rowGeom measures one row's fixed boxes: the access pick and the remove
// button size to their content, the name field takes the rest.
func (f *sourceFolders) rowGeom(env *kit.Env, gtx *layout.Context, row *sourceRow) rowGeom {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	iconW := gtx.Dp(theme.IconMD)
	selectW := row.access.Measure(env, gtx, &kit.SelectProps{Options: accessOptions(), Value: row.accessSel}).Size.X
	rmW := row.rm.Measure(env, gtx, &kit.IconButtonProps{Icon: kit.IconClose, Label: "Remove " + row.name.Text(), Size: kit.SizeXS}).Size.X
	return rowGeom{
		w: w, gap: gap, iconW: iconW, lineH: gtx.Dp(theme.ControlMD),
		nameX: iconW + gap, nameW: w - iconW - gap - selectW - gap - rmW - gap,
		selectX: w - rmW - gap - selectW, selectW: selectW,
		rmX: w - rmW, rmW: rmW,
	}
}

// rowIcon draws the row's folder glyph, centered on the name field's line.
func (f *sourceFolders) rowIcon(env *kit.Env, gtx *layout.Context, g rowGeom, errp *error) {
	env.Offset(gtx, image.Pt(0, (g.lineH-g.iconW)/2), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(g.iconW, g.iconW), Max: image.Pt(g.iconW, g.iconW)}
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

// rowPath draws the row's absolute path, muted and truncated in the middle.
func (f *sourceFolders) rowPath(env *kit.Env, gtx *layout.Context, row *sourceRow, g rowGeom) {
	env.Offset(gtx, image.Pt(g.iconW+g.gap, g.lineH+g.gap/2), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = 0, g.w-g.iconW-g.gap
		kit.MiddleEllipsisLabel(env, &local, theme.TypeSmall(), env.Th.Chrome().FgMuted, row.path)
	})
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
