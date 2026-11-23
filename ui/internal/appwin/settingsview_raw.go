package appwin

import (
	"fmt"
	"strings"

	"gioui.org/io/key"
	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

type rawTOMLView struct {
	card             *kit.CardState
	editor           *kit.TextAreaState
	diffBlock        *mdview.CodeBlockState
	policyBanner     *kit.BannerState
	saveBtn          *kit.ButtonState
	discardBtn       *kit.ButtonState
	confirmPolicyBtn *kit.ButtonState
	cancelPolicyBtn  *kit.ButtonState
	issueBtns        []*kit.ButtonState
}

func newRawTOMLView() *rawTOMLView {
	return &rawTOMLView{
		card:             kit.NewCardState(),
		editor:           kit.NewTextAreaState(),
		diffBlock:        mdview.NewCodeBlockState(),
		policyBanner:     kit.NewBannerState(),
		saveBtn:          kit.NewButtonState(),
		discardBtn:       kit.NewButtonState(),
		confirmPolicyBtn: kit.NewButtonState(),
		cancelPolicyBtn:  kit.NewButtonState(),
	}
}

func (r *rawTOMLView) syncFromModel(m *settings.Model) {
	if m == nil {
		return
	}
	if r.editor.Text() != m.RawDraft {
		r.editor.SetText(m.RawDraft)
	}
}

func (r *rawTOMLView) handleSaveShortcut(gtx *layout.Context, sc *screens) {
	for _, filter := range []key.Filter{
		{Focus: r.editor.Tag(), Required: key.ModShortcut, Name: key.Name("S")},
		{Required: key.ModShortcut, Name: key.Name("S")},
	} {
		for {
			ke, ok := gtx.Event(filter)
			if !ok {
				break
			}
			if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
				sc.submitRawTOML(false)
			}
		}
	}
}

func (r *rawTOMLView) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	r.handleSaveShortcut(gtx, sc)
	cardDims := r.layoutEditorCard(gtx, env, sc)
	children := make([]layout.FlexChild, 0, 5)
	children = append(children, rigidWidget(func(*layout.Context) layout.Dimensions { return cardDims }))
	if sc.settings.ShowConflictDiff {
		diffRaw := settings.BuildConflictUnifiedDiff(string(sc.settings.RawTOML), sc.settings.RawDraft)
		children = append(children, spacerH(theme.SpaceMD), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _ := mdview.LayoutCodeBlock(env, gtx, r.diffBlock, &mdview.CodeBlockProps{
				Title:           "fugue.toml (host vs yours)",
				Format:          diffLabel,
				IsDiff:          true,
				ShowLineNumbers: true,
				Raw:             diffRaw,
			})
			return dims
		}))
	}
	children = append(children, spacerH(theme.SpaceMD), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return r.layoutSaveBar(gtx, env, sc)
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func (r *rawTOMLView) layoutEditorCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	hashLabel := "Workspace fugue.toml"
	if sc.settings.ConfigHash != "" {
		hashLabel = "Workspace fugue.toml · hash " + sc.settings.ConfigHash
	}
	cardDims, _, cardErr := r.card.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		inner := make([]layout.FlexChild, 0, 8)
		inner = append(inner,
			rigidLabel(env, theme.TypeTitle(), env.Th.Chrome().Fg, "Raw TOML"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, hashLabel),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				props := &kit.TextAreaProps{
					Placeholder: "# Workspace fugue.toml",
					Monospace:   true,
					MaxLines:    16,
					ErrorLines:  sc.settings.IssueLines(),
					Disabled:    !sc.settings.Loaded || sc.settings.Saving,
				}
				ev := r.editor.Layout(env, gtx, props)
				if ev.Changed {
					sc.onRawTOMLChanged(r.editor.Text())
				}
				return r.editor.Measure(env, gtx, props)
			}),
		)
		if sc.settings.Validating {
			inner = append(inner, spacerH(theme.SpaceXS), rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted, "Validating..."))
		}
		if sc.settings.ValidationErr != "" {
			inner = append(inner, spacerH(theme.SpaceXS), rigidLabel(env, theme.TypeSmall(), env.Th.Status().Danger, sc.settings.ValidationErr))
		}
		if len(sc.settings.ValidationIssues) > 0 {
			inner = append(inner, spacerH(theme.SpaceSM))
			inner = append(inner, r.issueListChildren(&gtx, env, sc)...)
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, inner...)
	})
	sc.fail(cardErr)
	return cardDims
}

