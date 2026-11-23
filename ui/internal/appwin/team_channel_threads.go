package appwin

import (
	"image"
	"slices"
	"strconv"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type channelThreadRowState struct {
	click  widget.Clickable
	unread *kit.TagState
}

func (s *screens) layoutChannelThreads(gtx *layout.Context, env *kit.Env, p *teamChannelPane, threads []teamChannelThread, data *teamChannelData) layout.Dimensions {
	visible := slices.DeleteFunc(slices.Clone(threads), func(row teamChannelThread) bool {
		return !filterChannelThread(p.filter, row.node.State)
	})
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutChannelThreadHeader(g, env, p) }),
		spacerH(theme.SpaceXL),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return s.layoutChannelThreadRow(g, env, p, teamChannelThread{}, data.unread[""])
		}),
		spacerH(theme.SpaceLG),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return kit.Divider(env, g, kit.DividerProps{Orientation: kit.Horizontal})
		}),
		spacerH(theme.SpaceLG),
		rigidParagraph(env, theme.TypeCaption(), env.Th.Chrome().FgMuted, "Task threads"),
		spacerH(theme.SpaceXS),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			if len(visible) == 0 {
				return teamText(&g, env, "No matching threads")
			}
			return p.threadList.Layout(g, len(visible), func(g layout.Context, i int) layout.Dimensions {
				row := visible[i]
				return s.layoutChannelThreadRow(&g, env, p, row, data.unread[row.id])
			})
		}),
	)
}

func (s *screens) layoutChannelThreadHeader(gtx *layout.Context, env *kit.Env, p *teamChannelPane) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return teamFillLabel(&g, env, theme.TypeHeading(), env.Th.Chrome().Fg, "Threads")
		}),
		rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutChannelFilter(g, env, p) }),
	)
}

func (s *screens) layoutChannelThreadRow(gtx *layout.Context, env *kit.Env, p *teamChannelPane, row teamChannelThread, unread int) layout.Dimensions {
	state := p.threadRows[row.id]
	if state == nil {
		state = &channelThreadRowState{unread: kit.NewTagState()}
		p.threadRows[row.id] = state
	}
	indent := theme.SpaceXL * unit.Dp(min(row.depth, 3))
	dims := layout.Inset{Left: indent, Bottom: theme.SpaceXS}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		g.Constraints.Min.X = g.Constraints.Max.X
		if state.click.Clicked(g) {
			p.switchThread(row.id)
		}
		selected := row.id == p.thread
		return state.click.Layout(g, func(g layout.Context) layout.Dimensions {
			semantic.Button.Add(g.Ops)
			semantic.SelectedOp(selected).Add(g.Ops)
			return s.layoutChannelThreadCard(&g, env, state, row, selected, unread)
		})
	})
	if indent > 0 {
		edge := gtx.Dp(indent)
		paintTeamElbow(gtx, env.Th.Chrome().Border, edge-gtx.Dp(theme.SpaceMD), (dims.Size.Y-gtx.Dp(theme.SpaceXS))/2, edge)
	}
	return dims
}

func (s *screens) layoutChannelThreadCard(gtx *layout.Context, env *kit.Env, state *channelThreadRowState, row teamChannelThread, selected bool, unread int) layout.Dimensions {
	bg := func(g layout.Context) layout.Dimensions {
		ch := env.Th.Chrome()
		bounds := image.Rectangle{Max: g.Constraints.Min}
		shape := clip.UniformRRect(bounds, g.Dp(theme.RadiusMD))
		switch {
		case selected:
			paint.FillShape(g.Ops, env.Th.StatusTone(theme.StatusInfo).Fill, shape.Op(g.Ops))
		case state.click.Hovered():
			paint.FillShape(g.Ops, ch.HoverWash, shape.Op(g.Ops))
		}
		if env.FocusVisible(&g, &state.click) {
			paint.FillShape(g.Ops, ch.FocusBorder, clip.Stroke{Path: shape.Path(g.Ops), Width: float32(max(1, g.Dp(theme.Hairline)))}.Op())
		}
		return layout.Dimensions{Size: g.Constraints.Min}
	}
	return layout.Background{}.Layout(*gtx, bg, func(g layout.Context) layout.Dimensions {
		return layout.Inset{Top: theme.SpaceSM, Bottom: theme.SpaceSM, Left: theme.SpaceSM, Right: theme.SpaceSM}.Layout(g, func(g layout.Context) layout.Dimensions {
			return s.layoutChannelThreadContent(&g, env, state, row, unread)
		})
	})
}

