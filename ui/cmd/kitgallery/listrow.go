package main

import (
	"image"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	lrSelected = "selected"
	lrDragging = "dragging"
	lrLive     = "live"
)

func listRowStates() []string {
	return []string{stRest, stHover, lrSelected, stFocus, lrDragging, stDisabled}
}

type listRowPage struct {
	specs [6]*kit.ListRowState
	live  *kit.ListState
}

func newListRowPage() listRowPage {
	var specs [6]*kit.ListRowState
	for i := range specs {
		specs[i] = kit.NewListRowState()
	}
	live := kit.NewListState()
	live.SetSelected(0)
	return listRowPage{specs: specs, live: live}
}

func (p *pages) layoutListRows(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "ListRow §6.20: size.row height, 4 dp inset radius.md pills, sliding SelectedWash, Snappy hover, actions on hover/focus, status dot (never row background), and kit.Menu context menu.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{p.listRowSpecimensBlock, p.listRowLiveBlock})
}

func (p *pages) listRowSpecimensBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	rowW := gtx.Dp(unit.Dp(360))
	rowH := gtx.Dp(theme.Row)
	y := line + gap
	states := listRowStates()
	for i, st := range states {
		rs := p.list.specs[i]
		pr := kit.ListRowProps{
			Icon:       kit.IconSearch,
			Label:      "Session " + st,
			Secondary:  "claude-sonnet",
			Trailing:   "2m",
			StateLabel: "running",
			StateTone:  kit.ToneInfo,
			Actions:    []kit.ListAction{{ID: "clone", Icon: kit.IconCopy, Label: "Clone"}},
			Selected:   st == lrSelected,
			Draggable:  st == lrDragging,
			Dragging:   st == lrDragging,
			Disabled:   st == stDisabled,
		}
		if !dry {
			p.caption(gtx, image.Pt(0, y), st, c.FgSubtle)
			cd := cellDraw{
				key: cell{compListRow, 0, 0, st},
				tag: rs.Tag(),
				box: image.Pt(rowW, rowH),
				lay: func(gtx *layout.Context) {
					g := *gtx
					g.Constraints = layout.Exact(image.Pt(rowW, rowH))
					rs.Layout(p.env, &g, &pr)
				},
			}
			p.place(gtx, image.Pt(0, y+line+gtx.Dp(theme.SpaceXS)), &cd)
		}
		y += line + gtx.Dp(theme.SpaceXS) + rowH + gap
	}
	if !dry {
		p.caption(gtx, image.Point{}, "states", c.FgMuted)
	}
	return image.Pt(rowW, y)
}

func (p *pages) listRowLiveBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceMD)
	rowW := gtx.Dp(unit.Dp(360))
	rowH := gtx.Dp(theme.Row)
	items := []kit.ListItem{
		{Props: kit.ListRowProps{Icon: kit.IconCheck, Label: "Main workspace", Secondary: "fugue-harness", Trailing: "now", StateLabel: "ok", StateTone: kit.ToneSuccess, Actions: []kit.ListAction{{ID: "restart", Icon: kit.IconRestart, Label: "Restart"}}, ContextMenu: []kit.MenuItem{{ID: "inspect", Label: "Inspect"}, {ID: "restart", Label: "Restart"}}}},
		{Props: kit.ListRowProps{Icon: kit.IconSearch, Label: "Vendor session", Secondary: "codex", Trailing: "4m", StateLabel: "needs you", StateTone: kit.ToneWarning, Draggable: true}},
		{Props: kit.ListRowProps{Icon: kit.IconHelp, Label: "Failed job", Secondary: "build", Trailing: "12m", StateLabel: "failed", StateTone: kit.ToneDanger}},
	}
	totalH := len(items) * rowH
	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, "interactive list (click, Up/Down, Menu / Shift+F10)", c.FgMuted)
		cd := cellDraw{
			key: cell{compListRow, 1, 0, lrLive},
			box: image.Pt(rowW, totalH),
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints = layout.Exact(image.Pt(rowW, totalH))
				p.list.live.Layout(p.env, &g, items)
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(rowW, y+totalH)
}
