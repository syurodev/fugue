package main

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Dialog page (UK-09, ui-kit §6.17): the `form` frame with its fields —
// at rest and with the first field focused — the `confirm` frame with its
// danger primary — at rest and with the safe choice focused — and a live
// trigger that opens the real dialog on the window's scrim, with Escape, the
// focus trap and the focus return to the opener. Every specimen has its own
// state, as every matrix cell does, and re-opens if a stray click dismissed
// it, so the matrix is stable across frames.

const (
	dlgForm      = "form"
	dlgFormFocus = "form (field focused)"
	dlgConfirm   = "confirm"
	dlgSafeFocus = "confirm (safe focused)"
	dlgLive      = "live"
)

type dialogPage struct {
	form      [2]*kit.DialogState
	formField [2]*kit.TextFieldState
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
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Dialog §6.17: form and confirm on a scrim, the frame at radius.xl with a scrollable body. Focus is trapped (Tab and Shift+Tab cycle inside) and returns to the opener; Escape closes only when safe; Enter submits a form, never from a TextArea; a confirm focuses the safe choice; never a dialog on a dialog.") + gtx.Dp(theme.SpaceXL)
	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "specimens", []string{dlgForm, dlgFormFocus}, [][]cellDraw{p.dialogFormRow(gtx)}, dry)
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

// dialogFormRow is the form's two cells. The focused cell forces the first
// field's ring through the gallery preview (Env.Preview).
func (p *pages) dialogFormRow(gtx *layout.Context) []cellDraw {
	row := make([]cellDraw, 0, 2)
	for i, st := range []string{dlgForm, dlgFormFocus} {
		sp, field := p.dlg.form[i], p.dlg.formField[i]
		props := func() kit.DialogProps {
			return kit.DialogProps{
				Variant:     kit.DialogForm,
				Title:       "New connection",
				Description: "The host is validated before the connection is stored.",
				Inline:      true,
				FocusOrder:  []event.Tag{field.Tag()},
				Cancel:      p.dlg.liveB[0], Primary: p.dlg.liveB[1], PrimaryLabel: "Save",
				Body: func(gtx *layout.Context) image.Point {
					fp := &kit.TextFieldProps{Label: "Name", Placeholder: "e.g. dev-pg"}
					dims := field.Measure(p.env, gtx, fp)
					_, _ = field.Layout(p.env, gtx, fp)
					return image.Pt(gtx.Constraints.Max.X, dims.Size.Y)
				},
			}
		}
		tag := event.Tag(sp)
		if st == dlgFormFocus {
			tag = field.Tag()
		}
		box := measure(gtx, func(gtx *layout.Context) image.Point {
			res, err := sp.Layout(p.env, gtx, props())
			if err != nil {
				p.err = err
			}
			return res.Size
		})
		row = append(row, cellDraw{
			col: i, key: cell{compDialog, 0, 0, st}, tag: tag,
			box: box,
			lay: func(gtx *layout.Context) {
				if _, err := sp.Layout(p.env, gtx, props()); err != nil {
					p.err = err
				}
				if !sp.IsOpen() {
					sp.Open()
				}
			},
		})
	}
	return row
}

// dialogConfirmRow is the confirm's two cells: at rest, and with the safe
// choice focused (the ring forced through the preview).
func (p *pages) dialogConfirmRow(gtx *layout.Context) []cellDraw {
	row := make([]cellDraw, 0, 2)
	for i, st := range []string{dlgConfirm, dlgSafeFocus} {
		sp := p.dlg.confirm[i]
		cancel, primary := p.dlg.confirmB[i][0], p.dlg.confirmB[i][1]
		props := func() kit.DialogProps {
			return kit.DialogProps{
				Variant:     kit.DialogConfirm,
				Title:       "Delete connection dev-pg?",
				Description: "The stored credentials go with it. This cannot be undone.",
				Inline:      true,
				Cancel:      cancel, Primary: primary, PrimaryLabel: "Delete",
				PrimaryVariant: kit.Danger,
			}
		}
		tag := event.Tag(sp)
		if st == dlgSafeFocus {
			tag = cancel.Tag()
		}
		box := measure(gtx, func(gtx *layout.Context) image.Point {
			res, err := sp.Layout(p.env, gtx, props())
			if err != nil {
				p.err = err
			}
			return res.Size
		})
		row = append(row, cellDraw{
			col: i, key: cell{compDialog, 0, 0, st}, tag: tag,
			box: box,
			lay: func(gtx *layout.Context) {
				if _, err := sp.Layout(p.env, gtx, props()); err != nil {
					p.err = err
				}
				if !sp.IsOpen() {
					sp.Open()
				}
			},
		})
	}
	return row
}

// dialogLiveCell is the live trigger: the button opens the real dialog on the
// window's scrim; the form inside traps focus and Escape returns here.
func (p *pages) dialogLiveCell(gtx *layout.Context) cellDraw {
	btn, d := p.dlg.trigger, p.dlg.live
	field := p.dlg.liveField
	cancel, primary := p.dlg.liveB[0], p.dlg.liveB[1]
	pr := kit.ButtonProps{Text: "Open dialog", Variant: kit.Primary}
	fp := &kit.TextFieldProps{Label: "Workspace name", Placeholder: "e.g. backend"}
	props := func() kit.DialogProps {
		return kit.DialogProps{
			Variant:     kit.DialogForm,
			Title:       "Create workspace",
			Description: "The host validates the roots before the workspace is stored.",
			FocusOrder:  []event.Tag{field.Tag()},
			Cancel:      cancel, Primary: primary, PrimaryLabel: "Create",
			Body: func(gtx *layout.Context) image.Point {
				dims := field.Measure(p.env, gtx, fp)
				_, _ = field.Layout(p.env, gtx, fp)
				return image.Pt(gtx.Constraints.Max.X, dims.Size.Y)
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
