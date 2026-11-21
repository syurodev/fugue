package appwin

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type intFieldWidgets struct {
	field *kit.TextFieldState
	err   string
}

// settingsView owns the F10.5d1 settings pane: app preferences (theme mode and
// UI scale persisted via fugue.appstate.v1) and structured workspace settings
// forms with provenance badges, CAS conflict retention, and explicit policy
// confirmation. Until F10.20b's slot grid lands, it occupies the right pane.
type settingsView struct {
	refreshBtn       *kit.IconButtonState
	backBtn          *kit.ButtonState
	saveBtn          *kit.ButtonState
	discardBtn       *kit.ButtonState
	confirmPolicyBtn *kit.ButtonState
	cancelPolicyBtn  *kit.ButtonState

	prefsCard  *kit.CardState
	wsCard     *kit.CardState
	policyCard *kit.CardState
	scroll     *kit.ScrollAreaState
	empty      *kit.EmptyStateState

	errBanner      *kit.BannerState
	conflictBanner *kit.BannerState
	pendingBanner  *kit.BannerState
	policyBanner   *kit.BannerState

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
		refreshBtn:        kit.NewIconButtonState(),
		backBtn:           kit.NewButtonState(),
		saveBtn:           kit.NewButtonState(),
		discardBtn:        kit.NewButtonState(),
		confirmPolicyBtn:  kit.NewButtonState(),
		cancelPolicyBtn:   kit.NewButtonState(),
		prefsCard:         kit.NewCardState(),
		wsCard:            kit.NewCardState(),
		policyCard:        kit.NewCardState(),
		scroll:            kit.NewScrollAreaState(),
		empty:             kit.NewEmptyStateState(),
		errBanner:         kit.NewBannerState(),
		conflictBanner:    kit.NewBannerState(),
		pendingBanner:     kit.NewBannerState(),
		policyBanner:      kit.NewBannerState(),
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
	return layoutRightPane(gtx, env,
		func(gtx *layout.Context) layout.Dimensions { return v.layoutHeader(gtx, env, sc) },
		func(gtx *layout.Context) layout.Dimensions { return v.layoutBody(gtx, env, sc) },
	)
}

func (v *settingsView) layoutHeader(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		rigidLabel(env, theme.TypeHeading(), env.Th.Chrome().Fg, "Settings"),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
		}),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.IconButtonProps{
				Icon:     kit.IconRestart,
				Label:    "Refresh settings",
				Variant:  kit.IconButtonGhost,
				Size:     kit.SizeSM,
				Disabled: sc.settingsWorker == nil || sc.settings.Loading,
			}
			clicked, err := v.refreshBtn.Layout(env, gtx, props)
			sc.fail(err)
			if clicked {
				sc.requestReloadSettings()
			}
			return v.refreshBtn.Measure(env, gtx, props)
		}),
		spacerW(theme.SpaceSM),
		rigidInboxButton(env, sc.fail, v.backBtn, kit.ButtonProps{
			Text: labelBackToChat, Variant: kit.Ghost, Size: kit.SizeSM, Disabled: sc.open == "",
		}, func() { sc.showPane(paneTranscript) }),
	)
}

func (v *settingsView) layoutBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	wsChildren := v.workspaceSectionChildren(env, sc)
	children := make([]layout.FlexChild, 0, 2+len(wsChildren))
	children = append(children,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutPreferencesCard(gtx, env, sc) }),
		spacerH(theme.SpaceMD),
	)
	children = append(children, wsChildren...)
	return v.scroll.Layout(env, gtx, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
	})
}

func (v *settingsView) layoutPreferencesCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	cur := sc.settings.Prefs.Current
	dims, _, err := v.prefsCard.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		children := make([]layout.FlexChild, 0, 13)
		children = append(children,
			rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "App preferences"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Saved automatically on this device."),
			spacerH(theme.SpaceMD),
			rigidLabel(env, theme.TypeLabel(), env.Th.Chrome().Fg, "Theme mode"),
			spacerH(theme.SpaceXS),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutThemeModeControl(gtx, env, sc, cur) }),
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
	children := make([]layout.FlexChild, 0, 7+len(banners))
	children = append(children,
		v.workspaceSelectChild(env, sc, opts),
		spacerH(theme.SpaceMD),
	)
	children = append(children, banners...)
	children = append(children,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutTeamsCard(gtx, env, sc) }),
		spacerH(theme.SpaceMD),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutPolicyCard(gtx, env, sc) }),
		spacerH(theme.SpaceMD),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions { return v.layoutSaveBar(gtx, env, sc) }),
	)
	return children
}

func (v *settingsView) workspaceSelectChild(env *kit.Env, sc *screens, opts []kit.SelectOption) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		props := &kit.SelectProps{
			Label:       "Workspace",
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
	if sc.settings.PendingPolicyConfirmation {
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.pendingBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerWarning,
				Title:   "Out-of-band policy change waiting",
				Message: "Review or reject the external policy edit from the sidebar before saving new settings.",
				Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceMD))
	}
	if len(sc.settings.ChangedUnderneath) > 0 {
		parts := make([]string, 0, len(sc.settings.ChangedUnderneath))
		for _, ch := range sc.settings.ChangedUnderneath {
			parts = append(parts, fmt.Sprintf("%s: %s → %s", ch.Label, ch.OldValue, ch.NewValue))
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := v.conflictBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerWarning,
				Title:   "Reloaded after configuration conflict",
				Message: "Your unsaved edits were kept. Changed underneath: " + strings.Join(parts, "; "),
				Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceMD))
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
