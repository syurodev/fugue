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
		i := ci
		po := p.pop.specimens[i]
		pinned := st == povPinned || p.pop.pinned[i]
		withPin := st != povPlain
		field := p.pop.fields[i]
		props := kit.PopoverProps{
			Anchor: image.Rectangle{},
			Title:  "Filter",
			Width:  200,
			Pinned: pinned,
			Inline: true,
			Pin: func() *kit.IconButtonState {
				if withPin {
					return p.pop.pins[i]
				}
				return nil
			}(),
			Measure: func(gtx *layout.Context) image.Point {
				return field.Measure(p.env, gtx, &kit.TextFieldProps{Label: "Name", Placeholder: "e.g. dev-pg"}).Size
			},
			Content: func(gtx *layout.Context) {
				_, _ = field.Layout(p.env, gtx, &kit.TextFieldProps{Label: "Name", Placeholder: "e.g. dev-pg"})
			},
		}
		row = append(row, cellDraw{
			col: ci, key: cell{compPopover, 0, 0, st}, tag: po,
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
		})
	}
	return row
}

// popoverLiveCell is the live trigger: the button opens the real popover on
// the window overlay, a field inside takes keyboard focus, Escape or the
// close button returns focus here.
func (p *pages) popoverLiveCell(gtx *layout.Context, col int) cellDraw {
	btn, po := p.pop.trigger, p.pop.live
	field := p.pop.liveField
	pr := kit.ButtonProps{Text: "Open popover", Icon: kit.IconChevronDown, Variant: kit.Bordered}
	fp := &kit.TextFieldProps{Label: "Session name", Placeholder: "e.g. refactor-auth"}
	props := kit.PopoverProps{
		Anchor:      image.Rectangle{},
		Title:       "Popover on the overlay",
		Width:       240,
		Pinned:      p.pop.livePinned,
		FocusOnOpen: field.Tag(),
		Pin:         p.pop.livePin,
		Measure: func(gtx *layout.Context) image.Point {
			return field.Measure(p.env, gtx, fp).Size
		},
		Content: func(gtx *layout.Context) {
			ev, _ := field.Layout(p.env, gtx, fp)
			if ev.Changed {
				p.pop.liveVal = field.Text()
			}
		},
	}
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
			res, err := po.Layout(p.env, gtx, kit.PopoverProps{
				Anchor:      image.Rectangle{Max: box},
				Title:       props.Title,
				Width:       props.Width,
				Pinned:      props.Pinned,
				FocusOnOpen: props.FocusOnOpen,
				Pin:         props.Pin,
				Measure:     props.Measure,
				Content:     props.Content,
			})
			if err != nil {
				p.err = err
			}
			if res.PinClicked {
				p.pop.livePinned = !p.pop.livePinned
			}
		},
	}
}
