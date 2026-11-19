package main

import (
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	stPending = "pending"
	stError   = "error"
)

type controlsPage struct {
	checks   [3][5]*kit.CheckboxState
	liveChk  *kit.CheckboxState
	liveVal  kit.CheckState
	switches [2][7]*kit.SwitchState
	liveSw   *kit.SwitchState
	liveOn   bool
	segs     [5]*kit.SegmentedState
	liveSeg  *kit.SegmentedState
	segIdx   int
	cards    [5]*kit.CardState
	liveCard *kit.CardState
}

func newControlsPage() controlsPage {
	var cp controlsPage
	for v := range cp.checks {
		for s := range cp.checks[v] {
			cp.checks[v][s] = kit.NewCheckboxState()
		}
	}
	cp.liveChk = kit.NewCheckboxState()
	for v := range cp.switches {
		for s := range cp.switches[v] {
			cp.switches[v][s] = kit.NewSwitchState()
		}
	}
	cp.liveSw = kit.NewSwitchState()
	for i := range cp.segs {
		cp.segs[i] = kit.NewSegmentedState()
	}
	cp.liveSeg = kit.NewSegmentedState()
	for i := range cp.cards {
		cp.cards[i] = kit.NewCardState()
	}
	cp.liveCard = kit.NewCardState()
	return cp
}

func checkboxStates() []string {
	return []string{stRest, stHover, stPressed, stFocus, stDisabled}
}

func switchStates() []string {
	return []string{stRest, stHover, stPressed, stFocus, stDisabled, stPending, stError}
}

func segmentedStates() []string {
	return []string{stRest, stFocus, stDisabled}
}

func cardStates() []string {
	return []string{stRest, stHover, stPressed, stFocus, stDisabled}
}

func (p *pages) layoutControls(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "Checkbox §6.10, Switch §6.11, SegmentedControl §6.12, Card §6.19: static state matrices via Env.Preview plus live interactive specimens.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{
		p.checkboxMatrixBlock,
		p.switchMatrixBlock,
		p.segmentedBlock,
		p.cardBlock,
	})
}

func (p *pages) checkboxMatrixBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	vals := []kit.CheckState{kit.CheckOff, kit.CheckOn, kit.CheckMixed}
	names := []string{"off", "on", "mixed"}
	states := checkboxStates()

	x := 0
	maxH := 0
	for vi, val := range vals {
		y := line + gap
		colW := 0
		for si, st := range states {
			pr := kit.CheckboxProps{Label: names[vi] + " " + st, Value: val, Disabled: st == stDisabled}
			sz := p.placeCheckboxCell(gtx, dry, vi, si, st, x, y, pr)
			colW = max(colW, sz.X)
			y += sz.Y + gap
		}
		x += colW + gap
		maxH = max(maxH, y)
	}
	livePr := kit.CheckboxProps{Label: "Live (click or Space)", Value: p.ctrl.liveVal}
	lsz := p.ctrl.liveChk.Measure(p.env, gtx, livePr).Size
	if !dry {
		p.caption(gtx, image.Point{}, "Checkbox (off / on / mixed)", c.FgMuted)
		cd := cellDraw{
			key: cell{compCheckbox, 3, 0, cbLive},
			box: lsz,
			lay: func(gtx *layout.Context) {
				if ch, next, err := p.ctrl.liveChk.Layout(p.env, gtx, livePr); err != nil {
					p.err = err
				} else if ch {
					p.ctrl.liveVal = next
				}
			},
		}
		p.place(gtx, image.Pt(0, maxH), &cd)
	}
	maxW := max(x-gap, lsz.X, p.captionWidth(gtx, "Checkbox (off / on / mixed)"))
	return image.Pt(maxW, maxH+lsz.Y)
}

func (p *pages) placeCheckboxCell(gtx *layout.Context, dry bool, vi, si int, st string, x, y int, pr kit.CheckboxProps) image.Point {
	sz := p.ctrl.checks[vi][si].Measure(p.env, gtx, pr).Size
	if !dry {
		cd := cellDraw{
			key: cell{compCheckbox, vi, 0, st},
			tag: p.ctrl.checks[vi][si].Tag(),
			box: sz,
			lay: func(gtx *layout.Context) {
				if _, _, err := p.ctrl.checks[vi][si].Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		}
		p.place(gtx, image.Pt(x, y), &cd)
	}
	return sz
}

func (p *pages) switchMatrixBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	states := switchStates()

	x := 0
	maxH := 0
	for vi, checked := range []bool{false, true} {
		prefix := "off"
		if checked {
			prefix = "on"
		}
		y := line + gap
		colW := 0
		for si, st := range states {
			pr := switchCellProps(prefix, st, checked)
			sz := p.placeSwitchCell(gtx, dry, vi, si, st, x, y, pr)
			colW = max(colW, sz.X)
			y += sz.Y + gap
		}
		x += colW + gap
		maxH = max(maxH, y)
	}
	livePr := kit.SwitchProps{Label: "Live switch", Checked: p.ctrl.liveOn}
	lsz := p.ctrl.liveSw.Measure(p.env, gtx, livePr).Size
	if !dry {
		p.caption(gtx, image.Point{}, "Switch (off / on + pending / error)", c.FgMuted)
		cd := cellDraw{
			key: cell{compSwitch, 2, 0, cbLive},
			box: lsz,
			lay: func(gtx *layout.Context) {
				if ch, err := p.ctrl.liveSw.Layout(p.env, gtx, livePr); err != nil {
					p.err = err
				} else if ch {
					p.ctrl.liveOn = !p.ctrl.liveOn
				}
			},
		}
		p.place(gtx, image.Pt(0, maxH), &cd)
	}
	maxW := max(x-gap, lsz.X, p.captionWidth(gtx, "Switch (off / on + pending / error)"))
	return image.Pt(maxW, maxH+lsz.Y)
}

