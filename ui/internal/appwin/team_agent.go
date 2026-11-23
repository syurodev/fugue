package appwin

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/team"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const teamDiffID = "@team"

type teamAgentDetail struct {
	view       *client.TeamView
	recovery   []*kit.ButtonState
	section    *kit.TabsState
	selected   string
	message    *teamChannelComposer
	diff       *markdown.Doc
	diffLayout mdview.State
	diffList   layout.List
	inbox      *kit.ButtonState
}

func (s *screens) teamDetail(id, task string) *teamAgentDetail {
	key := id + "/" + task
	detail := s.teamAgents[key]
	if detail == nil {
		detail = &teamAgentDetail{
			section: kit.NewTabsState(), selected: "transcript", message: newTeamChannelComposer(),
			inbox: kit.NewButtonState(), diffList: layout.List{Axis: layout.Vertical},
			recovery: []*kit.ButtonState{kit.NewButtonState(), kit.NewButtonState(), kit.NewButtonState()},
		}
		s.teamAgents[key] = detail
	}
	return detail
}

func (s *screens) openTeamAgent(id, task string) {
	s.grid.openCompanion(SlotRef{Kind: KindAgent, ID: id + "/" + task})
}

func (s *screens) openTeamReview(id string) {
	s.openTeamAgent(id, teamDiffID)
}

func (s *screens) layoutTeamAgent(gtx *layout.Context, env *kit.Env, ref SlotRef) layout.Dimensions {
	id, task, ok := strings.Cut(ref.ID, "/")
	if !ok || id == "" || task == "" {
		return teamText(gtx, env, "Select an agent from a team board.")
	}
	if s.vault.Locked() {
		return teamText(gtx, env, "Unlock the vault to view this team.")
	}
	s.requestTeam(id)
	view, loaded := s.teamViews[id]
	if !loaded || view.VaultLocked {
		return teamText(gtx, env, "Loading team…")
	}
	detail := s.teamDetail(id, task)
	if task == teamDiffID {
		return s.layoutTeamReview(gtx, env, detail, &view)
	}
	for i := range view.Tasks {
		if view.Tasks[i].TaskID == task {
			return s.layoutAgentDetail(gtx, env, &view, &view.Tasks[i], detail)
		}
	}
	return teamText(gtx, env, "This task is no longer available.")
}

func (s *screens) layoutAgentDetail(gtx *layout.Context, env *kit.Env, view *client.TeamView, node *client.TaskNode, detail *teamAgentDetail) layout.Dimensions {
	return layout.UniformInset(theme.SpaceXL).Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutAgentOverview(g, env, node) }),
			spacerH(theme.SpaceLG),
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutAgentTabs(g, env, detail) }),
			spacerH(theme.SpaceMD),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				return teamPanel(g, env, env.Th.Chrome().SurfaceBase, layout.UniformInset(theme.SpaceMD), func(g layout.Context) layout.Dimensions {
					return s.layoutAgentPanel(&g, env, view.Summary.TeamID, node, detail)
				})
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return teamPanel(*g, env, env.Th.Chrome().FillSoft, layout.UniformInset(theme.SpaceMD), func(g layout.Context) layout.Dimensions {
					return s.layoutAgentActions(&g, env, view, node, detail)
				})
			}),
		)
	})
}

func (s *screens) layoutAgentOverview(gtx *layout.Context, env *kit.Env, node *client.TaskNode) layout.Dimensions {
	ch := env.Th.Chrome()
	return teamPanel(*gtx, env, ch.FillSoft, layout.UniformInset(theme.SpaceXL), func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidWidget(func(g *layout.Context) layout.Dimensions {
				return layout.Flex{Alignment: layout.Middle}.Layout(*g,
					rigidWidget(func(g *layout.Context) layout.Dimensions {
						return teamAvatar(g, env, teamVendorIcon(node.Vendor), env.Th.StatusTone(theme.StatusInfo))
					}),
					spacerW(theme.SpaceLG),
					layout.Flexed(1, func(g layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Vertical}.Layout(g,
							rigidParagraph(env, theme.TypeDisplay(), ch.Fg, teamRoleTitle(node)),
							rigidParagraph(env, theme.TypeCaption(), ch.FgMuted, teamAgentModel(node)),
						)
					}),
				)
			}),
			spacerH(theme.SpaceMD),
			rigidParagraph(env, theme.TypeBody(), ch.Fg, node.Goal),
			spacerH(theme.SpaceSM),
			rigidParagraph(env, theme.TypeCaption(), ch.FgMuted, "Write scope · "+strings.Join(node.WriteScope, ", ")),
			rigidParagraph(env, theme.TypeCaption(), ch.FgMuted, "Dependencies · "+strings.Join(node.DependsOn, ", ")),
			rigidWidget(func(g *layout.Context) layout.Dimensions { return s.layoutAgentChecks(g, env, node.SessionID) }),
		)
	})
}

