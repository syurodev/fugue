package appwin

import (
	"errors"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The source-folder list's drawing (FUG-115): the section heading, the card
// with its rows and affordance line, the typed-path fallback area, and the
// band declarations the dialog's focus trap cycles through. The list's state
// and rules live in sourcefolders.go.

// layout draws the section: the heading and the box with its rows, the
// affordance line and the fallback area. It declares each focusable control's
// band in draw order (§6.17).
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

// cardBody lays the box's lines out at a manual y, the way the dialog body
// does, so each row's controls know their band's y range. The card's padding
// already insets this body, and the card field's y plus that padding places
// every band in body coordinates.
func (f *sourceFolders) cardBody(env *kit.Env, gtx *layout.Context, body *wsBody) (layout.Dimensions, error) {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	pad := gtx.Dp(theme.SpaceLG)
	cardY := body.y
	y := 0
	var err error
	line := func(band func(y0, y1 int), draw func(gtx *layout.Context) (layout.Dimensions, error)) layout.Dimensions {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = w, w
		var dims layout.Dimensions
		var lerr error
		env.Offset(&local, image.Pt(0, y), func(gtx *layout.Context) {
			dims, lerr = draw(gtx)
		})
		if lerr != nil && err == nil {
			err = lerr
		}
		if band != nil {
			band(cardY+pad+y, cardY+pad+y+dims.Size.Y)
		}
		y += dims.Size.Y + gap
		return dims
	}

	if len(f.rows) == 0 {
		line(func(y0, y1 int) { body.bands.Focusable(f.add.Tag(), y0, y1) },
			func(gtx *layout.Context) (layout.Dimensions, error) {
				return f.layoutAffordance(env, gtx, f.emptyStateLine)
			})
	}
	for i, row := range f.rows {
		r := row
		idx := i
		line(func(y0, y1 int) {
			// The three focusables of one row share its y range: the trap
			// cycles them in draw order (§6.17).
			body.bands.Focusable(r.name.Tag(), y0, y1)
			body.bands.Focusable(r.access.Tag(), y0, y1)
			body.bands.Focusable(r.rm.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutRow(env, gtx, idx, r)
		})
		if r.err != "" {
			line(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
				l := *gtx
				l.Constraints.Min.X = w
				return kit.Paragraph(env, &l, theme.TypeSmall(), env.Th.Status().Danger, r.err), nil
			})
		}
	}

	line(func(y0, y1 int) {
		body.bands.Focusable(f.reveal.Tag(), y0, y1)
		body.bands.Focusable(f.add.Tag(), y0, y1)
	}, func(gtx *layout.Context) (layout.Dimensions, error) {
		return f.layoutAffordance(env, gtx, nil)
	})
	if f.typedOpen {
		line(func(y0, y1 int) {
			body.bands.Focusable(f.typed.Tag(), y0, y1)
			body.bands.Focusable(f.typedAdd.Tag(), y0, y1)
		}, func(gtx *layout.Context) (layout.Dimensions, error) {
			return f.layoutTyped(env, gtx)
		})
	}
	return layout.Dimensions{Size: image.Pt(w, max(y-gap, 0))}, err
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
	addProps := kit.ButtonProps{
		Text: "Add", Icon: kit.IconFolderPlus, Variant: kit.Secondary, Size: kit.SizeSM, Busy: f.picking,
	}
	revealProps := kit.ButtonProps{
		Text: "Type a path instead", Variant: kit.Link, Size: kit.SizeSM, Disabled: f.typedOpen,
	}
	addDims := f.add.Measure(env, gtx, addProps)
	var err error
	var leadDims layout.Dimensions
	if lead != nil {
		leadDims, err = lead(env, gtx)
		if err != nil {
			return layout.Dimensions{}, err
		}
	}
	children := make([]layout.FlexChild, 0, 3)
	if lead != nil {
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Min.Y = addDims.Size.Y
			d, lerr := lead(env, &local)
			if lerr != nil && err == nil {
				err = lerr
			}
			return d
		}))
	}
	if leadDims.Size.X > 0 || len(f.rows) > 0 {
		children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, addDims.Size.Y)}
		}))
	}
	children = append(children,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			revealed, lerr := f.reveal.Layout(env, &local, revealProps)
			if lerr != nil && err == nil {
				err = lerr
			}
			if revealed {
				f.revealTyped()
			}
			return f.reveal.Measure(env, &local, revealProps)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			clicked, lerr := f.add.Layout(env, &local, addProps)
			if lerr != nil && err == nil {
				err = lerr
			}
			if clicked {
				f.pick()
			}
			return f.add.Measure(env, &local, addProps)
		}),
	)
	dims := layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, children...)
	return dims, err
}

