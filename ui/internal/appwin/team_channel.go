package appwin

import (
	"fmt"
	"image"
	"strconv"
	"strings"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func channelButton(env *kit.Env, s *screens, state *kit.ButtonState, props kit.ButtonProps, action func()) layout.FlexChild {
	return layout.Rigid(func(g layout.Context) layout.Dimensions {
		clicked, err := state.Layout(env, &g, props)
		s.fail(err)
		if clicked {
			action()
		}
		return state.Measure(env, &g, props)
	})
}

func (s *screens) layoutTeamChannel(gtx *layout.Context, env *kit.Env, view *client.TeamView, p *teamChannelPane) layout.Dimensions {
	threads := modelChannelThreads(view)
	data := modelTeamChannel(s.teamLines[view.Summary.TeamID], view, p)
	if gtx.Constraints.Max.X < 2*gtx.Dp(theme.SidebarWidth) {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutChannelThreadMenu(g, env, p, threads, &data) }),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				return s.layoutChannelConversation(&g, env, view, p, threads, &data)
			}),
		)
	}
	return layout.Flex{}.Layout(*gtx,
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			g.Constraints.Max.X = min(g.Constraints.Max.X, g.Dp(theme.SidebarWidth))
			g.Constraints.Min.X = g.Constraints.Max.X
			return teamPanel(g, env, env.Th.Chrome().FillSoft, layout.UniformInset(theme.SpaceLG), func(g layout.Context) layout.Dimensions {
				g.Constraints.Min.Y = g.Constraints.Max.Y
				return s.layoutChannelThreads(&g, env, p, threads, &data)
			})
		}),
		spacerW(theme.SpaceXL),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return s.layoutChannelConversation(&g, env, view, p, threads, &data)
		}),
	)
}

func (s *screens) layoutChannelThreads(gtx *layout.Context, env *kit.Env, p *teamChannelPane, threads []teamChannelThread, data *teamChannelData) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidParagraph(env, theme.TypeHeading(), env.Th.Chrome().Fg, "Threads"),
		spacerH(theme.SpaceSM),
		rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutChannelFilter(g, env, p) }),
		spacerH(theme.SpaceMD),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return s.layoutChannelThreadRow(g, env, p, teamChannelThread{}, data)
		}),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return p.threadList.Layout(g, len(threads), func(g layout.Context, i int) layout.Dimensions {
				row := threads[i]
				if !filterChannelThread(p.filter, row.node.State) {
					return layout.Dimensions{}
				}
				return s.layoutChannelThreadRow(&g, env, p, row, data)
			})
		}),
	)
}

func (s *screens) layoutChannelFilter(gtx *layout.Context, env *kit.Env, p *teamChannelPane) layout.Dimensions {
	choices := []kit.MenuItem{{ID: allFilter, Label: allLabel}, {ID: teamStateAsking, Label: teamNeedsLabel}, {ID: channelFailed, Label: failedLabel}, {ID: stateRunning, Label: "Running"}}
	label := allLabel
	for _, item := range choices {
		if item.ID == p.filter {
			label = item.Label
		}
	}
	return s.layoutChannelMenu(gtx, env, p.filterButton, p.filterMenu, "Filter · "+label, choices, func(id string) { p.filter = id })
}

func (s *screens) layoutChannelThreadMenu(gtx *layout.Context, env *kit.Env, p *teamChannelPane, threads []teamChannelThread, data *teamChannelData) layout.Dimensions {
	items := []kit.MenuItem{{ID: channelGeneral, Label: channelGeneral}}
	label := channelGeneral
	for _, row := range threads {
		if !filterChannelThread(p.filter, row.node.State) {
			continue
		}
		name := channelRole(row.node)
		if unread := data.unread[row.id]; unread > 0 {
			name += fmt.Sprintf(" (%d)", unread)
		}
		items = append(items, kit.MenuItem{ID: row.id, Label: name})
		if row.id == p.thread {
			label = name
		}
	}
	return layout.Flex{}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return s.layoutChannelMenu(g, env, p.threadsButton, p.threadsMenu, label, items, func(id string) {
				if id == channelGeneral {
					id = ""
				}
				p.switchThread(id)
			})
		}),
		rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutChannelFilter(g, env, p) }),
	)
}