func (r *rawTOMLView) issueListChildren(gtx *layout.Context, env *kit.Env, sc *screens) []layout.FlexChild {
	issues := sc.settings.ValidationIssues
	for len(r.issueBtns) < len(issues) {
		r.issueBtns = append(r.issueBtns, kit.NewButtonState())
	}
	children := make([]layout.FlexChild, 0, len(issues)*2)
	for i := range issues {
		iss := issues[i]
		btn := r.issueBtns[i]
		label := formatValidationIssue(iss)
		if i > 0 {
			children = append(children, spacerH(theme.SpaceXXS))
		}
		children = append(children, rigidInboxButton(env, sc.fail, btn, kit.ButtonProps{
			Text:    label,
			Variant: kit.Ghost,
			Size:    kit.SizeSM,
		}, func() {
			pos := settings.LineColToRuneOffset(sc.settings.RawDraft, iss.Line, iss.Column)
			r.editor.SetCaret(pos, pos)
			gtx.Execute(key.FocusCmd{Tag: r.editor.Tag()})
			r.editor.ScrollToCaret()
		}))
	}
	return children
}

func (r *rawTOMLView) layoutSaveBar(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	dirty := sc.settings.RawDirty()
	diffs := sc.settings.PendingRawPolicyDiff
	var children []layout.FlexChild
	if sc.settings.PolicyConfirmArmed && len(diffs) > 0 {
		lines := make([]string, 0, len(diffs))
		for _, d := range diffs {
			lines = append(lines, fmt.Sprintf("%s: %s → %s", d.Key, d.OldValue, d.NewValue))
		}
		children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			dims, _, err := r.policyBanner.Layout(env, gtx, kit.BannerProps{
				Kind:    kit.BannerWarning,
				Title:   "Confirm policy changes",
				Message: "Saving updates policy keys and bumps policy_revision: " + strings.Join(lines, "; "),
				Visible: true,
			})
			sc.fail(err)
			return dims
		}), spacerH(theme.SpaceSM))
	}

	canSave := sc.settingsWorker != nil && sc.settings.Loaded && !sc.settings.Saving && dirty
	var buttons []kit.FlowItem
	if sc.settings.PolicyConfirmArmed && len(diffs) > 0 {
		buttons = append(buttons,
			flowButton(env, gtx, sc.fail, r.confirmPolicyBtn, kit.ButtonProps{
				Text: "Confirm & save policy", Variant: kit.Primary, Size: kit.SizeMD, Busy: sc.settings.Saving, Disabled: !canSave,
			}, func() { sc.submitRawTOML(true) }),
			flowButton(env, gtx, sc.fail, r.cancelPolicyBtn, kit.ButtonProps{
				Text: labelCancel, Variant: kit.Secondary, Size: kit.SizeMD, Disabled: sc.settings.Saving,
			}, func() {
				sc.settings.PolicyConfirmArmed = false
				sc.settings.PendingRawPolicyDiff = nil
			}),
		)
	} else {
		buttons = append(buttons,
			flowButton(env, gtx, sc.fail, r.saveBtn, kit.ButtonProps{
				Text: "Save raw TOML", Variant: kit.Primary, Size: kit.SizeMD, Busy: sc.settings.Saving, Disabled: !canSave,
			}, func() { sc.submitRawTOML(false) }),
		)
	}
	buttons = append(buttons,
		flowButton(env, gtx, sc.fail, r.discardBtn, kit.ButtonProps{
			Text: "Discard", Variant: kit.Ghost, Size: kit.SizeMD, Disabled: !dirty || sc.settings.Saving,
		}, sc.reloadRawFromHost),
	)
	children = append(children, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return flowHeader(gtx, buttons...)
	}))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func formatValidationIssue(iss client.WorkspaceConfigIssue) string {
	loc := fmt.Sprintf("%d:%d", max(iss.Line, 1), max(iss.Column, 1))
	if iss.Key != "" {
		return fmt.Sprintf("%s · %s: %s", loc, iss.Key, iss.Message)
	}
	return fmt.Sprintf("%s · %s", loc, iss.Message)
}