// layoutRow draws one folder row: the folder glyph, the inline-editable name,
// the access pick and the remove button on the first line, the muted
// middle-truncated path under them.
func (f *sourceFolders) layoutRow(env *kit.Env, gtx *layout.Context, i int, row *sourceRow) (layout.Dimensions, error) {
	w := gtx.Constraints.Max.X
	gap := gtx.Dp(theme.SpaceMD)
	selectProps := &kit.SelectProps{
		Options: []kit.SelectOption{{ID: defaultAccess, Label: "read and write"}, {ID: client.RootAccessRO, Label: "read only"}},
		Value:   row.accessSel,
	}
	selectDims := row.access.Measure(env, gtx, selectProps)
	rmProps := &kit.IconButtonProps{Icon: kit.IconClose, Label: "Remove " + row.name.Text(), Size: kit.SizeXS}
	rmDims := row.rm.Measure(env, gtx, rmProps)
	iconW := gtx.Dp(theme.IconMD)
	lineH := gtx.Dp(theme.ControlMD)
	nameW := w - iconW - gap - selectDims.Size.X - gap - rmDims.Size.X - gap
	nameProps := &kit.TextFieldProps{Placeholder: "Name the root"}

	var err error
	// Line 1: icon, name field, access select, remove button.
	env.Offset(gtx, image.Pt(0, (lineH-iconW)/2), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(iconW, iconW), Max: image.Pt(iconW, iconW)}
		if _, ierr := env.Icon(gtx, kit.IconFolder, theme.IconMD, env.Th.Chrome().FgMuted); ierr != nil {
			err = ierr
		}
	})
	env.Offset(gtx, image.Pt(iconW+gap, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = nameW, nameW
		if _, nerr := row.name.Layout(env, &local, nameProps); nerr != nil {
			err = nerr
		}
	})
	env.Offset(gtx, image.Pt(iconW+gap+nameW+gap, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = selectDims.Size.X, selectDims.Size.X
		res, serr := row.access.Layout(env, &local, selectProps)
		if serr == nil && res.Changed {
			row.accessSel = res.Value
		}
		if serr != nil && err == nil {
			err = serr
		}
	})
	env.Offset(gtx, image.Pt(w-rmDims.Size.X, (lineH-rmDims.Size.Y)/2), func(gtx *layout.Context) {
		local := *gtx
		clicked, rerr := row.rm.Layout(env, &local, rmProps)
		if rerr != nil && err == nil {
			err = rerr
		}
		if clicked {
			if f.onRemove != nil {
				f.onRemove(i)
			} else {
				f.remove(i)
			}
		}
	})

	// Line 2: the folder's absolute path, muted and truncated in the middle.
	env.Offset(gtx, image.Pt(iconW+gap, lineH+gap/2), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = 0, w-iconW-gap
		kit.MiddleEllipsisLabel(env, &local, theme.TypeSmall(), env.Th.Chrome().FgMuted, row.path)
	})
	totalH := lineH + gap/2 + gtx.Sp(theme.TypeSmall().Line)
	return layout.Dimensions{Size: image.Pt(w, totalH)}, err
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
		if terr != nil {
			err = terr
		}
	})
	// The button shares the field's box line: the label and its gap sit it
	// that far above the box.
	boxY := gtx.Sp(theme.TypeSmall().Line) + gtx.Dp(theme.SpaceXS)
	env.Offset(gtx, image.Pt(fieldW+gap, y+boxY), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints.Min.X, local.Constraints.Max.X = w-fieldW-gap, w-fieldW-gap
		clicked, aerr := f.typedAdd.Layout(env, &local, kit.ButtonProps{
			Text: "Add", Variant: kit.Secondary, Size: kit.SizeMD, Block: true,
		})
		if aerr != nil && err == nil {
			err = aerr
		}
		if clicked {
			f.addTyped()
		}
	})
	dims := f.typed.Measure(env, gtx, props)
	return layout.Dimensions{Size: image.Pt(w, y+dims.Size.Y)}, err
}
