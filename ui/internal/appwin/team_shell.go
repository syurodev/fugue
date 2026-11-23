package appwin

import (
	"fmt"
	"image"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/session"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const teamLabel = "Team"

type teamShell struct {
	mode                        *kit.SegmentedState
	open, create, needs, failed *kit.ButtonState
	showTeam                    bool
}

func (s *screens) teamShell(id string) *teamShell {
	state := s.teamShells[id]
	if state == nil {
		state = &teamShell{
			mode: kit.NewSegmentedState(), open: kit.NewButtonState(), create: kit.NewButtonState(),
			needs: kit.NewButtonState(), failed: kit.NewButtonState(),
		}
		s.teamShells[id] = state
	}
	return state
}

func (s *screens) layoutTeamSessionContent(gtx *layout.Context, env *kit.Env, id string) {
	if id == "" {
		s.layoutChatContent(gtx, env, id)
		return
	}
	summary, active := s.leadTeam(id)
	sh := s.teamShell(id)
	layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return s.layoutTeamSessionHeader(g, env, sh, id, &summary, active)
		}),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			if active && sh.showTeam {
				return s.layoutTeamBoard(&g, env, teamBoardKey{team: summary.TeamID, main: true})
			}
			s.layoutChatContent(&g, env, id)
			return layout.Dimensions{Size: g.Constraints.Max}
		}),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			if !active || sh.showTeam {
				return layout.Dimensions{}
			}
			return s.layoutTeamStrip(g, env, sh, &summary)
		}),
	)
}

func (s *screens) layoutTeamSessionHeader(gtx *layout.Context, env *kit.Env, sh *teamShell, id string, summary *client.TeamSummary, active bool) layout.Dimensions {
	if !active {
		return s.layoutCreateTeam(gtx, env, sh, id)
	}
	return layout.Inset{Top: theme.SpaceLG, Bottom: theme.SpaceLG, Left: theme.SpaceXL, Right: theme.SpaceXL}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutTeamMode(g, env, sh, summary.TeamID) }),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions { return layout.Dimensions{Size: g.Constraints.Min} }),
			channelButton(env, s, sh.open, kit.ButtonProps{Text: "Open in slot", Icon: kit.IconUsers, Size: kit.SizeSM}, func() {
				s.boardFor(teamBoardKey{team: summary.TeamID}).channel.reopen()
				s.grid.openCompanion(SlotRef{Kind: KindTeam, ID: summary.TeamID})
			}),
		)
	})
}

func (s *screens) layoutTeamMode(gtx *layout.Context, env *kit.Env, sh *teamShell, id string) layout.Dimensions {
	props := kit.SegmentedProps{Options: []kit.SegmentedOption{{ID: "chat", Label: "Chat"}, {ID: "team", Label: teamLabel}}}
	if sh.showTeam {
		props.Selected = 1
	}
	changed, next, err := sh.mode.Layout(env, gtx, props)
	s.fail(err)
	if changed {
		sh.showTeam = next == 1
		if sh.showTeam {
			s.boardFor(teamBoardKey{team: id, main: true}).channel.reopen()
		}
	}
	return sh.mode.Measure(env, gtx, props)
}

func (s *screens) layoutCreateTeam(gtx *layout.Context, env *kit.Env, sh *teamShell, id string) layout.Dimensions {
	sess, exists := s.sidebar.Lookup(id)
	if !exists || s.poster == nil {
		return layout.Dimensions{}
	}
	kind, allowed := s.composer.action(sess)
	connected := s.connected()
	return layout.Inset{Top: theme.SpaceXS, Left: theme.SpaceMD}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		props := kit.ButtonProps{Text: "Create team", Icon: kit.IconUsers, Size: kit.SizeSM, Disabled: !connected || !allowed || s.vault.Locked()}
		clicked, err := sh.create.Layout(env, &g, props)
		s.fail(err)
		if clicked {
			if kind == session.PostSteer {
				s.poster.Steer(id, "/fugue:team")
			} else {
				s.poster.Send(id, "/fugue:team")
			}
			s.armTurnAnchor(id)
		}
		return sh.create.Measure(env, &g, props)
	})
}

func (s *screens) layoutTeamStrip(gtx *layout.Context, env *kit.Env, sh *teamShell, summary *client.TeamSummary) layout.Dimensions {
	s.requestTeam(summary.TeamID)
	ch := env.Th.Chrome()
	title := s.teamViews[summary.TeamID].Title
	if title == "" {
		title = teamLabel
	}
	return layoutTeamChatWidth(gtx, env, theme.SpaceSM, func(g layout.Context) layout.Dimensions {
		return teamPanel(g, env, ch.FillSoft, layout.UniformInset(theme.SpaceMD), func(g layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(g,
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return teamAvatar(g, env, kit.IconUsers, env.Th.StatusTone(theme.StatusInfo))
				}),
				spacerW(theme.SpaceMD),
				layout.Flexed(1, func(g layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical}.Layout(g,
						rigidWidget(func(g *layout.Context) layout.Dimensions {
							return teamFillLabel(g, env, theme.TypeLabel(), ch.Fg, title)
						}),
						rigidWidget(func(g *layout.Context) layout.Dimensions {
							return teamFillLabel(g, env, theme.TypeCaption(), ch.FgMuted, fmt.Sprintf("%d running", summary.RunningCount))
						}),
					)
				}),
				s.teamStripChip(env, sh, summary, sh.needs, summary.AskingCount, "needs you", theme.StatusWarning),
				s.teamStripChip(env, sh, summary, sh.failed, summary.FailedCount, channelFailed, theme.StatusDanger),
			)
		})
	})
}

