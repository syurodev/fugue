package appwin

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	proposalTool     = "fugue_team_propose"
	proposalDecision = "decision"
	proposalApprove  = "approve"
	proposalEdited   = "approve_edited"
	proposalSolo     = "solo"
	proposalReject   = "reject"
	proposalRoleLead = "lead"
)

// The host accepts this versioned edit document in the proposal question's
// text field. Task IDs in it are plan-local, while GetTeam returns durable IDs.
type proposalEditSet struct {
	Version     int                 `json:"version"`
	Roles       []proposalRoleEdit  `json:"roles,omitempty"`
	RemoveTasks []string            `json:"remove_tasks,omitempty"`
	AddTasks    []proposalAddedTask `json:"add_tasks,omitempty"`
	TeamBudget  *proposalBudget     `json:"team_budget,omitempty"`
}

type proposalRoleEdit struct {
	Name  string `json:"name"`
	Model string `json:"model"`
}

type proposalBudget struct {
	MaxAgents          uint32 `json:"max_agents,omitempty"`
	MaxDurationSeconds int64  `json:"max_duration_seconds,omitempty"`
}

type proposalAddedTask struct {
	ID          string   `json:"id"`
	ParentID    string   `json:"parent_task_id"`
	TeamRole    string   `json:"team_role"`
	Role        string   `json:"role"`
	Goal        string   `json:"goal"`
	WriteScope  []string `json:"write_scope"`
	Deliverable string   `json:"deliverable"`
}

type proposalCard struct {
	teamID               string
	models               map[string]*kit.ComboboxState
	originalModels       map[string]string
	removed              map[string]bool
	removeButtons        map[string]*kit.ButtonState
	editedBadges         map[string]*kit.TagState
	maxAgents, deadline  *kit.TextFieldState
	originalBudget       proposalBudget
	addRole              *kit.SelectState
	selectedRole         string
	addGoal              *kit.TextFieldState
	addButton, addToggle *kit.ButtonState
	addOpen              bool
	added                []proposalAddedTask
	nextTask             int
}

func isProposalRequest(req *client.PendingInteraction) bool {
	return req.Tool == proposalTool && req.Kind == client.InteractionKindQuestion
}

func (s *screens) openProposalInInbox(teamID string) {
	s.grid.openCompanion(SlotRef{Kind: KindInbox})
	if teamID == "" {
		return
	}
	s.inboxView.proposalTeam = teamID
	s.requestTeam(teamID)
	s.selectProposalRequest()
}

func (s *screens) selectProposalRequest() {
	id := s.inboxView.proposalTeam
	if id == "" {
		return
	}
	view := s.teamViews[id]
	items := s.inbox.Items()
	for i := range items {
		req := &items[i]
		if isProposalRequest(req) && req.RequestID == view.PendingRequestID {
			s.inboxView.active = inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
			s.inboxView.proposalTeam = ""
			return
		}
	}
}

func (s *screens) proposalView(req *client.PendingInteraction) (client.TeamView, bool) {
	for id := range s.teamViews {
		view := s.teamViews[id]
		if view.PendingRequestID == req.RequestID && view.Summary.SessionID == req.SessionID {
			s.requestTeam(id) // the card being drawn keeps the team stream open
			return view, !view.VaultLocked
		}
	}
	for i := range s.teamSummaries {
		sum := &s.teamSummaries[i]
		if sum.SessionID == req.SessionID && sum.State == "proposed" {
			s.requestTeam(sum.TeamID)
		}
	}
	return client.TeamView{}, false
}

func newProposalCard(view *client.TeamView) *proposalCard {
	p := &proposalCard{
		teamID: view.Summary.TeamID,
		models: make(map[string]*kit.ComboboxState), originalModels: make(map[string]string),
		removed: make(map[string]bool), removeButtons: make(map[string]*kit.ButtonState),
		editedBadges: make(map[string]*kit.TagState),
		maxAgents:    kit.NewTextFieldState(), deadline: kit.NewTextFieldState(),
		addRole: kit.NewSelectState(), addGoal: kit.NewTextFieldState(), addButton: kit.NewButtonState(), addToggle: kit.NewButtonState(),
	}
	if b := view.Budget; b != nil {
		p.originalBudget = proposalBudget{MaxAgents: b.MaxAgents, MaxDurationSeconds: b.MaxDurationSeconds}
		p.maxAgents.SetText(strconv.FormatUint(uint64(b.MaxAgents), 10))
		p.deadline.SetText(strconv.FormatInt(b.MaxDurationSeconds, 10))
	}
	for i := range view.Tasks {
		node := &view.Tasks[i]
		if _, exists := p.models[node.CatalogRole]; !exists {
			field := kit.NewComboboxState()
			field.SetText(node.Model)
			p.models[node.CatalogRole] = field
			p.originalModels[node.CatalogRole] = node.Model
		}
	}
	return p
}

