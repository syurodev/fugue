package appwin

import (
	"image"
	"strconv"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type intFieldSpec struct {
	id     string
	label  string
	helper string
	unit   string
	min    int
	max    int
	get    func(*client.WorkspaceSettings) int
	set    func(*client.WorkspaceSettings, int)
}

func teamIntFieldSpecs() []intFieldSpec {
	return []intFieldSpec{
		{
			id: settings.FieldIdleDetachSeconds, label: "Idle detach timeout",
			helper: "0 disables idle detach.", unit: "s",
			min: 0, max: settings.MaxIdleDetachSeconds,
			get: func(s *client.WorkspaceSettings) int { return s.IdleDetachSeconds },
			set: func(s *client.WorkspaceSettings, v int) { s.IdleDetachSeconds = v },
		},
		{
			id: settings.FieldTaskDeadlineSeconds, label: "Task deadline",
			helper: "Child task wall-clock limit.", unit: "s",
			min: 1, max: settings.MaxTeamDeadlineSeconds,
			get: func(s *client.WorkspaceSettings) int { return s.TaskDeadlineSeconds },
			set: func(s *client.WorkspaceSettings, v int) { s.TaskDeadlineSeconds = v },
		},
		{
			id: settings.FieldTaskMaxTurns, label: "Task max turns",
			helper: "Max vendor turns per task.", unit: "turns",
			min: 1, max: settings.MaxTaskTurns,
			get: func(s *client.WorkspaceSettings) int { return s.TaskMaxTurns },
			set: func(s *client.WorkspaceSettings, v int) { s.TaskMaxTurns = v },
		},
		{
			id: settings.FieldSilenceTimeoutSeconds, label: "Silence timeout",
			helper: "0 disables stall checks.", unit: "s",
			min: 0, max: settings.MaxSilenceTimeoutSeconds,
			get: func(s *client.WorkspaceSettings) int { return s.SilenceTimeoutSeconds },
			set: func(s *client.WorkspaceSettings, v int) { s.SilenceTimeoutSeconds = v },
		},
		{
			id: settings.FieldTeamDeadlineSeconds, label: "Team deadline",
			helper: "Active team dispatch window.", unit: "s",
			min: 1, max: settings.MaxTeamDeadlineSeconds,
			get: func(s *client.WorkspaceSettings) int { return s.TeamDeadlineSeconds },
			set: func(s *client.WorkspaceSettings, v int) { s.TeamDeadlineSeconds = v },
		},
		{
			id: settings.FieldTeamMaxAgents, label: "Team max agents",
			helper: "Max non-lead tasks.", unit: "agents",
			min: 1, max: settings.MaxTeamAgents,
			get: func(s *client.WorkspaceSettings) int { return s.TeamMaxAgents },
			set: func(s *client.WorkspaceSettings, v int) { s.TeamMaxAgents = v },
		},
	}
}

func approvalModeOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: "plan", Label: "plan"},
		{ID: "ask", Label: "ask"},
		{ID: "auto", Label: "auto"},
		{ID: "full", Label: "full"},
	}
}

func sandboxProfileOptions() []kit.SelectOption {
	return []kit.SelectOption{
		{ID: "read-only", Label: "read-only"},
		{ID: "workspace-write", Label: "workspace-write"},
		{ID: "full-access", Label: "full-access"},
	}
}

func (v *settingsView) layoutTeamsCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	dims, _, err := v.wsCard.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		children := []layout.FlexChild{
			rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "Team defaults & budgets"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Non-policy defaults for team execution and idle detach. Edits write to the workspace tier."),
			spacerH(theme.SpaceMD),
		}
		specs := teamIntFieldSpecs()
		for i := range specs {
			spec := specs[i]
			children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutTeamIntField(gtx, env, sc, &spec)
			}))
			if i < len(specs)-1 {
				children = append(children, spacerH(theme.SpaceMD))
			}
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	sc.fail(err)
	return dims
}