func (s *screens) teamStripChip(env *kit.Env, sh *teamShell, summary *client.TeamSummary, button *kit.ButtonState, count uint32, label string, status theme.StatusKind) layout.FlexChild {
	return layout.Rigid(func(g layout.Context) layout.Dimensions {
		if count == 0 {
			return layout.Dimensions{}
		}
		return layout.Inset{Left: theme.SpaceSM}.Layout(g, func(g layout.Context) layout.Dimensions {
			props := kit.ButtonProps{Text: fmt.Sprintf("%d %s", count, label), Size: kit.SizeSM, Variant: kit.Ghost}
			tone := env.Th.StatusTone(status)
			return layout.Background{}.Layout(g, func(g layout.Context) layout.Dimensions {
				size := g.Constraints.Min
				paint.FillShape(g.Ops, tone.Fill, clip.UniformRRect(image.Rectangle{Max: size}, size.Y/2).Op(g.Ops))
				return layout.Dimensions{Size: size}
			}, func(g layout.Context) layout.Dimensions {
				clicked, err := button.Layout(env, &g, props)
				s.fail(err)
				if clicked {
					s.jumpFromTeamStrip(sh, summary.TeamID, status)
				}
				return button.Measure(env, &g, props)
			})
		})
	})
}

func (s *screens) layoutTeamLeadComposer(gtx *layout.Context, env *kit.Env, id string, connected bool) {
	layoutTeamChatWidth(gtx, env, theme.SpaceLG, func(g layout.Context) layout.Dimensions {
		return layoutLeadComposerSurface(g, env, s.composer.stateFor(id).area, func(g layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(g,
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return layout.Inset{Left: theme.SpaceLG}.Layout(*g, func(g layout.Context) layout.Dimensions {
						return kit.Label(env, &g, theme.TypeCaption(), env.Th.Chrome().FgMuted, "To Lead")
					})
				}),
				spacerH(theme.SpaceXS),
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					return s.composer.layoutSessionComposer(env, g, s, id, connected, true)
				}),
			)
		})
	})
}

func layoutLeadComposerSurface(gtx layout.Context, env *kit.Env, area *kit.TextAreaState, body layout.Widget) layout.Dimensions {
	return layout.Background{}.Layout(gtx, func(g layout.Context) layout.Dimensions {
		size := g.Constraints.Min
		chrome := env.Th.Chrome()
		paintTeamSurface(&g, env, size, chrome.SurfaceBase)
		if env.FocusVisible(&g, area.Tag()) {
			stroke := max(1, g.Dp(theme.Hairline))
			rings := clip.UniformRRect(image.Rectangle{Max: size}.Inset(stroke), g.Dp(theme.RadiusXL)-stroke)
			paint.FillShape(g.Ops, chrome.FocusBorder, clip.Stroke{Path: rings.Path(g.Ops), Width: float32(stroke)}.Op())
		}
		return layout.Dimensions{Size: size}
	}, func(g layout.Context) layout.Dimensions {
		return layout.UniformInset(theme.SpaceLG).Layout(g, body)
	})
}

func teamChatWidth(gtx *layout.Context) int {
	return min(gtx.Dp(theme.TranscriptMeasure), max(gtx.Constraints.Max.X-2*gtx.Dp(theme.SpaceXL), 0))
}

func layoutTeamChatWidth(gtx *layout.Context, env *kit.Env, bottom unit.Dp, body layout.Widget) layout.Dimensions {
	width := teamChatWidth(gtx)
	x := (gtx.Constraints.Max.X - width) / 2
	var dims layout.Dimensions
	env.Offset(gtx, image.Pt(x, 0), func(g *layout.Context) {
		local := *g
		local.Constraints.Min.X, local.Constraints.Max.X = width, width
		dims = layout.Inset{Bottom: bottom}.Layout(local, body)
	})
	dims.Size.X = gtx.Constraints.Max.X
	return dims
}

func (s *screens) jumpFromTeamStrip(sh *teamShell, id string, status theme.StatusKind) {
	sh.showTeam = true
	board := s.boardFor(teamBoardKey{team: id, main: true})
	view := s.teamViews[id]
	board.syncAgents(&view)
	board.tab = tabTree
	if items := teamChipAgents(&board.teamTreeState, status); len(items) > 0 {
		board.jump = items[0].ID
	}
}
