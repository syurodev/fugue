package appwin

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	tabForms = "forms"
	tabRaw   = "raw"
)

type intFieldWidgets struct {
	field *kit.TextFieldState
	err   string
}

// settingsView owns the F10.5d1/F10.5d2 settings pane: app preferences,
// structured workspace settings forms, and the Q-62 raw TOML editor tab with
// live validation, CAS conflict diffing, and Q-64 policy confirmation routing.
// It renders inside the Settings slot.
type settingsView struct {
	saveBtn           *kit.ButtonState
	discardBtn        *kit.ButtonState
	confirmPolicyBtn  *kit.ButtonState
	cancelPolicyBtn   *kit.ButtonState
	pendingConfirmBtn *kit.ButtonState
	pendingRejectBtn  *kit.ButtonState
	toggleDiffBtn     *kit.ButtonState

	tabs              *kit.TabsState
	activeTab         string
	pendingTabSwitch  string
	prefsCard         *kit.CardState
	wsCard            *kit.CardState
	policyCard        *kit.CardState
	pendingPolicyCard *kit.CardState
	scroll            *kit.ScrollAreaState
	empty             *kit.EmptyStateState

	errBanner       *kit.BannerState
	conflictBanner  *kit.BannerState
	configErrBanner *kit.BannerState
	policyBanner    *kit.BannerState
	tabSwitchBanner *kit.BannerState

	rawView *rawTOMLView

	themeSeg *kit.SegmentedState
	scaleSeg *kit.SegmentedState
	wsSelect *kit.SelectState

	approvalSelect    *kit.SelectState
	sandboxSelect     *kit.SelectState
	shellAllowField   *kit.TextFieldState
	envTagsField      *kit.TextFieldState
	envTagsErr        string
	teamsSwitch       *kit.SwitchState
	autoApproveSwitch *kit.SwitchState

	intFields  map[string]*intFieldWidgets
	tierTags   map[string]*kit.TagState
	policyTags map[string]*kit.TagState
	dirtyTags  map[string]*kit.TagState
}

func newSettingsView() *settingsView {
	return &settingsView{
		saveBtn:           kit.NewButtonState(),
		discardBtn:        kit.NewButtonState(),
		confirmPolicyBtn:  kit.NewButtonState(),
		cancelPolicyBtn:   kit.NewButtonState(),
		pendingConfirmBtn: kit.NewButtonState(),
		pendingRejectBtn:  kit.NewButtonState(),
		toggleDiffBtn:     kit.NewButtonState(),
		tabs:              kit.NewTabsState(),
		activeTab:         tabForms,
		prefsCard:         kit.NewCardState(),
		wsCard:            kit.NewCardState(),
		policyCard:        kit.NewCardState(),
		pendingPolicyCard: kit.NewCardState(),
		scroll:            kit.NewScrollAreaState(),
		empty:             kit.NewEmptyStateState(),
		errBanner:         kit.NewBannerState(),
		conflictBanner:    kit.NewBannerState(),
		configErrBanner:   kit.NewBannerState(),
		policyBanner:      kit.NewBannerState(),
		tabSwitchBanner:   kit.NewBannerState(),
		rawView:           newRawTOMLView(),
		themeSeg:          kit.NewSegmentedState(),
		scaleSeg:          kit.NewSegmentedState(),
		wsSelect:          kit.NewSelectState(),
		approvalSelect:    kit.NewSelectState(),
		sandboxSelect:     kit.NewSelectState(),
		shellAllowField:   kit.NewTextFieldState(),
		envTagsField:      kit.NewTextFieldState(),
		teamsSwitch:       kit.NewSwitchState(),
		autoApproveSwitch: kit.NewSwitchState(),
		intFields:         map[string]*intFieldWidgets{},
		tierTags:          map[string]*kit.TagState{},
		policyTags:        map[string]*kit.TagState{},
		dirtyTags:         map[string]*kit.TagState{},
	}
}

func (v *settingsView) ensureIntField(id string) *intFieldWidgets {
	if w, ok := v.intFields[id]; ok {
		return w
	}
	w := &intFieldWidgets{field: kit.NewTextFieldState()}
	v.intFields[id] = w
	return w
}

func (v *settingsView) hasFieldValidationError() bool {
	if v.envTagsErr != "" {
		return true
	}
	for _, w := range v.intFields {
		if w.err != "" {
			return true
		}
	}
	return false
}