func (v *settingsView) layoutTeamIntField(gtx *layout.Context, env *kit.Env, sc *screens, spec *intFieldSpec) layout.Dimensions {
	w := v.ensureIntField(spec.id)
	minF, maxF := float64(spec.min), float64(spec.max)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutFieldMetaRow(gtx, env, sc, spec.id, "")
		}),
		spacerH(theme.SpaceXXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.TextFieldProps{
				Label:    spec.label,
				Helper:   spec.helper,
				Error:    w.err,
				Variant:  kit.TextFieldNumber,
				Unit:     spec.unit,
				Min:      &minF,
				Max:      &maxF,
				Disabled: !sc.settings.Loaded || sc.settings.Saving,
			}
			local := *gtx
			local.Constraints.Min.X = local.Constraints.Max.X
			ev, err := w.field.Layout(env, &local, props)
			sc.fail(err)
			if ev.Changed {
				parsed, parseErr := settings.ParseBoundedInt(w.field.Text(), spec.min, spec.max)
				if parseErr != nil {
					w.err = parseErr.Error()
				} else {
					w.err = ""
					spec.set(&sc.settings.Draft, parsed)
					sc.settings.PolicyConfirmArmed = false
				}
			}
			dims := w.field.Measure(env, &local, props)
			dims.Size.X = local.Constraints.Max.X
			return dims
		}),
	)
}

func (v *settingsView) layoutPolicyCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	dims, _, err := v.policyCard.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "Policy & sandbox"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Policy keys require an explicit confirmation step before saving and are never auto-saved."),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutPolicySelect(gtx, env, sc, settings.FieldApprovalMode, "Approval mode", "Default approval mode for new sessions.", v.approvalSelect, approvalModeOptions(), sc.settings.Draft.ApprovalMode, func(val string) {
					sc.settings.Draft.ApprovalMode = val
				})
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutPolicySelect(gtx, env, sc, settings.FieldSandboxProfile, "Sandbox profile", "Default filesystem and network profile for new sessions.", v.sandboxSelect, sandboxProfileOptions(), sc.settings.Draft.SandboxProfile, func(val string) {
					sc.settings.Draft.SandboxProfile = val
				})
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutShellAllowField(gtx, env, sc)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutEnvTagsField(gtx, env, sc)
			}),
			spacerH(theme.SpaceMD),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutPolicySwitch(gtx, env, sc, settings.FieldSessionTeams, "Offer team proposals", v.teamsSwitch, sc.settings.Draft.SessionTeams, func(next bool) {
					sc.settings.Draft.SessionTeams = next
				})
			}),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return v.layoutPolicySwitch(gtx, env, sc, settings.FieldTeamsAutoApprove, "Auto-approve read-only team proposals (max 3 agents)", v.autoApproveSwitch, sc.settings.Draft.TeamsAutoApprove, func(next bool) {
					sc.settings.Draft.TeamsAutoApprove = next
				})
			}),
		)
	})
	sc.fail(err)
	return dims
}

func (v *settingsView) layoutPolicySelect(
	gtx *layout.Context, env *kit.Env, sc *screens,
	fieldID, label, helper string, sel *kit.SelectState,
	opts []kit.SelectOption, value string, onPick func(string),
) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutFieldMetaRow(gtx, env, sc, fieldID, "policy")
		}),
		spacerH(theme.SpaceXXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.SelectProps{
				Label:    label,
				Helper:   helper,
				Options:  opts,
				Value:    value,
				Disabled: !sc.settings.Loaded || sc.settings.Saving,
			}
			local := *gtx
			res, err := sel.Layout(env, &local, props)
			sc.fail(err)
			if res.Changed {
				onPick(res.Value)
				sc.settings.PolicyConfirmArmed = false
			}
			return sel.Measure(env, gtx, props)
		}),
	)
}

func (v *settingsView) layoutShellAllowField(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutFieldMetaRow(gtx, env, sc, settings.FieldShellAllow, "policy")
		}),
		spacerH(theme.SpaceXXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.TextFieldProps{
				Label:       "Shell allow prefixes",
				Placeholder: "git status, go test",
				Helper:      "Comma-separated command prefixes permitted without confirmation.",
				Variant:     kit.TextFieldText,
				Disabled:    !sc.settings.Loaded || sc.settings.Saving,
			}
			local := *gtx
			local.Constraints.Min.X = local.Constraints.Max.X
			ev, err := v.shellAllowField.Layout(env, &local, props)
			sc.fail(err)
			if ev.Changed {
				sc.settings.Draft.ShellAllow = settings.ParseShellAllow(v.shellAllowField.Text())
				sc.settings.PolicyConfirmArmed = false
			}
			dims := v.shellAllowField.Measure(env, &local, props)
			dims.Size.X = local.Constraints.Max.X
			return dims
		}),
	)
}

