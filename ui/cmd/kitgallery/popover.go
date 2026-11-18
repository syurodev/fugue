package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Popover page (UK-09, ui-kit §6.16): inline specimens of the panel —
// plain, with the pin toggle, pinned — and a live trigger that opens the real
// popover on the window overlay, anchored under the trigger, with a field in
// it. The specimens re-open if a stray click closed them, so the matrix is
// stable across frames.

const (
	povPlain  = "plain"
	povPin    = "pin toggle"
	povPinned = "pinned"
	povLive   = "live"
)

func popoverStates() []string { return []string{povPlain, povPin, povPinned, povLive} }

// Shared specimen copy, so no literal repeats (goconst).
const (
	povTitle      = "Filter"
	povLiveTitle  = "Popover on the overlay"
	povFieldLbl   = "Session name"
	povFieldHint  = "e.g. refactor-auth"
	povSpecLabel  = "Name"
	povSpecHint   = dlgFieldHint
	povTriggerLbl = "Open popover"
)

type popoverPage struct {
	specimens  [3]*kit.PopoverState
	pins       [3]*kit.IconButtonState
	fields     [3]*kit.TextFieldState
	pinned     [3]bool
	trigger    *kit.ButtonState
	live       *kit.PopoverState
	liveField  *kit.TextFieldState
	livePin    *kit.IconButtonState
	livePinned bool
	liveVal    string
}

func newPopoverPage() popoverPage {
	var pp popoverPage
	for i := range pp.specimens {
		pp.specimens[i] = kit.NewPopoverState()
		pp.pins[i] = kit.NewIconButtonState()
		pp.fields[i] = kit.NewTextFieldState()
		pp.specimens[i].Open()
	}
	pp.trigger = kit.NewButtonState()
	pp.live = kit.NewPopoverState()
	pp.liveField = kit.NewTextFieldState()
	pp.livePin = kit.NewIconButtonState()
	return pp
}

func (p *pages) layoutPopovers(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Popover §6.16: the menu panel's shape and motion (SurfaceRaised, radius.xl, elevation.float), a header with a close button, an optional pin toggle. Escape and a click outside close it; a pinned popover ignores the click outside. Opened from the keyboard it takes focus; closing returns it to the opener.") + gtx.Dp(theme.SpaceXL)
	blocks := []block{
		func(gtx *layout.Context, dry bool) image.Point {
			return p.matrix(gtx, "specimens", popoverStates(), [][]cellDraw{p.popoverRow(gtx)}, dry)
		},
	}
	p.flow(gtx, y, blocks)
}

// popoverRow is the row of specimen cells and the live trigger. A specimen
// closed by a stray click re-opens, so the matrix stays complete.
func (p *pages) popoverRow(gtx *layout.Context) []cellDraw {
	states := popoverStates()
	row := make([]cellDraw, 0, len(states))
	for ci, st := range states {
		if st == povLive {
			row = append(row, p.popoverLiveCell(gtx, ci))
			continue
		}
		row = append(row, p.popoverSpecimenCell(gtx, ci, st))
	}
	return row
}

// popoverSpecimenCell is one inline specimen: plain, with the pin toggle, or
// pinned.
func (p *pages) popoverSpecimenCell(gtx *layout.Context, i int, st string) cellDraw {
	po := p.pop.specimens[i]
	pinned := st == povPinned || p.pop.pinned[i]
	withPin := st != povPlain
	field := p.pop.fields[i]
	var pin *kit.IconButtonState
	if withPin {
		pin = p.pop.pins[i]
	}
	fp := &kit.TextFieldProps{Label: povSpecLabel, Placeholder: povSpecHint}
	props := &kit.PopoverProps{
		Title:  povTitle,
		Width:  200,
		Pinned: pinned,
		Inline: true,
		Pin:    pin,
		Measure: func(gtx *layout.Context) image.Point {
			return field.Measure(p.env, gtx, fp).Size
		},
		Content: func(gtx *layout.Context) {
			if _, err := field.Layout(p.env, gtx, fp); err != nil {
				p.err = err
			}
		},
	}
	return cellDraw{
		col: i, key: cell{compPopover, 0, 0, st}, tag: po,
		box: measure(gtx, func(gtx *layout.Context) image.Point {
			res, err := po.Layout(p.env, gtx, props)
			if err != nil {
				p.err = err
			}
			return res.Size
		}),
		lay: func(gtx *layout.Context) {
			res, err := po.Layout(p.env, gtx, props)
			if err != nil {
				p.err = err
			}
			if res.PinClicked {
				p.pop.pinned[i] = !p.pop.pinned[i]
			}
			if !po.IsOpen() {
				po.Open()
			}
		},
	}
}

// popoverLiveCell is the live trigger: the button opens the real popover on
// the window overlay, a field inside takes keyboard focus, Escape or the
// close button returns focus here.
func (p *pages) popoverLiveCell(gtx *layout.Context, col int) cellDraw {
	btn, po := p.pop.trigger, p.pop.live
	pr := kit.ButtonProps{Text: povTriggerLbl, Icon: kit.IconChevronDown, Variant: kit.Bordered}
	props := p.popoverLiveProps()
	box := measure(gtx, func(gtx *layout.Context) image.Point { return btn.Measure(p.env, gtx, pr).Size })
	return cellDraw{
		col: col, key: cell{compPopover, 1, 0, povLive}, tag: btn.Tag(), box: box,
		lay: func(gtx *layout.Context) {
			clicked, err := btn.Layout(p.env, gtx, pr)
			if err != nil {
				p.err = err
			}
			if clicked {
				if btn.KeyClicked() {
					po.OpenKeyboard()
				} else {
					po.Open()
				}
			}
			po.SetOpener(btn.Tag())
			overlayProps := *props
			overlayProps.Anchor = image.Rectangle{Max: box}
			overlayProps.Inline = false
			res, err := po.Layout(p.env, gtx, &overlayProps)
			if err != nil {
				p.err = err
			}
			if res.PinClicked {
				p.pop.livePinned = !p.pop.livePinned
			}
		},
	}
}

// popoverLiveProps are the live popover's props: its field content and the
// pin state, shared by the measure and the overlay layout.
func (p *pages) popoverLiveProps() *kit.PopoverProps {
	field := p.pop.liveField
	fp := &kit.TextFieldProps{Label: povFieldLbl, Placeholder: povFieldHint}
	return &kit.PopoverProps{
		Title:       povLiveTitle,
		Width:       240,
		Pinned:      p.pop.livePinned,
		FocusOnOpen: field.Tag(),
		Pin:         p.pop.livePin,
		Measure: func(gtx *layout.Context) image.Point {
			return field.Measure(p.env, gtx, fp).Size
		},
		Content: func(gtx *layout.Context) {
			ev, err := field.Layout(p.env, gtx, fp)
			if err != nil {
				p.err = err
			}
			if ev.Changed {
				p.pop.liveVal = field.Text()
			}
		},
	}
}
