package appwin

import (
	"image"
	"strconv"
	"strings"

	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func (s *screens) layoutTeamMembers(gtx *layout.Context, env *kit.Env, view *client.TeamView, board *teamBoardState) layout.Dimensions {
	tree := &board.teamTreeState
	tree.syncAgents(view)
	if !view.VaultLocked {
		tree.syncMemberMessages(s.teamLines[view.Summary.TeamID])
	}
	tree.membersList.Axis = layout.Vertical
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return tree.membersList.Layout(*gtx, len(tree.ordered), func(g layout.Context, i int) layout.Dimensions {
		c := tree.ordered[i]
		return layout.Inset{Bottom: theme.SpaceMD}.Layout(g, func(g layout.Context) layout.Dimensions {
			if c.member.click.Clicked(g) {
				s.openTeamAgent(view.Summary.TeamID, c.node.TaskID)
			}
			return c.member.layout(&g, env, c, teamMemberLabels(c, view.VaultLocked), false)
		})
	})
}

func (s *teamTreeState) syncMemberMessages(lines []client.ChannelLine) {
	for i := range lines {
		msg := &lines[i]
		if c := s.nodes[msg.SenderTaskID]; c != nil && msg.ChannelSeq >= c.latest.ChannelSeq {
			if msg.ChannelSeq != c.latest.ChannelSeq || msg.Content != c.latest.Content {
				c.latest = *msg
				c.member.valid = false
			}
		}
		if c := s.nodes[msg.RecipientTaskID]; c != nil && msg.ChannelSeq > c.receivedSeq {
			c.receivedSeq = msg.ChannelSeq
			c.member.valid = false
		}
	}
}

type teamCardLabels struct {
	subtitle string
	metadata string
}

func teamTreeLabels(c *teamAgentCard) teamCardLabels {
	meta := c.elapsed
	if c.node.Attempt > 1 {
		meta += " · Attempt " + strconv.FormatUint(uint64(c.node.Attempt), 10)
	}
	return teamCardLabels{subtitle: teamGoal(c), metadata: meta}
}

func teamMemberLabels(c *teamAgentCard, locked bool) teamCardLabels {
	meta := "No channel messages"
	if c.latest.ChannelSeq > 0 && !locked {
		meta = teamSequence(c.latest.ChannelSeq) + " " + strings.ReplaceAll(c.latest.Content, "\n", " ")
	}
	if c.receivedSeq > 0 && !locked {
		meta += " · Last addressed " + teamSequence(c.receivedSeq)
	}
	return teamCardLabels{subtitle: teamGoal(c), metadata: meta}
}

func teamGoal(c *teamAgentCard) string {
	if c.node.Goal != "" {
		return c.node.Goal
	}
	return "No task description"
}

// layout replays the card's static text and badge ops until its snapshot
// revision or layout constraints change. Input and selection stay live.
func (p *teamCardPaint) layout(gtx *layout.Context, env *kit.Env, c *teamAgentCard, labels teamCardLabels, selected bool) layout.Dimensions {
	return p.click.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		key := teamPaintKey{th: env.Th, width: g.Constraints.Max.X, metric: g.Metric}
		if !p.valid || key != p.key {
			p.record(&g, env, c, labels)
			p.key, p.valid = key, true
			p.revision++
		}
		semantic.Button.Add(g.Ops)
		paintTeamCard(&g, env, p.size, selected || p.click.Hovered())
		p.call.Add(g.Ops)
		return layout.Dimensions{Size: p.size}
	})
}

func (p *teamCardPaint) record(gtx *layout.Context, env *kit.Env, c *teamAgentCard, labels teamCardLabels) {
	p.ops.Reset()
	g := *gtx
	g.Ops = &p.ops
	g.Constraints.Min.Y = 0
	g.Constraints.Min.X = g.Constraints.Max.X
	rec := op.Record(g.Ops)
	dims := layout.Inset{Top: theme.SpaceLG, Bottom: theme.SpaceLG, Left: theme.SpaceLG, Right: theme.SpaceLG}.Layout(g, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Middle}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return teamAvatar(g, env, teamVendorIcon(c.node.Vendor), env.Th.StatusTone(theme.StatusInfo))
			}),
			spacerW(theme.SpaceLG),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions { return layoutTeamCardLabels(&g, env, c, labels) }),
		)
	})
	p.call, p.size = rec.Stop(), dims.Size
}

func layoutTeamCardLabels(gtx *layout.Context, env *kit.Env, c *teamAgentCard, labels teamCardLabels) layout.Dimensions {
	chrome := env.Th.Chrome()
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return layoutTeamCardTitle(&g, env, c)
		}),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return kit.EllipsisLabel(env, &g, theme.TypeBody(), chrome.Fg, labels.subtitle)
		}),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			return kit.EllipsisLabel(env, &g, theme.TypeSmall(), chrome.FgMuted, labels.metadata)
		}),
	)
}

func layoutTeamCardTitle(gtx *layout.Context, env *kit.Env, c *teamAgentCard) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return teamFillLabel(&g, env, theme.TypeTitle(), env.Th.Chrome().Fg, c.title)
		}),
		rigidParagraph(env, theme.TypeCaption(), env.Th.Chrome().FgMuted, c.model),
		spacerW(theme.SpaceMD),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			// A fresh Tag starts at rest so the recorded ops carry its final tone.
			state := kit.NewTagState()
			dims, err := state.Layout(env, &g, c.tag)
			if err != nil {
				return layout.Dimensions{}
			}
			return dims
		}),
	)
}

func paintTeamCard(gtx *layout.Context, env *kit.Env, size image.Point, active bool) {
	chrome := env.Th.Chrome()
	fill := chrome.SurfaceBase
	if active {
		fill = chrome.SurfaceTinted
	}
	paintTeamSurface(gtx, env, size, fill)
}