func (s *screens) layoutChannelMenu(gtx *layout.Context, env *kit.Env, button *kit.ButtonState, menu *kit.MenuState, text string, items []kit.MenuItem, choose func(string)) layout.Dimensions {
	props := kit.ButtonProps{Text: text, Size: kit.SizeSM}
	clicked, err := button.Layout(env, gtx, props)
	s.fail(err)
	dims := button.Measure(env, gtx, props)
	if clicked {
		menu.Open()
	}
	event, err := menu.Layout(env, gtx, kit.MenuProps{Items: items, Anchor: image.Rectangle{Max: dims.Size}})
	s.fail(err)
	if event.Chosen != "" {
		choose(event.Chosen)
	}
	return dims
}

func (s *screens) layoutChannelThreadRow(gtx *layout.Context, env *kit.Env, p *teamChannelPane, row teamChannelThread, data *teamChannelData) layout.Dimensions {
	state := p.threadRows[row.id]
	if state == nil {
		state = kit.NewListRowState()
		state.Enter()
		p.threadRows[row.id] = state
	}
	props := kit.ListRowProps{Label: channelGeneral, Icon: kit.IconMessageSquare, Selected: row.id == p.thread}
	if row.node != nil {
		props.Label = channelRole(row.node)
		props.StateLabel, props.StateTone = channelThreadStatus(row.node.State)
	}
	if unread := data.unread[row.id]; unread > 0 {
		props.Trailing = strconv.Itoa(unread)
	}
	return layout.Inset{Left: theme.SpaceSM * unit.Dp(min(row.depth, 3))}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		dims, ev := state.Layout(env, &g, &props)
		s.fail(ev.Err)
		if ev.Clicked {
			p.switchThread(row.id)
		}
		if row.node == nil {
			return dims
		}
		g.Constraints.Min.X = 0
		offset := op.Offset(image.Pt(0, dims.Size.Y)).Push(g.Ops)
		defer offset.Pop()
		caption := layout.Inset{Left: theme.ControlSM, Right: theme.SpaceSM, Bottom: theme.SpaceXS}.Layout(g, func(g layout.Context) layout.Dimensions {
			return kit.EllipsisLabel(env, &g, theme.TypeCaption(), env.Th.Chrome().FgMuted, row.node.Goal)
		})
		return layout.Dimensions{Size: image.Pt(dims.Size.X, dims.Size.Y+caption.Size.Y)}
	})
}

func (s *screens) layoutChannelConversation(gtx *layout.Context, env *kit.Env, view *client.TeamView, p *teamChannelPane, threads []teamChannelThread, data *teamChannelData) layout.Dimensions {
	ch := env.Th.Chrome()
	return teamPanel(*gtx, env, ch.SurfaceBase, layout.UniformInset(theme.SpaceXL), func(g layout.Context) layout.Dimensions {
		g.Constraints.Min.Y = g.Constraints.Max.Y
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return s.layoutChannelHeading(g, env, view.Summary.TeamID, p, data, threads)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return kit.Divider(env, g, kit.DividerProps{Orientation: kit.Horizontal})
			}),
			spacerH(theme.SpaceMD),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				return s.layoutChannelHistory(&g, env, view.Summary.TeamID, p, data)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return teamPanel(*g, env, ch.FillSoft, layout.UniformInset(theme.SpaceMD), func(g layout.Context) layout.Dimensions {
					return p.composer.layout(&g, env, s, view.Summary.TeamID, view, threads)
				})
			}),
		)
	})
}

func (s *screens) layoutChannelHeading(gtx *layout.Context, env *kit.Env, id string, p *teamChannelPane, data *teamChannelData, threads []teamChannelThread) layout.Dimensions {
	name := channelGeneral
	for _, thread := range threads {
		if thread.id == p.thread {
			name = channelRole(thread.node)
			break
		}
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamAvatar(g, env, kit.IconMessageSquare, env.Th.StatusTone(theme.StatusInfo))
		}),
		spacerW(theme.SpaceMD),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return teamFillLabel(&g, env, theme.TypeHeading(), env.Th.Chrome().Fg, name)
		}),
		channelButton(env, s, p.older, kit.ButtonProps{Text: "Older messages", Variant: kit.Bordered, Size: kit.SizeSM, Disabled: p.historyEnd || len(data.bySequence) == 0}, func() {
			s.olderTeamMessages(id, p)
		}),
	)
}