func (s *screens) layoutChannelThreadContent(gtx *layout.Context, env *kit.Env, state *channelThreadRowState, row teamChannelThread, unread int) layout.Dimensions {
	ch := env.Th.Chrome()
	name, icon := channelGeneral, kit.IconMessageSquare
	if row.node != nil {
		name, icon = channelRole(row.node), teamVendorIcon(row.node.Vendor)
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamAvatarAtSize(g, env, icon, env.Th.NeutralTone(), theme.ControlSM)
		}),
		spacerW(theme.SpaceMD),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			if row.node == nil {
				return layout.Flex{Alignment: layout.Middle}.Layout(g,
					layout.Flexed(1, func(g layout.Context) layout.Dimensions {
						return teamFillLabel(&g, env, theme.TypeLabel(), ch.Fg, name)
					}),
					s.threadUnreadBadge(env, state, unread),
				)
			}
			return s.layoutChannelTaskText(&g, env, state, row, unread)
		}),
	)
}

func (s *screens) layoutChannelTaskText(gtx *layout.Context, env *kit.Env, state *channelThreadRowState, row teamChannelThread, unread int) layout.Dimensions {
	ch := env.Th.Chrome()
	label, tone := channelThreadStatus(row.node.State)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(*g,
				layout.Flexed(1, func(g layout.Context) layout.Dimensions {
					return teamFillLabel(&g, env, theme.TypeLabel(), ch.Fg, channelRole(row.node))
				}),
				spacerW(theme.SpaceSM),
				rigidWidget(func(g *layout.Context) layout.Dimensions { return layoutThreadStatusLabel(g, env, label, tone) }),
			)
		}),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return layout.Flex{Alignment: layout.Middle}.Layout(*g,
				layout.Flexed(1, func(g layout.Context) layout.Dimensions {
					return teamFillLabel(&g, env, theme.TypeCaption(), ch.FgMuted, row.node.Goal)
				}),
				s.threadUnreadBadge(env, state, unread),
			)
		}),
	)
}

func (s *screens) threadUnreadBadge(env *kit.Env, state *channelThreadRowState, unread int) layout.FlexChild {
	return rigidWidget(func(g *layout.Context) layout.Dimensions {
		if unread == 0 {
			return layout.Dimensions{}
		}
		return layout.Inset{Left: theme.SpaceSM}.Layout(*g, func(g layout.Context) layout.Dimensions {
			dims, err := state.unread.Layout(env, &g, kit.TagProps{Text: strconv.Itoa(unread), Variant: kit.TagStatus, Status: theme.StatusInfo})
			s.fail(err)
			return dims
		})
	})
}

func layoutThreadStatusLabel(gtx *layout.Context, env *kit.Env, label string, tone kit.ListRowTone) layout.Dimensions {
	ink := env.Th.Chrome().IconMuted
	switch tone {
	case kit.ToneInfo:
		ink = env.Th.StatusTone(theme.StatusInfo).Fg
	case kit.ToneSuccess:
		ink = env.Th.StatusTone(theme.StatusSuccess).Fg
	case kit.ToneWarning:
		ink = env.Th.StatusTone(theme.StatusWarning).Fg
	case kit.ToneDanger:
		ink = env.Th.StatusTone(theme.StatusDanger).Fg
	case kit.ToneNeutral:
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			r := max(1, g.Dp(theme.SpaceXXS))
			size := image.Pt(2*r, 2*r)
			paint.FillShape(g.Ops, ink, clip.UniformRRect(image.Rectangle{Max: size}, r).Op(g.Ops))
			return layout.Dimensions{Size: size}
		}),
		spacerW(theme.SpaceXS),
		rigidParagraph(env, theme.TypeCaption(), ink, label),
	)
}
