package main

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Dialog page (UK-09, ui-kit §6.17): the `form` frame with its fields
// and footer — at rest, with the first field focused, submitting (the busy
// primary) and on its scrim — the `confirm` frame with its danger primary —
// at rest and with the safe choice focused — and a live trigger that opens
// the real dialog on the window's scrim, with Escape, the focus trap and the
// focus return to the opener. Every specimen has its own state, as every
// matrix cell does, and re-opens if a stray click dismissed it, so the
// matrix is stable across frames.

const (
	dlgForm      = "form"
	dlgFormFocus = "form (field focused)"
	dlgFormBusy  = "form (submitting)"
	dlgFormScrim = "form on scrim"
	dlgConfirm   = "confirm"
	dlgSafeFocus = "confirm (safe focused)"
	dlgLive      = "live"
)

// Shared specimen copy, so no literal repeats (goconst).
const (
	labelSave    = "Save"
	dlgConnTitle = "New connection"
	dlgConnDesc  = "The host is validated before the connection is stored."
	dlgFieldName = "Name"
	dlgFieldHint = "e.g. dev-pg"
	dlgDelTitle  = "Delete connection dev-pg?"
	dlgDelDesc   = "The stored credentials go with it. This cannot be undone."
	dlgDelVerb   = "Delete"
	dlgWorkspace = "Workspace name"
)

type dialogPage struct {
	form      [4]*kit.DialogState
	formField [4]*kit.TextFieldState
	scrims    [4]image.Rectangle
	confirm   [2]*kit.DialogState
	confirmB  [2][2]*kit.ButtonState // cancel, primary of each confirm specimen
	trigger   *kit.ButtonState
	live      *kit.DialogState
	liveField *kit.TextFieldState
	liveB     [2]*kit.ButtonState
	cancelled bool
}

func newDialogPage() dialogPage {
	var dp dialogPage
	for i := range dp.form {
		dp.form[i] = kit.NewDialogState()
		dp.formField[i] = kit.NewTextFieldState()
		dp.form[i].Open()
	}
	for i := range dp.confirm {
		dp.confirm[i] = kit.NewDialogState()
		for j := range dp.confirmB[i] {
			dp.confirmB[i][j] = kit.NewButtonState()
		}
		dp.confirm[i].Open()
	}
	for i := range dp.liveB {
		dp.liveB[i] = kit.NewButtonState()
	}
	dp.trigger = kit.NewButtonState()
	dp.live = kit.NewDialogState()
	dp.liveField = kit.NewTextFieldState()
	return dp
}

func (p *pages) layoutDialogs(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Dialog §6.17: form and confirm on a scrim, the frame at radius.xl with a scrollable body on the ScrollArea and a footer of Cancel and one primary. Focus is trapped (Tab and Shift+Tab cycle inside) and returns to the opener; Escape closes only when safe; Enter submits a form, never from a TextArea; a confirm focuses the safe choice; never a dialog on a dialog.") + gtx.Dp(theme.SpaceXL)
	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "specimens", []string{dlgForm, dlgFormFocus},
				[][]cellDraw{{p.dialogFormCell(gtx, 0, 0, dlgForm), p.dialogFormCell(gtx, 1, 1, dlgFormFocus)}}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "submitting and scrim", []string{dlgFormBusy, dlgFormScrim},
				[][]cellDraw{{p.dialogFormCell(gtx, 2, 0, dlgFormBusy), p.dialogFormCell(gtx, 3, 1, dlgFormScrim)}}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "confirm", []string{dlgConfirm, dlgSafeFocus}, [][]cellDraw{p.dialogConfirmRow(gtx)}, dry)
		},
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "interactive", []string{dlgLive}, [][]cellDraw{{p.dialogLiveCell(gtx)}}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

// dialogFormCell is one form specimen cell. The focused cell forces the
// field's ring through the gallery preview (Env.Preview); the submitting
// cell keeps the primary busy; the scrim cell paints the dim around its own
// frame.
func (p *pages) dialogFormCell(gtx *layout.Context, i, col int, st string) cellDraw {
	sp, field := p.dlg.form[i], p.dlg.formField[i]
	margin := gtx.Dp(theme.SpaceLG)
	props := func() *kit.DialogProps {
		return &kit.DialogProps{
			Variant:     kit.DialogForm,
			Title:       dlgConnTitle,
			Description: dlgConnDesc,
			Inline:      true,
			ScrimRect:   p.dlg.scrims[i],
			Cancel:      p.dlg.liveB[0], Primary: p.dlg.liveB[1], PrimaryLabel: labelSave,
			Busy: st == dlgFormBusy,
			Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
				return p.formBody(gtx, field, bands)
			},
		}
	}
	tag := event.Tag(sp)
	if st == dlgFormFocus {
		tag = field.Tag()
	}
	return p.dialogCell(gtx, sp, props, cell{compDialog, 0, 0, st}, tag, col, func(box image.Point) {
		if st == dlgFormScrim {
			// The dim extends around the frame but under nothing painted
			// after it: the column gap to the sides, a sliver up toward the
			// caption, a margin below into the block gap.
			p.dlg.scrims[i] = image.Rect(-margin, -gtx.Dp(theme.SpaceXS),
				box.X+margin, box.Y+gtx.Dp(theme.SpaceLG))
		}
	})
}

