package appwin

import (
	"fmt"
	"slices"
	"strings"

	"gioui.org/layout"
	"gioui.org/unit"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/team"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type proposalTreeNode struct {
	node  *client.TaskNode
	depth int
}

func proposalTree(tasks []client.TaskNode) []proposalTreeNode {
	children := make(map[string][]*client.TaskNode)
	for i := range tasks {
		task := &tasks[i]
		children[task.ParentTaskID] = append(children[task.ParentTaskID], task)
	}
	var ordered []proposalTreeNode
	seen := make(map[string]bool, len(tasks))
	var walk func(*client.TaskNode, int)
	walk = func(task *client.TaskNode, depth int) {
		if seen[task.TaskID] {
			return
		}
		seen[task.TaskID] = true
		ordered = append(ordered, proposalTreeNode{node: task, depth: depth})
		for _, child := range children[task.TaskID] {
			walk(child, depth+1)
		}
	}
	for _, root := range children[""] {
		walk(root, 0)
	}
	for i := range tasks {
		task := &tasks[i]
		walk(task, 0)
	}
	return ordered
}

func proposalModelOptions(tasks []client.TaskNode, catalog []team.Role, vendor string) []kit.SuggestItem {
	seen := make(map[string]bool, len(tasks))
	var options []kit.SuggestItem
	for i := range tasks {
		task := &tasks[i]
		if task.Vendor == vendor && task.Model != "" && !seen[task.Model] {
			seen[task.Model] = true
			options = append(options, kit.SuggestItem{ID: task.Model, Label: task.Model})
		}
	}
	for _, role := range catalog {
		if role.Vendor == vendor && role.Model != "" && !seen[role.Model] {
			seen[role.Model] = true
			options = append(options, kit.SuggestItem{ID: role.Model, Label: role.Model})
		}
	}
	return options
}

func proposalNodeRow(env *kit.Env, sc *screens, p *proposalCard, item *proposalTreeNode, options []kit.SuggestItem, primary, busy bool) layout.FlexChild {
	task := item.node
	id := proposalTaskLocalID(p.teamID, task.TaskID)
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layout.Inset{Left: theme.SpaceMD * unit.Dp(min(item.depth, 3))}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			fill := env.Th.Chrome().FillSoft
			if p.changedModel(task.CatalogRole) && !p.removed[id] {
				fill = env.Th.StatusTone(theme.StatusInfo).Fill
			}
			return teamPanel(local, env, fill, layout.UniformInset(theme.SpaceLG), func(g layout.Context) layout.Dimensions {
				return layoutProposalNode(&g, env, sc, p, task, id, options, primary, busy)
			})
		})
	})
}

func proposalTaskLocalID(teamID, taskID string) string {
	return strings.TrimPrefix(taskID, teamID+".")
}

func layoutProposalNode(gtx *layout.Context, env *kit.Env, sc *screens, p *proposalCard, task *client.TaskNode, id string, options []kit.SuggestItem, primary, busy bool) layout.Dimensions {
	ch := env.Th.Chrome()
	field := p.models[task.CatalogRole]
	removed := p.removed[id]
	header := []layout.FlexChild{rigidLabel(env, theme.TypeLabel(), ch.Fg, task.CatalogRole+" · "+task.HierarchyRole)}
	if removed || p.changedModel(task.CatalogRole) {
		header = append(header, spacerW(theme.SpaceXS), rigidTag(env, sc.fail,
			proposalEditedBadge(p, task.TaskID), kit.TagProps{Text: "Edited", Variant: kit.TagStatus, Status: theme.StatusInfo}))
	}
	rows := []layout.FlexChild{rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, header...)
	})}
	if !removed {
		rows = append(rows, proposalNodeFields(env, sc, task, field, options, primary, busy)...)
	} else {
		rows = append(rows, rigidLabel(env, theme.TypeCaption(), ch.FgMuted, "Will be removed"))
	}
	if task.HierarchyRole != proposalRoleLead {
		label := "Remove"
		if removed {
			label = "Undo removal"
		}
		rows = append(rows, rigidInboxButton(env, sc.fail, proposalRemoveButton(p, id),
			kit.ButtonProps{Text: label, Variant: kit.Ghost, Size: kit.SizeSM, Disabled: busy},
			func() { p.toggleRemoval(id) }))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, rows...)
}

