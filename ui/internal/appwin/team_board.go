package appwin

import (
	"maps"
	"slices"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/team"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	tabChannel = "channel"
	tabTree    = "tree"
	tabMembers = "members"
)

type teamBoardKey struct {
	team string
	main bool
}

type teamBoardState struct {
	teamTreeState
	channel   *teamChannelPane
	tabs      *kit.TabsState
	status    *kit.TagState
	tab, jump string
	retry     *kit.ButtonState
}

func (s *screens) boardFor(key teamBoardKey) *teamBoardState {
	board := s.teamBoards[key]
	if board == nil {
		read := s.teamRead[key.team]
		if stored := s.grid.doc.TeamRead[key.team]; read == nil && stored != nil {
			read = maps.Clone(stored)
		}
		if read == nil {
			read = make(map[string]int64)
		}
		s.teamRead[key.team] = read
		board = &teamBoardState{channel: newTeamChannelPane(read), tabs: kit.NewTabsState(), status: kit.NewTagState(), tab: tabChannel, retry: kit.NewButtonState()}
		board.channel.teamID = key.team
		s.teamBoards[key] = board
	}
	return board
}

func (s *screens) layoutTeamBoard(gtx *layout.Context, env *kit.Env, key teamBoardKey) layout.Dimensions {
	if key.team == "" {
		return teamText(gtx, env, "Select a team in the sidebar.")
	}
	s.requestTeam(key.team)
	board := s.boardFor(key)
	view, ok := s.teamViews[key.team]
	if !ok {
		if s.failedTeamRead(team.Read, key.team, "") {
			return s.layoutTeamRetry(gtx, env, board.retry, "Team unavailable.", team.Read, key.team, "")
		}
		return teamText(gtx, env, "Loading team…")
	}
	return layout.Inset{Top: theme.SpaceXL, Bottom: theme.SpaceLG, Left: theme.SpaceXL, Right: theme.SpaceXL}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutTeamBoardHeader(g, env, &view, board) }),
			spacerH(theme.SpaceLG),
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutTeamTabs(g, env, board) }),
			spacerH(theme.SpaceLG),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				switch board.tab {
				case tabTree:
					return s.layoutTeamTree(&g, env, &view, board)
				case tabMembers:
					return s.layoutTeamMembers(&g, env, &view, board)
				default:
					if view.VaultLocked {
						return teamText(&g, env, "Unlock the vault to view the team channel.")
					}
					return s.layoutTeamChannel(&g, env, &view, board.channel)
				}
			}),
		)
	})
}

func (s *screens) layoutTeamTabs(gtx *layout.Context, env *kit.Env, board *teamBoardState) layout.Dimensions {
	dims, ev := board.tabs.Layout(env, gtx, kit.TabsProps{
		Items:    []kit.TabItem{{ID: tabChannel, Label: "Channel"}, {ID: tabTree, Label: "Tree"}, {ID: tabMembers, Label: "Members"}},
		ActiveID: board.tab,
	})
	s.fail(ev.Err)
	if ev.Selected != "" && ev.Selected != board.tab {
		board.tab = ev.Selected
		if board.tab == tabChannel {
			board.channel.reopen()
		}
	}
	return dims
}

func (s *screens) layoutTeamBoardHeader(gtx *layout.Context, env *kit.Env, view *client.TeamView, board *teamBoardState) layout.Dimensions {
	ch := env.Th.Chrome()
	title := view.Title
	if title == "" {
		title = "Team board"
	}
	status := teamBoardStatus(view.Summary.State)
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamAvatar(g, env, kit.IconUsers, env.Th.StatusTone(theme.StatusInfo))
		}),
		spacerW(theme.SpaceLG),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			rows := []layout.FlexChild{rigidWidget(func(g *layout.Context) layout.Dimensions {
				return teamFillLabel(g, env, theme.TypeDisplay(), ch.Fg, title)
			})}
			if view.Brief != "" {
				rows = append(rows, rigidWidget(func(g *layout.Context) layout.Dimensions {
					return teamFillLabel(g, env, theme.TypeBody(), ch.FgMuted, view.Brief)
				}))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(g, rows...)
		}),
		spacerW(theme.SpaceMD),
		rigidTag(env, s.fail, board.status, status),
	)
}