func (v *settingsView) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return layoutSlotContent(gtx, env, func(gtx *layout.Context) layout.Dimensions {
		return v.layoutBody(gtx, env, sc)
	})
}

func (v *settingsView) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	wsChildren := v.workspaceSectionChildren(env, sc)
	children := make([]layout.FlexChild, 0, 2+len(wsChildren))
	children = append(children,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutPreferencesCard(gtx, env, sc) }),
		spacerH(theme.SpaceXXL),
	)
	children = append(children, wsChildren...)
	return v.scroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		colW := min(gtx.Constraints.Max.X, gtx.Dp(theme.TranscriptMeasure))
		local := *gtx
		local.Constraints.Min.X = colW
		local.Constraints.Max.X = colW
		return layout.Flex{Axis: layout.Vertical}.Layout(local, children...)
	})
}

func (v *settingsView) layoutPreferencesCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	cur := sc.settings.Prefs.Current
	local := *gtx
	local.Constraints.Min.X = local.Constraints.Max.X
	dims, _, err := v.prefsCard.Layout(env, &local, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 15)
		children = append(children,
			rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "App preferences"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Saved automatically on this device."),
			spacerH(theme.SpaceMD),
			rigidLabel(env, theme.TypeLabel(), env.Th.Chrome().Fg, "Theme mode"),
			spacerH(theme.SpaceXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutThemeModeControl(gtx, env, sc, cur) }),
		)
		if settings.NormalizeThemeMode(cur.ThemeMode) == settings.ThemeSystem && sc.systemAppearance() == appearanceUnknown {
			children = append(children,
				spacerH(theme.SpaceXS),
				rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "System appearance could not be read; using Light."),
			)
		}
		children = append(children,
			spacerH(theme.SpaceMD),
			rigidLabel(env, theme.TypeLabel(), env.Th.Chrome().Fg, "UI scale"),
			spacerH(theme.SpaceXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutUIScaleControl(gtx, env, sc, cur) }),
		)
		if sc.settings.Prefs.Err != "" {
			children = append(children, spacerH(theme.SpaceSM), rigidLabel(env, theme.TypeSmall(), env.Th.Status().Danger, sc.settings.Prefs.Err))
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, children...)
	})
	sc.fail(err)
	return dims
}

func (v *settingsView) layoutThemeModeControl(gtx *layout.Context, env *kit.Env, sc *screens, cur settings.Preferences) layout.Dimensions {
	themeOpts := []kit.SegmentedOption{
		{ID: string(settings.ThemeLight), Label: "Light"},
		{ID: string(settings.ThemeDark), Label: "Dark"},
		{ID: string(settings.ThemeSystem), Label: "System"},
	}
	themeIdx := 2
	switch cur.ThemeMode {
	case settings.ThemeLight:
		themeIdx = 0
	case settings.ThemeDark:
		themeIdx = 1
	case settings.ThemeSystem:
		themeIdx = 2
	}
	pr := kit.SegmentedProps{Options: themeOpts, Selected: themeIdx}
	changed, next, segErr := v.themeSeg.Layout(env, gtx, pr)
	sc.fail(segErr)
	if changed && next >= 0 && next < len(themeOpts) {
		updated := cur
		updated.ThemeMode = settings.ThemeMode(themeOpts[next].ID)
		sc.updatePreferences(updated)
	}
	return v.themeSeg.Measure(env, gtx, pr)
}

func (v *settingsView) layoutUIScaleControl(gtx *layout.Context, env *kit.Env, sc *screens, cur settings.Preferences) layout.Dimensions {
	scaleSteps := []float32{settings.Scale090, settings.Scale100, settings.Scale110, settings.Scale125}
	scaleOpts := []kit.SegmentedOption{
		{ID: "0.9", Label: "90%"},
		{ID: "1.0", Label: "100%"},
		{ID: "1.1", Label: "110%"},
		{ID: "1.25", Label: "125%"},
	}
	scaleIdx := 1
	for i, step := range scaleSteps {
		if settings.NormalizeUIScale(cur.UIScale) == step {
			scaleIdx = i
			break
		}
	}
	pr := kit.SegmentedProps{Options: scaleOpts, Selected: scaleIdx}
	changed, next, segErr := v.scaleSeg.Layout(env, gtx, pr)
	sc.fail(segErr)
	if changed && next >= 0 && next < len(scaleSteps) {
		updated := cur
		updated.UIScale = scaleSteps[next]
		sc.updatePreferences(updated)
	}
	return v.scaleSeg.Measure(env, gtx, pr)
}