func (s *screens) layoutAgentTabs(gtx *layout.Context, env *kit.Env, detail *teamAgentDetail) layout.Dimensions {
	dims, ev := detail.section.Layout(env, gtx, kit.TabsProps{
		Items:    []kit.TabItem{{ID: "transcript", Label: "Transcript"}, {ID: diffLabel, Label: "Diff"}},
		ActiveID: detail.selected,
	})
	s.fail(ev.Err)
	if ev.Selected != "" {
		detail.selected = ev.Selected
	}
	return dims
}

func (s *screens) layoutAgentPanel(gtx *layout.Context, env *kit.Env, id string, node *client.TaskNode, detail *teamAgentDetail) layout.Dimensions {
	if detail.selected == diffLabel {
		return s.layoutAgentDiff(gtx, env, id, node.TaskID, detail)
	}
	if node.SessionID == "" {
		return teamText(gtx, env, "The agent session has not started.")
	}
	s.ensureSessionTopics(node.SessionID)
	s.layoutChatContent(gtx, env, node.SessionID)
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

func (s *screens) layoutAgentActions(gtx *layout.Context, env *kit.Env, view *client.TeamView, node *client.TaskNode, detail *teamAgentDetail) layout.Dimensions {
	if node.State == teamStateUnknown {
		return s.layoutAgentRecovery(gtx, env, view.Summary.TeamID, node.TaskID, detail)
	}
	detail.message.direct = node.TaskID
	return detail.message.layout(gtx, env, s, view.Summary.TeamID, view, modelChannelThreads(view))
}

func (s *screens) layoutAgentRecovery(gtx *layout.Context, env *kit.Env, id, task string, detail *teamAgentDetail) layout.Dimensions {
	children := make([]layout.FlexChild, 0, len(detail.recovery))
	for i, option := range []struct{ label, id string }{{"Continue", "resume"}, {"Keep diff", "keep_diff"}, {"Cancel", "cancel"}} {
		children = append(children, channelButton(env, s, detail.recovery[i], kit.ButtonProps{Text: option.label, Size: kit.SizeSM}, func() {
			if s.vault.Unlocked() {
				s.queueTeam(team.Request{Op: team.Recover, Team: id, Task: task, Decision: option.id})
			}
		}))
	}
	return layout.Flex{}.Layout(*gtx, children...)
}

func (s *screens) layoutTeamReview(gtx *layout.Context, env *kit.Env, detail *teamAgentDetail, view *client.TeamView) layout.Dimensions {
	return layout.UniformInset(theme.SpaceMD).Layout(*gtx, func(g layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(g,
			rigidParagraph(env, theme.TypeTitle(), env.Th.Chrome().Fg, "Review & Integrate"),
			rigidParagraph(env, theme.TypeBody(), env.Th.Chrome().Fg, view.Title),
			channelButton(env, s, detail.inbox, kit.ButtonProps{Text: "Open in inbox", Size: kit.SizeSM}, func() {
				s.openTeamInbox(view.PendingRequestID)
			}),
			layout.Flexed(1, func(g layout.Context) layout.Dimensions {
				return s.layoutAgentDiff(&g, env, view.Summary.TeamID, teamDiffID, detail)
			}),
		)
	})
}

func (s *screens) layoutAgentDiff(gtx *layout.Context, env *kit.Env, id, task string, detail *teamAgentDetail) layout.Dimensions {
	if detail.view == nil {
		if s.failedTeamRead(team.Diff, id, task) {
			return teamText(gtx, env, "Diff unavailable.")
		}
		s.queueTeam(team.Request{Op: team.Diff, Team: id, Task: task})
		return teamText(gtx, env, "Loading diff…")
	}
	if detail.view.DiffText == "" {
		return teamText(gtx, env, "No diff available.")
	}
	if detail.diff == nil {
		detail.diff = markdown.Parse("````diff\n" + detail.view.DiffText + "\n````")
	}
	return detail.diffList.Layout(*gtx, 1, func(g layout.Context, _ int) layout.Dimensions {
		return mdview.LayoutEnv(env, &g, detail.diff, &detail.diffLayout)
	})
}

func (s *screens) layoutAgentChecks(gtx *layout.Context, env *kit.Env, sessionID string) layout.Dimensions {
	transcript := s.transcripts[sessionID]
	if transcript == nil {
		return layout.Dimensions{}
	}
	var passed, attention int
	for _, item := range transcript.Items() {
		if item.Metadata.Tool == "check" {
			if item.Metadata.Status == "succeeded" {
				passed++
			} else {
				attention++
			}
		}
	}
	if passed+attention == 0 {
		return layout.Dimensions{}
	}
	return teamText(gtx, env, fmt.Sprintf("Checks · %d passed · %d other", passed, attention))
}