func (p *proposalCard) edits() (proposalEditSet, bool) {
	edits := proposalEditSet{Version: 1, AddTasks: p.added}
	for _, role := range slices.Sorted(maps.Keys(p.models)) {
		if next := strings.TrimSpace(p.models[role].Text()); next != p.originalModels[role] {
			if next == "" {
				return edits, false
			}
			edits.Roles = append(edits.Roles, proposalRoleEdit{Name: role, Model: next})
		}
	}
	edits.RemoveTasks = slices.Sorted(maps.Keys(p.removed))
	b, valid := p.budget()
	if b != p.originalBudget {
		edits.TeamBudget = &b
	}
	return edits, valid
}

func (p *proposalCard) budget() (proposalBudget, bool) {
	agents, okAgents := parseProposalLimit(p.maxAgents.Text(), 64)
	if agents < 0 || agents > 64 || !okAgents {
		return proposalBudget{}, false
	}
	deadline, okDeadline := parseProposalLimit(p.deadline.Text(), 604800)
	return proposalBudget{MaxAgents: uint32(agents), MaxDurationSeconds: deadline}, okDeadline
}

func parseProposalLimit(text string, maximum int64) (int64, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(text, 10, 32)
	return n, err == nil && n >= 0 && n <= maximum
}

func (e *proposalEditSet) changed() bool {
	return len(e.Roles)+len(e.RemoveTasks)+len(e.AddTasks) > 0 || e.TeamBudget != nil
}

func submitProposal(sc *screens, form *cardFormState, req *client.PendingInteraction, choice string, edits *proposalEditSet) {
	if !proposalOffers(req, choice) {
		return
	}
	var note string
	switch choice {
	case proposalEdited:
		blob, err := json.Marshal(edits)
		if err != nil {
			sc.fail(err)
			return
		}
		note = string(blob)
	case proposalSolo, proposalReject:
		note = form.denyNote.Text()
	}
	pin := inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
	submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{
		Outcome: client.InteractionOutcomeAnswered,
		Answers: []client.InteractionQuestionAnswer{{
			QuestionID: proposalDecision, OptionIDs: []string{choice}, Text: note,
		}},
	})
}

func proposalOffers(req *client.PendingInteraction, option string) bool {
	for _, q := range req.Questions {
		if q.QuestionID == proposalDecision {
			return slices.ContainsFunc(q.Options, func(o client.InteractionOption) bool { return o.ID == option })
		}
	}
	return false
}

func layoutProposalHeader(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) layout.Dimensions {
	ch := env.Th.Chrome()
	return layout.Flex{Alignment: layout.Middle}.Layout(*gtx,
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamAvatar(g, env, kit.IconUsers, env.Th.StatusTone(theme.StatusInfo))
		}),
		spacerW(theme.SpaceLG),
		layout.Flexed(1, func(g layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(g,
				rigidWidget(func(g *layout.Context) layout.Dimensions {
					g.Constraints.Min.X = 0
					dims, err := form.kindTag.Layout(env, g, kit.TagProps{Text: "Team proposal", Variant: kit.TagStatus, Status: theme.StatusInfo})
					sc.fail(err)
					return dims
				}),
				spacerH(theme.SpaceXS),
				rigidParagraph(env, theme.TypeDisplay(), ch.Fg, req.Summary),
			)
		}),
	)
}