func (v *settingsView) workspaceSectionChildren(env *kit.Env, sc *screens) []layout.FlexChild {
	list := sc.workspaces.List()
	if len(list) == 0 {
		return []layout.FlexChild{rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.empty.Layout(env, gtx, &kit.EmptyStateProps{
				Icon:        kit.IconHelp,
				Title:       "No workspaces",
				Description: "Create a workspace in the sidebar to edit its settings.",
			})
			sc.fail(err)
			return dims
		})}
	}
	opts := make([]kit.SelectOption, 0, len(list))
	for _, ws := range list {
		opts = append(opts, kit.SelectOption{ID: ws.ID, Label: ws.Name})
	}
	banners := v.workspaceBannerChildren(env, sc)
	children := make([]layout.FlexChild, 0, 10+len(banners))
	children = append(children,
		v.workspaceSelectChild(env, sc, opts),
		spacerH(theme.SpaceMD),
	)
	children = append(children, banners...)
	children = append(children, v.workspaceTabsChild(env, sc), spacerH(theme.SpaceMD))
	if v.pendingTabSwitch != "" {
		children = append(children, v.tabSwitchBannerChild(env, sc), spacerH(theme.SpaceMD))
	}
	if v.activeTab == tabRaw {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.rawView.layout(gtx, env, sc)
		}))
		return children
	}
	children = append(children,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutTeamsCard(gtx, env, sc) }),
		spacerH(theme.SpaceXXL),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutPolicyCard(gtx, env, sc) }),
		spacerH(theme.SpaceXXL),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutSaveBar(gtx, env, sc) }),
	)
	return children
}

func (v *settingsView) currentTabDirty(m *settings.Model) bool {
	if v.activeTab == tabRaw {
		return m.RawDirty()
	}
	return len(m.DirtyFields()) > 0
}

func (v *settingsView) workspaceTabsChild(env *kit.Env, sc *screens) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		if v.activeTab == "" {
			v.activeTab = tabForms
		}
		pr := kit.TabsProps{
			Variant: kit.TabsMode,
			Items: []kit.TabItem{
				{ID: tabForms, Label: "Forms", Dirty: len(sc.settings.DirtyFields()) > 0},
				{ID: tabRaw, Label: "Raw TOML", Dirty: sc.settings.RawDirty()},
			},
			ActiveID: v.activeTab,
		}
		dims, ev := v.tabs.Layout(env, gtx, pr)
		sc.fail(ev.Err)
		if ev.Selected != "" && ev.Selected != v.activeTab {
			if v.currentTabDirty(sc.settings) {
				v.pendingTabSwitch = ev.Selected
			} else {
				v.activeTab = ev.Selected
				v.pendingTabSwitch = ""
			}
		}
		return dims
	})
}

func (v *settingsView) tabSwitchBannerChild(env *kit.Env, sc *screens) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		dims, ev, err := v.tabSwitchBanner.Layout(env, gtx, kit.BannerProps{
			Kind:        kit.BannerWarning,
			Title:       "Unsaved changes",
			Message:     "Discard unsaved edits on this tab before switching?",
			ActionLabel: "Switch & discard",
			Dismissible: true,
			Visible:     v.pendingTabSwitch != "",
		})
		sc.fail(err)
		if ev.ActionClicked && v.pendingTabSwitch != "" {
			if v.activeTab == tabRaw {
				sc.reloadRawFromHost()
			} else {
				sc.settings.DiscardDraft()
				v.syncFromModel(sc.settings)
			}
			v.activeTab = v.pendingTabSwitch
			v.pendingTabSwitch = ""
		} else if ev.Dismissed {
			v.pendingTabSwitch = ""
		}
		return dims
	})
}