func (p *proposalCard) changedModel(role string) bool {
	return strings.TrimSpace(p.models[role].Text()) != p.originalModels[role]
}

func (p *proposalCard) toggleRemoval(id string) {
	if p.removed[id] {
		delete(p.removed, id)
		return
	}
	p.removed[id] = true
}

func proposalEditedBadge(p *proposalCard, id string) *kit.TagState {
	if p.editedBadges[id] == nil {
		p.editedBadges[id] = kit.NewTagState()
	}
	return p.editedBadges[id]
}

func proposalRemoveButton(p *proposalCard, id string) *kit.ButtonState {
	if p.removeButtons[id] == nil {
		p.removeButtons[id] = kit.NewButtonState()
	}
	return p.removeButtons[id]
}

func proposalNodeFields(env *kit.Env, sc *screens, task *client.TaskNode, field *kit.ComboboxState, options []kit.SuggestItem, primary, busy bool) []layout.FlexChild {
	ch := env.Th.Chrome()
	rows := []layout.FlexChild{rigidParagraph(env, theme.TypeSmall(), ch.Fg, task.Goal)}
	for _, meta := range []struct {
		name  string
		items []string
	}{
		{"Write scope", task.WriteScope},
		{"Depends on", task.DependsOn},
	} {
		if len(meta.items) > 0 {
			rows = append(rows, rigidParagraph(env, theme.TypeCaption(), ch.FgMuted, meta.name+": "+strings.Join(meta.items, ", ")))
		}
	}
	rows = append(rows, rigidParagraph(env, theme.TypeCaption(), ch.FgMuted, sc.proposalAccountLabel(task)))
	if !primary {
		return append(rows, rigidLabel(env, theme.TypeCaption(), ch.FgMuted, "Role model: "+strings.TrimSpace(field.Text())))
	}
	return append(rows, spacerH(theme.SpaceXS), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		p := kit.ComboboxProps{Label: "Role model", Placeholder: "Vendor default", Items: options, Disabled: busy, Block: true}
		_, err := field.Layout(env, gtx, &p)
		if !gtx.Focused(field.Tag()) {
			field.Close()
		}
		sc.fail(err)
		return field.Measure(env, gtx, &p)
	}))
}

func (s *screens) proposalAccountLabel(task *client.TaskNode) string {
	acc, ok := s.accounts.Account(task.Vendor)
	if task.AccountID != "" {
		return "Account: " + task.AccountID
	}
	if !ok {
		return "Account chosen by host"
	}
	label := acc.Login
	if label == "" {
		label = acc.Vendor
	}
	if !acc.QuotaKnown || len(acc.Windows) == 0 {
		return "Account: " + label + " · quota unknown"
	}
	var used float64
	for _, window := range acc.Windows {
		used = max(used, window.Utilization)
	}
	return fmt.Sprintf("Account: %s · %.0f%% quota used", label, used*100)
}

func proposalBudgetRows(env *kit.Env, sc *screens, p *proposalCard, busy bool) []layout.FlexChild {
	ch := env.Th.Chrome()
	return []layout.FlexChild{
		spacerH(theme.SpaceLG),
		rigidWidget(func(g *layout.Context) layout.Dimensions {
			return teamPanel(*g, env, ch.FillSoft, layout.UniformInset(theme.SpaceLG), func(g layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Vertical}.Layout(g,
					rigidLabel(env, theme.TypeLabel(), ch.Fg, "Team budget"),
					spacerH(theme.SpaceMD),
					rigidWidget(func(g *layout.Context) layout.Dimensions {
						return layout.Flex{}.Layout(*g,
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layoutProposalTextField(env, sc, &gtx, p.maxAgents, "Max agents", "Default", busy)
							}),
							spacerW(theme.SpaceMD),
							layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
								return layoutProposalTextField(env, sc, &gtx, p.deadline, "Deadline (sec.)", "Default", busy)
							}),
						)
					}),
				)
			})
		}),
	}
}

func layoutProposalTextField(env *kit.Env, sc *screens, gtx *layout.Context, st *kit.TextFieldState, label, placeholder string, busy bool) layout.Dimensions {
	props := kit.TextFieldProps{Label: label, Placeholder: placeholder, Disabled: busy}
	local := *gtx
	local.Constraints.Min.X = local.Constraints.Max.X
	_, err := st.Layout(env, &local, &props)
	sc.fail(err)
	return st.Measure(env, &local, &props)
}

