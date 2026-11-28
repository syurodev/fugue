package appwin

import (
	"fmt"
	"image"
	"image/color"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type teamCardPaint struct {
	ops      op.Ops
	call     op.CallOp
	key      teamPaintKey
	valid    bool
	revision uint64
	click    widget.Clickable
	size     image.Point
}

type teamPaintKey struct {
	th     *theme.Theme
	width  int
	metric unit.Metric
}

func (s *screens) layoutTeamTree(gtx *layout.Context, env *kit.Env, view *client.TeamView, board *teamBoardState) layout.Dimensions {
	tree := &board.teamTreeState
	tree.syncAgents(view)
	if board.jump != "" && tree.nodes[board.jump] != nil {
		tree.jumpToAgent(board.jump)
		board.jump = ""
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return s.layoutTeamTreeHeader(&g, env, view, tree)
		}),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			g.Constraints.Min.X = g.Constraints.Max.X
			tree.treeList.Axis = layout.Vertical
			rows := tree.treeRows()
			return tree.treeList.Layout(g, len(rows), func(g layout.Context, i int) layout.Dimensions {
				return s.layoutTeamTreeRow(&g, env, tree, view.Summary.TeamID, rows[i])
			})
		}),
	)
}

func (s *screens) layoutTeamTreeHeader(gtx *layout.Context, env *kit.Env, view *client.TeamView, tree *teamTreeState) layout.Dimensions {
	if tree.review == nil {
		tree.review = kit.NewButtonState()
		tree.needs = &teamCountChip{menu: kit.NewMenuState()}
		tree.failed = &teamCountChip{menu: kit.NewMenuState()}
	}
	return layout.Inset{Bottom: theme.SpaceXL}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return teamPanel(g, env, env.Th.Chrome().FillSoft, layout.UniformInset(theme.SpaceLG), func(g layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(g,
				layout.Flexed(1, func(g layout.Context) layout.Dimensions {
					return s.layoutTeamTreeCounts(&g, env, &view.Summary, tree)
				}),
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return s.layoutTeamTreeReview(g, env, &view.Summary, tree)
				}),
			)
		})
	})
}

func (s *screens) layoutTeamTreeCounts(gtx *layout.Context, env *kit.Env, summary *client.TeamSummary, tree *teamTreeState) layout.Dimensions {
	label := fmt.Sprintf("%d agents · %d running", summary.TaskCount, summary.RunningCount)
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return teamFillLabel(&g, env, theme.TypeLabel(), env.Th.Chrome().Fg, label)
		}),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return tree.needs.layout(&g, env, s, tree, summary.AskingCount, theme.StatusWarning)
		}),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return tree.failed.layout(&g, env, s, tree, summary.FailedCount, theme.StatusDanger)
		}),
	)
}

func (s *screens) layoutTeamTreeReview(gtx *layout.Context, env *kit.Env, summary *client.TeamSummary, tree *teamTreeState) layout.Dimensions {
	if summary.State != teamFinalReview {
		return layout.Dimensions{}
	}
	return layout.Inset{Left: theme.SpaceLG}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		props := kit.ButtonProps{Text: "Review & Integrate", Icon: kit.IconGitPullRequest, Variant: kit.Primary, Size: kit.SizeSM}
		clicked, err := tree.review.Layout(env, &g, props)
		s.fail(err)
		if clicked {
			s.openTeamReview(summary.TeamID)
		}
		return tree.review.Measure(env, &g, props)
	})
}

type teamCountChip struct {
	click widget.Clickable
	menu  *kit.MenuState
}

func (c *teamCountChip) layout(gtx *layout.Context, env *kit.Env, sc *screens, tree *teamTreeState, count uint32, status theme.StatusKind) layout.Dimensions {
	if count == 0 {
		return layout.Dimensions{}
	}
	label := "needs you"
	if status == theme.StatusDanger {
		label = channelFailed
	}
	text := fmt.Sprintf("%d %s", count, label)
	return layout.Inset{Left: theme.SpaceXS}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return c.layoutChip(&g, env, sc, tree, text, status)
	})
}