func (v *settingsView) workspaceSelectChild(env *kit.Env, sc *screens, opts []kit.SelectOption) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		props := &kit.SelectProps{
			Label:       labelWorkspace,
			Placeholder: "Choose a workspace",
			Options:     opts,
			Value:       sc.settings.WorkspaceID,
		}
		res, err := v.wsSelect.Layout(env, gtx, props)
		sc.fail(err)
		if res.Changed && res.Value != "" {
			name := res.Value
			if ws, ok := sc.workspaces.Lookup(res.Value); ok {
				name = ws.Name
			}
			sc.settings.SelectWorkspace(res.Value, name)
			sc.ensureSettingsWorkspaceSelected()
		}
		return v.wsSelect.Measure(env, gtx, props)
	})
}

func (v *settingsView) workspaceBannerChildren(env *kit.Env, sc *screens) []layout.FlexChild {
	var children []layout.FlexChild
	if sc.settings.ConfigErrorKey != "" || sc.settings.ConfigErrorLine > 0 {
		msg := fmt.Sprintf("Line %d:%d (%s) — host is keeping the last accepted policy.",
			max(sc.settings.ConfigErrorLine, 1), max(sc.settings.ConfigErrorColumn, 1), sc.settings.ConfigErrorKey)
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.configErrBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerDanger,
				Title:   "Workspace configuration file has an error",
				Message: msg,
				Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceMD))
	}
	if sc.settings.PendingPolicyConfirmation {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return v.layoutPendingPolicyCard(gtx, env, sc)
		}), spacerH(theme.SpaceMD))
	}
	if sc.settings.RawConflict || len(sc.settings.ChangedUnderneath) > 0 {
		children = append(children, v.layoutConflictBannerChild(env, sc), spacerH(theme.SpaceMD))
	}
	if errMsg := firstNonEmpty(sc.settings.SaveErr, sc.settings.LoadErr); errMsg != "" {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.errBanner.Layout(env, gtx, kit.BannerProps{
				Kind: kit.BannerDanger, Title: "Workspace settings error", Message: errMsg, Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceMD))
	}
	return children
}

func (v *settingsView) layoutSaveBar(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	dirty := sc.settings.DirtyFields()
	dirtyPolicy := sc.settings.DirtyPolicyFields()
	var children []layout.FlexChild
	if sc.settings.PolicyConfirmArmed && len(dirtyPolicy) > 0 {
		labels := make([]string, 0, len(dirtyPolicy))
		for _, id := range dirtyPolicy {
			labels = append(labels, settings.FieldLabel(id))
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.policyBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerWarning,
				Title:   "Confirm policy changes",
				Message: "Saving updates workspace policy keys (" + strings.Join(labels, ", ") + ") and bumps policy_revision.",
				Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceSM))
	}
	canSave := sc.settingsWorker != nil && sc.settings.Loaded && !sc.settings.Saving && len(dirty) > 0 && !v.hasFieldValidationError()
	buttons := []layout.FlexChild{}
	if sc.settings.PolicyConfirmArmed && len(dirtyPolicy) > 0 {
		buttons = append(buttons,
			rigidInboxButton(env, sc.fail, v.confirmPolicyBtn, kit.ButtonProps{
				Text: "Confirm & save policy", Variant: kit.Primary, Size: kit.SizeMD, Busy: sc.settings.Saving, Disabled: !canSave,
			}, func() { sc.submitWorkspaceSettings(true) }),
			spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, v.cancelPolicyBtn, kit.ButtonProps{
				Text: labelCancel, Variant: kit.Secondary, Size: kit.SizeMD, Disabled: sc.settings.Saving,
			}, func() { sc.settings.PolicyConfirmArmed = false }),
		)
	} else {
		buttons = append(buttons,
			rigidInboxButton(env, sc.fail, v.saveBtn, kit.ButtonProps{
				Text: "Save changes", Variant: kit.Primary, Size: kit.SizeMD, Busy: sc.settings.Saving, Disabled: !canSave,
			}, func() { sc.submitWorkspaceSettings(false) }),
		)
	}
	buttons = append(buttons,
		spacerW(theme.SpaceSM),
		rigidInboxButton(env, sc.fail, v.discardBtn, kit.ButtonProps{
			Text: "Discard", Variant: kit.Ghost, Size: kit.SizeMD, Disabled: len(dirty) == 0 || sc.settings.Saving,
		}, func() {
			sc.settings.DiscardDraft()
			v.syncFromModel(sc.settings)
		}),
	)
	children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, buttons...)
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