func (s *screens) layoutTeamsSidebar(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Inset{Top: theme.SpaceXL, Left: theme.SpaceMD, Right: theme.SpaceSM}.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidParagraph(env, theme.TypeHeading(), env.Th.Chrome().Fg, "Teams"),
			spacerH(theme.SpaceLG),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				if len(s.teamSummaries) == 0 {
					return teamText(&g, env, "No teams yet.")
				}
				return s.teamSidebarList.Layout(g, len(s.teamSummaries), func(g layout.Context, i int) layout.Dimensions {
					return s.layoutTeamSidebarRow(&g, env, &s.teamSummaries[i])
				})
			}),
		)
	})
}

// layoutTeamRetry shows a failure message with a Retry button that queues the
// failed read again.
func (s *screens) layoutTeamRetry(gtx *layout.Context, env *kit.Env, retry *kit.ButtonState, text string, op team.Operation, id, task string) layout.Dimensions {
	local := *gtx
	local.Constraints.Min.Y = 0
	return layout.Flex{Alignment: layout.Middle}.Layout(local,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions { return teamText(&g, env, text) }),
		spacerW(theme.SpaceMD),
		channelButton(env, s, retry, kit.ButtonProps{Text: "Retry", Size: kit.SizeSM}, func() {
			s.retryTeamRead(op, id, task)
		}),
	)
}

func teamText(gtx *layout.Context, env *kit.Env, text string) layout.Dimensions {
	return kit.EllipsisLabel(env, gtx, theme.TypeBody(), env.Th.Chrome().FgMuted, text)
}

func (s *screens) leadTeam(session string) (client.TeamSummary, bool) {
	i := slices.IndexFunc(s.teamSummaries, func(t client.TeamSummary) bool {
		return t.SessionID == session && (t.State == channelActive || t.State == teamFinalReview)
	})
	if i < 0 || session == "" {
		return client.TeamSummary{}, false
	}
	return s.teamSummaries[i], true
}

func (s *screens) olderTeamMessages(id string, p *teamChannelPane) {
	lines := s.teamLines[id]
	if len(lines) != 0 && !p.historyEnd {
		s.queueTeam(team.Request{Op: team.Page, Team: id, Before: lines[0].ChannelSeq})
	}
}

const (
	teamStateAsking  = "asking"
	teamStateUnknown = "unknown"
	teamStateDone    = "done"
	teamStateReview  = "review"
	teamNeedsLabel   = "Needs you"
	failedLabel      = "Failed"
	sendLabelText    = "Send"
	allLabel         = "All"
	allFilter        = "all"
	channelUser      = "user"
	teamFinalReview  = "final_review"
)

func (s *screens) saveTeamPosition(p *teamChannelPane) {
	// Layout saves encode asynchronously, so each document owns its own maps.
	positions := maps.Clone(s.grid.doc.TeamRead)
	if positions == nil {
		positions = make(map[string]map[string]int64)
	}
	positions[p.teamID] = maps.Clone(p.lastRead)
	s.grid.doc.TeamRead = positions
	s.grid.dirty = true
}

func teamBoardStatus(state string) kit.TagProps {
	status := kit.TagProps{Text: strings.ReplaceAll(state, "_", " "), Variant: kit.TagNeutral}
	if state == channelActive {
		status.Variant, status.Status = kit.TagStatus, theme.StatusSuccess
	}
	if state == teamFinalReview {
		status.Variant, status.Status = kit.TagStatus, theme.StatusWarning
	}
	return status
}