func (s *screens) layoutChannelHistory(gtx *layout.Context, env *kit.Env, id string, p *teamChannelPane, data *teamChannelData) layout.Dimensions {
	if len(data.lines) == 0 {
		return teamText(gtx, env, "No messages in this thread.")
	}
	if p.jump > 0 && data.bySequence[p.jump] == nil {
		s.olderTeamMessages(id, p)
	}
	unread := p.scrollChannel(data.lines)
	// A short stream reads from the top, while longer threads follow the latest message.
	p.list.ScrollToEnd = len(data.lines) > 8
	dims := p.list.Layout(*gtx, len(data.lines), func(g layout.Context, i int) layout.Dimensions {
		return s.layoutChannelMessage(&g, env, p, data, data.lines[i], i == unread)
	})
	if p.readChannel(data.lines) {
		s.saveTeamPosition(p)
	}
	return dims
}

func (s *screens) layoutChannelMessage(gtx *layout.Context, env *kit.Env, p *teamChannelPane, data *teamChannelData, line *client.ChannelLine, unread bool) layout.Dimensions {
	state := p.messageState(line.ChannelSeq)
	var rows []layout.FlexChild
	if unread {
		rows = append(rows, rigidWidget(func(g *layout.Context) layout.Dimensions { return channelUnreadLine(g, env) }))
	}
	rows = append(rows, rigidWidget(func(g *layout.Context) layout.Dimensions {
		return s.layoutChannelMessageBody(g, env, p, state, data, line)
	}))
	return layout.Inset{Bottom: theme.SpaceSM}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g, rows...)
	})
}

func channelUnreadLine(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidParagraph(env, theme.TypeCaption(), env.Th.StatusTone(theme.StatusInfo).Fg, "New messages"),
		layout.Rigid(layout.Spacer{Width: theme.SpaceSM}.Layout),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			size := image.Pt(g.Constraints.Max.X, 1)
			paint.FillShape(g.Ops, env.Th.StatusTone(theme.StatusInfo).Fg, clip.Rect{Max: size}.Op())
			return layout.Dimensions{Size: size}
		}),
	)
}

func (s *screens) layoutChannelMessageBody(gtx *layout.Context, env *kit.Env, p *teamChannelPane, state *teamMessageRow, data *teamChannelData, line *client.ChannelLine) layout.Dimensions {
	tag := channelKindTag(line.Kind)
	content := func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return s.layoutChannelMessageHeader(g, env, state, data, line, tag, p)
			}),
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				if state.content != line.Content || state.document == nil {
					state.content, state.document = line.Content, markdown.Parse(line.Content)
				}
				return mdview.LayoutEnv(env, g, state.document, &state.markdown)
			}),
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return s.layoutChannelMessageLinks(g, env, p, state, data, line)
			}),
		)
	}
	body := func(g layout.Context) layout.Dimensions {
		return layout.Flex{Alignment: layout.Start}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions { return channelAvatar(g, env, data, line) }),
			spacerW(theme.SpaceMD),
			layout.Flexed(1, content),
		)
	}
	if line.Kind == channelFailed || line.Kind == channelStall {
		return teamPanel(*gtx, env, env.Th.StatusTone(tag.Status).Fill, layout.UniformInset(theme.SpaceMD), body)
	}
	return layout.UniformInset(theme.SpaceMD).Layout(*gtx, body)
}

func channelAvatar(gtx *layout.Context, env *kit.Env, data *teamChannelData, line *client.ChannelLine) layout.Dimensions {
	icon := kit.IconUsers
	tone := env.Th.StatusTone(theme.StatusInfo)
	if node := data.byTask[line.SenderTaskID]; node != nil {
		icon = teamVendorIcon(node.Vendor)
		if node.Vendor == client.VendorClaude {
			tone = env.Th.StatusTone(theme.StatusSuccess)
		}
	}
	if line.SenderKind == "watchdog" {
		tone = env.Th.StatusTone(theme.StatusWarning)
		icon = kit.IconEye
	}
	if line.SenderKind == channelUser {
		tone = theme.Tone{Fill: env.Th.Chrome().FillSoft, Fg: env.Th.Chrome().Fg}
	}
	return teamAvatar(gtx, env, icon, tone)
}