func (v *settingsView) layoutEnvTagsField(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutFieldMetaRow(gtx, env, sc, settings.FieldEnvTags, "policy")
		}),
		spacerH(theme.SpaceXXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.TextFieldProps{
				Label:       "Environment tags",
				Placeholder: "*.prod=prod",
				Helper:      "Comma-separated match=tag rules.",
				Error:       v.envTagsErr,
				Variant:     kit.TextFieldText,
				Disabled:    !sc.settings.Loaded || sc.settings.Saving,
			}
			local := *gtx
			local.Constraints.Min.X = local.Constraints.Max.X
			ev, err := v.envTagsField.Layout(env, &local, props)
			sc.fail(err)
			if ev.Changed {
				rules, parseErr := settings.ParseEnvTags(v.envTagsField.Text())
				if parseErr != nil {
					v.envTagsErr = parseErr.Error()
				} else {
					v.envTagsErr = ""
					sc.settings.Draft.EnvTags = rules
					sc.settings.PolicyConfirmArmed = false
				}
			}
			dims := v.envTagsField.Measure(env, &local, props)
			dims.Size.X = local.Constraints.Max.X
			return dims
		}),
	)
}

func (v *settingsView) layoutPolicySwitch(
	gtx *layout.Context, env *kit.Env, sc *screens,
	fieldID, label string, sw *kit.SwitchState,
	checked bool, onToggle func(bool),
) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutFieldMetaRow(gtx, env, sc, fieldID, "policy")
		}),
		spacerH(theme.SpaceXXS),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := kit.SwitchProps{
				Label:    label,
				Checked:  checked,
				Disabled: !sc.settings.Loaded || sc.settings.Saving,
			}
			local := *gtx
			toggled, err := sw.Layout(env, &local, props)
			sc.fail(err)
			if toggled {
				onToggle(!checked)
				sc.settings.PolicyConfirmArmed = false
			}
			return sw.Measure(env, gtx, props)
		}),
	)
}

func (v *settingsView) layoutFieldMetaRow(gtx *layout.Context, env *kit.Env, sc *screens, fieldID, kindBadge string) layout.Dimensions {
	tier := sc.settings.TierFor(fieldID)
	tierTag := ensureTagState(v.tierTags, fieldID)
	children := []layout.FlexChild{
		rigidTag(env, sc.fail, tierTag, kit.TagProps{
			Text:    "tier: " + settings.TierLabel(tier),
			Variant: tierTagVariant(tier),
			Status:  theme.StatusInfo,
		}),
	}
	if kindBadge != "" {
		badgeTag := ensureTagState(v.policyTags, fieldID)
		children = append(children, spacerW(theme.SpaceXS), rigidTag(env, sc.fail, badgeTag, kit.TagProps{
			Text:    kindBadge,
			Variant: kit.TagStatus,
			Status:  theme.StatusWarning,
		}))
	}
	if isFieldDirty(sc.settings, fieldID) {
		dirtyTag := ensureTagState(v.dirtyTags, fieldID)
		children = append(children, spacerW(theme.SpaceXS), rigidTag(env, sc.fail, dirtyTag, kit.TagProps{
			Text:    "unsaved",
			Variant: kit.TagStatus,
			Status:  theme.StatusInfo,
		}))
	}
	children = append(children, layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
	}))
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, children...)
}

func tierTagVariant(tier int64) kit.TagVariant {
	if tier == client.ConfigTierWorkspace || tier == client.ConfigTierSession {
		return kit.TagStatus
	}
	return kit.TagNeutral
}

func isFieldDirty(m *settings.Model, fieldID string) bool {
	for _, id := range m.DirtyFields() {
		if id == fieldID {
			return true
		}
	}
	return false
}

func ensureTagState(m map[string]*kit.TagState, key string) *kit.TagState {
	if t, ok := m[key]; ok {
		return t
	}
	t := kit.NewTagState()
	m[key] = t
	return t
}

func (v *settingsView) syncFromModel(m *settings.Model) {
	v.shellAllowField.SetText(settings.FormatShellAllow(m.Draft.ShellAllow))
	v.envTagsField.SetText(settings.FormatEnvTags(m.Draft.EnvTags))
	v.envTagsErr = ""
	for _, spec := range teamIntFieldSpecs() {
		w := v.ensureIntField(spec.id)
		w.field.SetText(strconv.Itoa(spec.get(&m.Draft)))
		w.err = ""
	}
}