func (v *settingsView) layoutPendingPolicyCard(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	dims, _, err := v.pendingPolicyCard.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		inner := make([]layout.FlexChild, 0, 6+len(sc.settings.PendingPolicyDiff))
		inner = append(inner,
			rigidLabel(env, theme.TypeTitle(), env.Th.Status().Warning, "Out-of-band policy change waiting"),
			spacerH(theme.SpaceXS),
			rigidLabel(env, theme.TypeSmall(), env.Th.Chrome().FgMuted,
				"fugue.toml changed outside the app. The host keeps the accepted policy until you confirm or reject."),
		)
		if len(sc.settings.PendingPolicyDiff) > 0 {
			diff := make([]workspace.DiffEntry, 0, len(sc.settings.PendingPolicyDiff))
			for _, d := range sc.settings.PendingPolicyDiff {
				diff = append(diff, workspace.DiffEntry{Key: d.Key, OldValue: d.OldValue, NewValue: d.NewValue})
			}
			inner = append(inner, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return layoutDiffPanel(env, gtx, diff)
			}))
		}
		busy := sc.settings.PolicyDecisionBusy
		canAct := sc.settingsWorker != nil && sc.settings.Loaded && !busy
		inner = append(inner, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return flowHeader(gtx,
				flowButton(env, gtx, sc.fail, v.pendingConfirmBtn, kit.ButtonProps{
					Text: "Confirm", Variant: kit.Primary, Size: kit.SizeSM, Busy: busy, Disabled: !canAct,
				}, sc.confirmPendingPolicy),
				flowButton(env, gtx, sc.fail, v.pendingRejectBtn, kit.ButtonProps{
					Text: "Reject", Variant: kit.Secondary, Size: kit.SizeSM, Busy: busy, Disabled: !canAct,
				}, sc.rejectPendingPolicy),
			)
		}))
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx, inner...)
	})
	sc.fail(err)
	return dims
}

func (v *settingsView) layoutConflictBannerChild(env *kit.Env, sc *screens) layout.FlexChild {
	parts := make([]string, 0, len(sc.settings.ChangedUnderneath))
	for _, ch := range sc.settings.ChangedUnderneath {
		parts = append(parts, fmt.Sprintf("%s: %s → %s", ch.Label, ch.OldValue, ch.NewValue))
	}
	msg := "Your unsaved edits were kept."
	if sc.settings.ConflictHash != "" {
		msg += " Current hash: " + sc.settings.ConflictHash + "."
	}
	if len(parts) > 0 {
		msg += " Changed underneath: " + strings.Join(parts, "; ")
	}
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		dims, ev, err := v.conflictBanner.Layout(env, gtx, kit.BannerProps{
			Kind:        kit.BannerWarning,
			Title:       "Reloaded after configuration conflict",
			Message:     msg,
			ActionLabel: "Reload",
			Visible:     true,
		})
		sc.fail(err)
		if ev.ActionClicked {
			sc.reloadRawFromHost()
		}
		if !sc.settings.RawConflict {
			return dims
		}
		diffLabel := "Show diff"
		if sc.settings.ShowConflictDiff {
			diffLabel = "Hide diff"
		}
		return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
			rigidWidget(func(*layout.Context) layout.Dimensions { return dims }),
			spacerH(theme.SpaceXS),
			rigidInboxButton(env, sc.fail, v.toggleDiffBtn, kit.ButtonProps{
				Text: diffLabel, Variant: kit.Secondary, Size: kit.SizeSM,
			}, func() {
				sc.settings.ShowConflictDiff = !sc.settings.ShowConflictDiff
			}),
		)
	})
}