func (s *screens) layoutChannelMessageHeader(gtx *layout.Context, env *kit.Env, state *teamMessageRow, data *teamChannelData, line *client.ChannelLine, tag kit.TagProps, p *teamChannelPane) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	sender := channelSender(line, data)
	if line.RecipientTaskID != "" {
		sender += " → " + data.role(line.RecipientTaskID)
	}
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return teamFillLabel(&g, env, theme.TypeLabel(), env.Th.Chrome().Fg, sender)
		}),
		layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
		rigidParagraph(env, theme.TypeCaption(), env.Th.Chrome().FgMuted, line.CreatedAt.Local().Format("15:04")),
		layout.Rigid(layout.Spacer{Width: theme.SpaceXS}.Layout),
		layout.Rigid(func(g layout.Context) layout.Dimensions {
			if tag.Text == "" {
				return layout.Dimensions{}
			}
			dims, err := state.tag.Layout(env, &g, tag)
			s.fail(err)
			return dims
		}),
		channelButton(env, s, state.number, kit.ButtonProps{Text: teamSequence(line.ChannelSeq), Variant: kit.Ghost, Size: kit.SizeSM}, func() { p.jump = line.ChannelSeq }),
	)
}

func (s *screens) layoutChannelMessageLinks(gtx *layout.Context, env *kit.Env, p *teamChannelPane, state *teamMessageRow, data *teamChannelData, line *client.ChannelLine) layout.Dimensions {
	var buttons []layout.FlexChild
	if p.thread == "" && line.ThreadRoot != "" {
		label := channelReplyLabel(data.replies[line.ThreadRoot])
		buttons = append(buttons, channelButton(env, s, state.replies, kit.ButtonProps{Text: label, Size: kit.SizeSM}, func() {
			p.switchThread(line.ThreadRoot)
		}))
	}
	if line.Kind == channelApproval {
		buttons = append(buttons, channelButton(env, s, state.inbox, kit.ButtonProps{Text: "Open in inbox", Size: kit.SizeSM}, func() {
			ref, _ := strings.CutPrefix(line.Content, "approval request ")
			request, _, _ := strings.Cut(ref, " ")
			s.openTeamInbox(request)
		}))
	}
	for _, number := range channelReferences(line.Content) {
		button := state.reference(number)
		buttons = append(buttons, channelButton(env, s, button, kit.ButtonProps{Text: teamSequence(number), Size: kit.SizeSM}, func() {
			if link := data.bySequence[number]; link != nil && p.thread != "" {
				p.switchThread(link.ThreadRoot)
			}
			p.jump = number
			if data.bySequence[number] == nil {
				s.olderTeamMessages(p.teamID, p)
			}
		}))
	}
	return layout.Inset{Top: theme.SpaceXS}.Layout(*gtx, func(g layout.Context) layout.Dimensions { return layout.Flex{}.Layout(g, buttons...) })
}

func channelReplyLabel(count int) string {
	if count == 1 {
		return "1 reply ›"
	}
	return fmt.Sprintf("%d replies ›", count)
}

func channelReferences(text string) []int64 {
	var refs []int64
	seen := make(map[int64]bool)
	for _, token := range strings.Fields(text) {
		digits, found := strings.CutPrefix(strings.Trim(token, ".,:;!?()[]`"), "#")
		if found {
			if n, err := strconv.ParseInt(digits, 10, 64); err == nil && n > 0 && !seen[n] {
				seen[n] = true
				refs = append(refs, n)
			}
		}
	}
	return refs
}

func (s *screens) openTeamInbox(request string) {
	s.grid.openCompanion(SlotRef{Kind: KindInbox})
	items := s.inbox.Items()
	for i := range items {
		item := &items[i]
		if item.RequestID == request {
			s.inboxView.active = inbox.Pin{RequestID: item.RequestID, Incarnation: item.Incarnation}
			return
		}
	}
}

func teamVendorIcon(vendor string) kit.IconName {
	if vendor == "claude" {
		return kit.IconMessageSquare
	}
	return kit.IconTerminal
}