func (p *proposalCard) addTaskRows(env *kit.Env, sc *screens, view *client.TeamView, busy bool) []layout.FlexChild {
	var root string
	for i := range view.Tasks {
		node := &view.Tasks[i]
		if node.HierarchyRole == proposalRoleLead {
			root = proposalTaskLocalID(p.teamID, node.TaskID)
			break
		}
	}
	if root == "" {
		return nil
	}
	rows := make([]layout.FlexChild, 0, 2*len(p.added)+7)
	for _, task := range p.added {
		rows = append(rows, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
				rigidTag(env, sc.fail, proposalEditedBadge(p, task.ID),
					kit.TagProps{Text: "Edited", Variant: kit.TagStatus, Status: theme.StatusInfo}),
				spacerW(theme.SpaceXS),
				rigidParagraph(env, theme.TypeSmall(), env.Th.Chrome().Fg, task.Role+" · "+task.Goal),
			)
		}), rigidInboxButton(env, sc.fail, proposalRemoveButton(p, task.ID),
			kit.ButtonProps{Text: "Undo addition", Variant: kit.Ghost, Size: kit.SizeSM, Disabled: busy},
			func() {
				p.added = slices.DeleteFunc(p.added, func(a proposalAddedTask) bool { return a.ID == task.ID })
			}))
	}
	roles := proposalAddRoles(sc, view)
	if len(roles) == 0 {
		return rows
	}
	rows = append(rows, spacerH(theme.SpaceLG), channelButton(env, sc, p.addToggle, kit.ButtonProps{
		Text: "Add an agent", Icon: kit.IconPlus, Variant: kit.Bordered, Size: kit.SizeSM, Disabled: busy,
	}, func() { p.addOpen = !p.addOpen }))
	if !p.addOpen {
		return rows
	}
	return append(rows, spacerH(theme.SpaceLG),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := kit.SelectProps{Label: "Role for new task", Placeholder: "Choose a role", Value: p.selectedRole, Options: roles, Disabled: busy, Block: true}
			ev, err := p.addRole.Layout(env, gtx, &props)
			sc.fail(err)
			if ev.Changed {
				p.selectedRole = ev.Value
			}
			return p.addRole.Measure(env, gtx, &props)
		}),
		spacerH(theme.SpaceLG),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutProposalTextField(env, sc, gtx, p.addGoal, "New task goal", "Describe the task", busy)
		}),
		spacerH(theme.SpaceLG),
		rigidInboxButton(env, sc.fail, p.addButton, kit.ButtonProps{
			Text: "Add task", Variant: kit.Secondary, Size: kit.SizeMD,
			Disabled: busy || p.selectedRole == "" || strings.TrimSpace(p.addGoal.Text()) == "",
		}, func() { p.addTask(view.Tasks, root); p.addOpen = false }))
}

func proposalAddRoles(sc *screens, view *client.TeamView) []kit.SelectOption {
	roles := make(map[string]bool)
	var options []kit.SelectOption
	for _, role := range sc.teamCatalog[view.Summary.WorkspaceID] {
		roles[role.Name] = true
		options = append(options, kit.SelectOption{ID: role.Name, Label: role.Name})
	}
	for i := range view.Tasks {
		task := &view.Tasks[i]
		role := task.CatalogRole
		if role != "" && !roles[role] {
			roles[role] = true
			options = append(options, kit.SelectOption{ID: role, Label: role})
		}
	}
	return options
}

func (p *proposalCard) addTask(nodes []client.TaskNode, root string) {
	var id string
	for {
		p.nextTask++
		id = fmt.Sprintf("added-%d", p.nextTask)
		if !slices.ContainsFunc(nodes, func(t client.TaskNode) bool { return t.TaskID == p.teamID+"."+id }) {
			break
		}
	}
	p.added = append(p.added, proposalAddedTask{
		ID: id, ParentID: root, TeamRole: "member",
		Role: p.selectedRole, Goal: strings.TrimSpace(p.addGoal.Text()), WriteScope: []string{}, Deliverable: "report",
	})
	p.selectedRole = ""
	p.addGoal.SetText("")
}