func (c *teamCountChip) layoutChip(gtx *layout.Context, env *kit.Env, sc *screens, tree *teamTreeState, text string, status theme.StatusKind) layout.Dimensions {
	pressed := c.click.Clicked(*gtx)
	tone := env.Th.StatusTone(status)
	dims := c.click.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layoutTeamChip(&g, env, text, tone)
	})
	if pressed {
		c.menu.Open()
	}
	props := kit.MenuProps{Items: teamChipAgents(tree, status), Anchor: image.Rectangle{Max: dims.Size}, AlignEnd: true}
	result, err := c.menu.Layout(env, gtx, props)
	sc.fail(err)
	if result.Chosen != "" {
		tree.jumpToAgent(result.Chosen)
	}
	return dims
}

func teamChipAgents(tree *teamTreeState, status theme.StatusKind) []kit.MenuItem {
	var items []kit.MenuItem
	for _, c := range tree.ordered {
		if c.tag.Variant == kit.TagStatus && c.tag.Status == status {
			items = append(items, kit.MenuItem{ID: c.node.TaskID, Label: c.title + " · " + c.node.Goal})
		}
	}
	return items
}

func layoutTeamChip(gtx *layout.Context, env *kit.Env, text string, tone theme.Tone) layout.Dimensions {
	semantic.Button.Add(gtx.Ops)
	rec := op.Record(gtx.Ops)
	dims := layout.Inset{Left: theme.SpaceSM, Right: theme.SpaceSM, Top: theme.SpaceXXS, Bottom: theme.SpaceXXS}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return kit.EllipsisLabel(env, &g, theme.TypeSmall(), tone.Fg, text)
	})
	call := rec.Stop()
	paint.FillShape(gtx.Ops, tone.Fill, clip.UniformRRect(image.Rectangle{Max: dims.Size}, dims.Size.Y/2).Op(gtx.Ops))
	call.Add(gtx.Ops)
	return dims
}

func (s *screens) layoutTeamTreeRow(gtx *layout.Context, env *kit.Env, tree *teamTreeState, teamID string, row teamTreeRow) layout.Dimensions {
	indent := gtx.Dp(theme.SpaceXL)
	card := row.card
	dims := layout.Inset{Left: theme.SpaceXL * unit.Dp(row.depth), Bottom: theme.SpaceMD}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return s.layoutTreeAgent(&g, env, tree, teamID, card)
	})
	paintTeamBranches(gtx, env.Th.Chrome().Border, row, indent, dims.Size.Y)
	return dims
}

func (s *screens) layoutTreeAgent(gtx *layout.Context, env *kit.Env, tree *teamTreeState, teamID string, c *teamAgentCard) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			if c.tree.click.Clicked(g) {
				s.openTeamAgent(teamID, c.node.TaskID)
			}
			return c.tree.layout(&g, env, c, teamTreeLabels(c), tree.treeFocus == c.node.TaskID)
		}),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return s.layoutTeamCollapse(&g, env, tree, c)
		}),
	)
}

func (s *screens) layoutTeamCollapse(gtx *layout.Context, env *kit.Env, tree *teamTreeState, c *teamAgentCard) layout.Dimensions {
	if len(c.children) == 0 {
		return layout.Dimensions{}
	}
	if c.collapse == nil {
		c.collapse = kit.NewIconButtonState()
	}
	props := kit.IconButtonProps{Icon: kit.IconChevronDown, Label: "Collapse agents", Size: kit.SizeXS}
	if c.collapsed {
		props.Icon, props.Label = kit.IconChevronRight, "Expand agents"
	}
	clicked, err := c.collapse.Layout(env, gtx, &props)
	s.fail(err)
	if clicked {
		c.collapsed = !c.collapsed
		tree.dirty = true
		env.Keep()
	}
	return c.collapse.Measure(env, gtx, &props)
}

func paintTeamBranches(gtx *layout.Context, ink color.NRGBA, row teamTreeRow, indent, height int) {
	hairline := max(1, gtx.Dp(theme.Hairline))
	for level := range row.depth {
		x := indent*level + indent/2
		if row.continued&(1<<level) != 0 {
			paint.FillShape(gtx.Ops, ink, clip.Rect(image.Rect(x, 0, x+hairline, height)).Op())
		}
	}
	if row.depth == 0 {
		return
	}
	right := indent * row.depth
	x := right - indent/2
	mid := (height - gtx.Dp(theme.SpaceMD)) / 2
	if row.sibling {
		paint.FillShape(gtx.Ops, ink, clip.Rect(image.Rect(x, 0, x+hairline, height)).Op())
	}
	paintTeamElbow(gtx, ink, x, mid, right)
}