func layoutProposalBody(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) layout.Dimensions {
	view, ready := sc.proposalView(req)
	if !ready {
		return kit.Paragraph(env, gtx, theme.TypeBody(), env.Th.Chrome().FgMuted, "Loading the proposed team…")
	}
	if form.proposal == nil || form.proposal.teamID != view.Summary.TeamID {
		form.proposal = newProposalCard(&view)
	}
	p := form.proposal
	ch := env.Th.Chrome()
	rows := []layout.FlexChild{
		spacerH(theme.SpaceLG),
		rigidParagraph(env, theme.TypeBody(), ch.Fg, view.Brief),
		spacerH(theme.SpaceXL),
		rigidParagraph(env, theme.TypeHeading(), ch.Fg, fmt.Sprintf("Proposed team · %d agents", proposedAgentCount(view.Tasks))),
		spacerH(theme.SpaceMD),
	}
	rows = append(rows, proposalPlanRows(env, sc, &view, p, form.busy)...)
	rows = append(rows, proposalBudgetRows(env, sc, p, form.busy)...)
	if req.Body != "" {
		rows = append(rows, spacerH(theme.SpaceLG), layoutProposalObservations(env, req.Body))
	}
	rows = append(rows, p.addTaskRows(env, sc, &view, form.busy)...)
	rows = append(rows, spacerH(theme.SpaceMD),
		rigidTextArea(env, form.denyNote, "Note (optional)", "For Work solo or Reject", form.busy),
		spacerH(theme.SpaceMD))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, rows...)
}

func proposalPlanRows(env *kit.Env, sc *screens, view *client.TeamView, p *proposalCard, busy bool) []layout.FlexChild {
	firstRole := make(map[string]bool)
	tree := proposalTree(view.Tasks)
	rows := make([]layout.FlexChild, 0, 2*len(tree))
	for i := range tree {
		item := &tree[i]
		node := item.node
		options := proposalModelOptions(view.Tasks, sc.teamCatalog[view.Summary.WorkspaceID], node.Vendor)
		primary := !firstRole[node.CatalogRole]
		if !p.removed[proposalTaskLocalID(p.teamID, node.TaskID)] {
			firstRole[node.CatalogRole] = true
		}
		rows = append(rows, proposalNodeRow(env, sc, p, item, options, primary, busy), spacerH(theme.SpaceSM))
	}
	return rows
}

func layoutProposalObservations(env *kit.Env, details string) layout.FlexChild {
	ch := env.Th.Chrome()
	return rigidWidget(func(g *layout.Context) layout.Dimensions {
		return teamPanel(*g, env, ch.FillSoft, layout.UniformInset(theme.SpaceLG), func(g layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(g,
				rigidParagraph(env, theme.TypeLabel(), ch.Fg, "Usage & warnings"),
				spacerH(theme.SpaceXS),
				rigidParagraph(env, theme.TypeSmall(), ch.FgMuted, details),
			)
		})
	})
}

func proposedAgentCount(nodes []client.TaskNode) int {
	n := 0
	for i := range nodes {
		if nodes[i].HierarchyRole != proposalRoleLead {
			n++
		}
	}
	return n
}

func layoutProposalFooter(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState, pin inbox.Pin) layout.Dimensions {
	edits, valid := proposalEditSet{}, false
	if form.proposal != nil {
		edits, valid = form.proposal.edits()
	}
	choice, primaryLabel := proposalApprove, "Approve"
	if edits.changed() {
		choice, primaryLabel = proposalEdited, "Approve with edits"
	}
	busy := form.busy || !sc.inbox.CanAnswer(pin)
	gap, rowGap := flowGap(gtx)
	return kit.FlowRow(gtx, gap, rowGap, []kit.FlowItem{
		flowButton(env, gtx, sc.fail, form.allowOnceBtn, kit.ButtonProps{
			Text: primaryLabel, Variant: kit.Primary, Size: kit.SizeSM, Disabled: busy || !valid || !proposalOffers(req, choice),
		}, func() { submitProposal(sc, form, req, choice, &edits) }),
		flowButton(env, gtx, sc.fail, form.cancelBtn, kit.ButtonProps{
			Text: "Work solo", Variant: kit.Secondary, Size: kit.SizeSM, Disabled: busy || !proposalOffers(req, proposalSolo),
		}, func() { submitProposal(sc, form, req, proposalSolo, nil) }),
		flowButton(env, gtx, sc.fail, form.denyBtn, kit.ButtonProps{
			Text: "Reject", Variant: kit.Ghost, Size: kit.SizeSM, Disabled: busy || !proposalOffers(req, proposalReject),
		}, func() { submitProposal(sc, form, req, proposalReject, nil) }),
	})
}
