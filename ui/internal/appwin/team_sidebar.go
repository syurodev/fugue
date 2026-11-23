package appwin

import (
	"fmt"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/widget"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type teamSidebarRow struct {
	click widget.Clickable
	badge *kit.TagState
}

func (s *screens) layoutTeamSidebarRow(gtx *layout.Context, env *kit.Env, summary *client.TeamSummary) layout.Dimensions {
	id := summary.TeamID
	row := s.teamSidebarRows[id]
	if row == nil {
		row = &teamSidebarRow{badge: kit.NewTagState()}
		s.teamSidebarRows[id] = row
	}
	title := s.teamViews[id].Title
	if title == "" {
		title = id
	}
	ref := SlotRef{Kind: KindTeam, ID: id}
	if row.click.Clicked(*gtx) {
		s.boardFor(teamBoardKey{team: id, main: true}).channel.reopen()
		s.grid.openMain(ref)
	}
	selected := ref == refAt(s.grid.doc, Pos{})
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Bottom: theme.SpaceSM}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return row.click.Layout(g, func(g layout.Context) layout.Dimensions {
			semantic.Button.Add(g.Ops)
			semantic.SelectedOp(selected).Add(g.Ops)
			return layoutTeamNavHighlight(g, env, &row.click, selected, func(g layout.Context) layout.Dimensions {
				return layout.UniformInset(theme.SpaceMD).Layout(g, func(g layout.Context) layout.Dimensions {
					return s.layoutTeamSidebarEntry(&g, env, row, title, summary)
				})
			})
		})
	})
}

func (s *screens) layoutTeamSidebarEntry(gtx *layout.Context, env *kit.Env, row *teamSidebarRow, title string, summary *client.TeamSummary) layout.Dimensions {
	ch := env.Th.Chrome()
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamAvatar(g, env, kit.IconUsers, env.Th.NeutralTone())
		}),
		spacerW(theme.SpaceMD),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(g,
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return teamFillLabel(g, env, theme.TypeLabel(), ch.Fg, title)
				}),
				spacerH(theme.SpaceXXS),
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return layout.Flex{Alignment: layout.Middle}.Layout(*g,
						layout.Flexed(1, func(g layout.Context) layout.Dimensions {
							return teamFillLabel(&g, env, theme.TypeCaption(), ch.FgMuted, fmt.Sprintf("%d agents", summary.TaskCount))
						}),
						spacerW(theme.SpaceXS),
						rigidTag(env, s.fail, row.badge, teamBoardStatus(summary.State)),
					)
				}),
			)
		}),
	)
}