// formBody lays the specimen's field at the full body width, declaring its
// band, and carries a draw error out to the page.
func (p *pages) formBody(gtx *layout.Context, field *kit.TextFieldState, bands *kit.DialogBands) image.Point {
	fp := &kit.TextFieldProps{Label: dlgFieldName, Placeholder: dlgFieldHint}
	dims := field.Measure(p.env, gtx, fp)
	if _, err := field.Layout(p.env, gtx, fp); err != nil {
		p.err = err
	}
	bands.Focusable(field.Tag(), 0, dims.Size.Y)
	return image.Point{X: gtx.Constraints.Max.X, Y: dims.Size.Y}
}

// dialogConfirmRow is the confirm's two cells: at rest, and with the safe
// choice focused (the ring forced through the preview).
func (p *pages) dialogConfirmRow(gtx *layout.Context) []cellDraw {
	row := make([]cellDraw, 0, 2)
	for i, st := range []string{dlgConfirm, dlgSafeFocus} {
		sp := p.dlg.confirm[i]
		cancel, primary := p.dlg.confirmB[i][0], p.dlg.confirmB[i][1]
		props := func() *kit.DialogProps {
			return &kit.DialogProps{
				Variant:     kit.DialogConfirm,
				Title:       dlgDelTitle,
				Description: dlgDelDesc,
				Inline:      true,
				Cancel:      cancel, Primary: primary, PrimaryLabel: dlgDelVerb,
				PrimaryVariant: kit.Danger,
			}
		}
		tag := event.Tag(sp)
		if st == dlgSafeFocus {
			tag = cancel.Tag()
		}
		row = append(row, p.dialogCell(gtx, sp, props, cell{compDialog, 0, 0, st}, tag, i, nil))
	}
	return row
}

// dialogCell measures one inline dialog specimen and wraps its layout; a
// specimen a stray click dismissed re-opens, so the matrix stays complete.
// After measuring, the cell expands the specimen's scrim rect around the
// measured frame, so an on-scrim specimen dims its own surroundings.
func (p *pages) dialogCell(gtx *layout.Context, sp *kit.DialogState, props func() *kit.DialogProps, key cell, tag event.Tag, col int, onMeasured func(image.Point)) cellDraw {
	box := measure(gtx, func(gtx *layout.Context) image.Point {
		res, err := sp.Layout(p.env, gtx, props())
		if err != nil {
			p.err = err
		}
		return res.Size
	})
	if onMeasured != nil {
		onMeasured(box)
	}
	return cellDraw{
		col: col, key: key, tag: tag,
		box: box,
		lay: func(gtx *layout.Context) {
			if _, err := sp.Layout(p.env, gtx, props()); err != nil {
				p.err = err
			}
			if !sp.IsOpen() {
				sp.Open()
			}
		},
	}
}

// dialogLiveCell is the live trigger: the button opens the real dialog on the
// window's scrim; the form inside traps focus and Escape returns here.
func (p *pages) dialogLiveCell(gtx *layout.Context) cellDraw {
	btn, d := p.dlg.trigger, p.dlg.live
	field := p.dlg.liveField
	cancel, primary := p.dlg.liveB[0], p.dlg.liveB[1]
	pr := kit.ButtonProps{Text: "Open dialog", Variant: kit.Primary}
	fp := &kit.TextFieldProps{Label: dlgWorkspace, Placeholder: "e.g. backend"}
	props := func() *kit.DialogProps {
		return &kit.DialogProps{
			Variant:     kit.DialogForm,
			Title:       "Create workspace",
			Description: "The host validates the roots before the workspace is stored.",
			Cancel:      cancel, Primary: primary, PrimaryLabel: labelSave,
			Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
				dims := field.Measure(p.env, gtx, fp)
				if _, err := field.Layout(p.env, gtx, fp); err != nil {
					p.err = err
				}
				bands.Focusable(field.Tag(), 0, dims.Size.Y)
				return image.Point{X: gtx.Constraints.Max.X, Y: dims.Size.Y}
			},
		}
	}
	box := measure(gtx, func(gtx *layout.Context) image.Point { return btn.Measure(p.env, gtx, pr).Size })
	return cellDraw{
		col: 0, key: cell{compDialog, 1, 0, dlgLive}, tag: btn.Tag(), box: box,
		lay: func(gtx *layout.Context) {
			clicked, err := btn.Layout(p.env, gtx, pr)
			if err != nil {
				p.err = err
			}
			if clicked {
				d.Open()
			}
			d.SetOpener(btn.Tag())
			res, err := d.Layout(p.env, gtx, props())
			if err != nil {
				p.err = err
			}
			if res.Cancelled {
				p.dlg.cancelled = true
			}
			if res.Submitted {
				p.dlg.cancelled = false
			}
		},
	}
}