func switchCellProps(prefix, st string, checked bool) kit.SwitchProps {
	status := kit.SwitchIdle
	switch st {
	case stPending:
		status = kit.SwitchPending
	case stError:
		status = kit.SwitchError
	}
	return kit.SwitchProps{Label: prefix + " " + st, Checked: checked, Status: status, Disabled: st == stDisabled}
}

func (p *pages) placeSwitchCell(gtx *layout.Context, dry bool, vi, si int, st string, x, y int, pr kit.SwitchProps) image.Point {
	sz := p.ctrl.switches[vi][si].Measure(p.env, gtx, pr).Size
	if !dry {
		cd := cellDraw{
			key: cell{compSwitch, vi, 0, st},
			tag: p.ctrl.switches[vi][si].Tag(),
			box: sz,
			lay: func(gtx *layout.Context) {
				if _, err := p.ctrl.switches[vi][si].Layout(p.env, gtx, pr); err != nil {
					p.err = err
				}
			},
		}
		p.place(gtx, image.Pt(x, y), &cd)
	}
	return sz
}

func (p *pages) segmentedBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	opts := []kit.SegmentedOption{
		{ID: "day", Label: "Day"},
		{ID: "week", Label: "Week"},
		{ID: "month", Label: "Month"},
	}
	states := segmentedStates()

	y := line + gap
	maxW := 0
	for si, st := range states {
		pr := kit.SegmentedProps{Options: opts, Selected: si % len(opts), Disabled: st == stDisabled}
		sz := p.ctrl.segs[si].Measure(p.env, gtx, pr).Size
		if !dry {
			cd := cellDraw{
				key: cell{compSegmented, 0, 0, st},
				tag: p.ctrl.segs[si].Tag(),
				box: sz,
				lay: func(gtx *layout.Context) {
					if _, _, err := p.ctrl.segs[si].Layout(p.env, gtx, pr); err != nil {
						p.err = err
					}
				},
			}
			p.place(gtx, image.Pt(0, y), &cd)
		}
		maxW = max(maxW, sz.X)
		y += sz.Y + gap
	}
	livePr := kit.SegmentedProps{Options: opts, Selected: p.ctrl.segIdx}
	lsz := p.ctrl.liveSeg.Measure(p.env, gtx, livePr).Size
	if !dry {
		p.caption(gtx, image.Point{}, "SegmentedControl", c.FgMuted)
		cd := cellDraw{
			key: cell{compSegmented, 1, 0, cbLive},
			box: lsz,
			lay: func(gtx *layout.Context) {
				if ch, next, err := p.ctrl.liveSeg.Layout(p.env, gtx, livePr); err != nil {
					p.err = err
				} else if ch {
					p.ctrl.segIdx = next
				}
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	maxW = max(maxW, lsz.X, p.captionWidth(gtx, "Switch (off / on + pending / error)"))
	return image.Pt(maxW, y+lsz.Y)
}

func (p *pages) cardBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := gtx.Dp(unit.Dp(240))
	states := cardStates()

	y := line + gap
	for si, st := range states {
		pr := kit.CardProps{Clickable: true, Disabled: st == stDisabled}
		body := func(gtx layout.Context) layout.Dimensions {
			return kit.Label(p.env, &gtx, theme.TypeBody(), c.Fg, "Card "+st)
		}
		sz := measure(gtx, func(gtx *layout.Context) image.Point {
			g := *gtx
			g.Constraints.Max.X = w
			d, _, err := p.ctrl.cards[si].Layout(p.env, &g, pr, body)
			if err != nil {
				p.err = err
			}
			return d.Size
		})
		if !dry {
			cd := cellDraw{
				key: cell{compCard, 0, 0, st},
				tag: p.ctrl.cards[si].Tag(),
				box: sz,
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints.Max.X = w
					if _, _, err := p.ctrl.cards[si].Layout(p.env, &g, pr, body); err != nil {
						p.err = err
					}
				},
			}
			p.place(gtx, image.Pt(0, y), &cd)
		}
		y += sz.Y + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "Card (clickable)", c.FgMuted)
	}
	return image.Pt(w, y)
}
