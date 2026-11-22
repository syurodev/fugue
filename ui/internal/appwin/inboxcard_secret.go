package appwin

import (
	"fmt"
	"strings"

	"gioui.org/layout"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func (qf *questionFormState) clearSecret() {
	if qf.secretInput != nil {
		qf.secretInput.SetText("")
		qf.secretInput = kit.NewTextFieldState()
	}
}

func (f *cardFormState) clearSecrets() {
	for _, qf := range f.questions {
		qf.clearSecret()
	}
}

func canSubmitSecretQuestion(qf *questionFormState) bool {
	if qf.secretMode == 1 {
		return qf.selectedSecID != ""
	}
	return qf.secretInput.Text() != "" && (!qf.saveSecret || strings.TrimSpace(qf.saveName.Text()) != "")
}

func layoutSecretQuestion(env *kit.Env, sc *screens, qf *questionFormState, busy bool) []layout.FlexChild {
	wsID, wsName := sc.currentWorkspace()
	available := sc.secrets.AllForScope(wsID)
	out := make([]layout.FlexChild, 0, 10)
	out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layoutSecretMode(gtx, env, sc, qf, busy, len(available) > 0)
	}))
	if qf.secretMode == 1 {
		return append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutSecretPicker(gtx, env, sc, qf, busy, available, wsName)
		}))
	}
	out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layoutSecretValue(gtx, env, sc, qf, busy)
	}), spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		return layoutSaveSecretCheck(gtx, env, sc, qf, busy)
	}))
	if qf.saveSecret {
		return append(out, secretSaveFields(env, sc, qf, busy, wsID, wsName)...)
	}
	return out
}

func layoutSecretMode(gtx *layout.Context, env *kit.Env, sc *screens, qf *questionFormState, busy, hasSaved bool) layout.Dimensions {
	props := kit.SegmentedProps{
		Options: []kit.SegmentedOption{
			{ID: "enter", Label: "Enter value"},
			{ID: "pick", Label: "From Secret Store", Disabled: !hasSaved},
		},
		Selected: qf.secretMode,
		Disabled: busy,
	}
	changed, next, err := qf.modeSeg.Layout(env, gtx, props)
	sc.fail(err)
	if changed {
		qf.secretMode = next
	}
	props.Selected = qf.secretMode
	return qf.modeSeg.Measure(env, gtx, props)
}

func layoutSecretPicker(gtx *layout.Context, env *kit.Env, sc *screens, qf *questionFormState, busy bool, available []client.SecretMeta, wsName string) layout.Dimensions {
	opts := make([]kit.SelectOption, 0, len(available))
	for i := range available {
		m := &available[i]
		scopeLabel := labelGlobal
		if m.ScopeKind == client.SecretScopeWorkspace {
			scopeLabel = workspaceScopeLabel(wsName)
		}
		opts = append(opts, kit.SelectOption{ID: m.SecretID, Label: fmt.Sprintf("%s (%s)", m.Name, scopeLabel)})
	}
	helper := "Host decrypts and relays the selected secret directly without exposing plaintext."
	if len(opts) == 0 {
		helper = "No saved secrets available in the Secret Store."
	}
	props := &kit.SelectProps{
		Label:       "Saved secret",
		Placeholder: "Choose a secret…",
		Helper:      helper,
		Options:     opts,
		Value:       qf.selectedSecID,
		Disabled:    busy || len(opts) == 0,
	}
	res, err := qf.secretPicker.Layout(env, gtx, props)
	sc.fail(err)
	if res.Changed {
		qf.selectedSecID = res.Value
	}
	return qf.secretPicker.Measure(env, gtx, props)
}

func layoutSecretValue(gtx *layout.Context, env *kit.Env, sc *screens, qf *questionFormState, busy bool) layout.Dimensions {
	props := &kit.TextFieldProps{
		Label:       labelSecretValue,
		Placeholder: placeholderSecretValue,
		Helper:      "Masked input; zeroed from memory immediately after sending.",
		Variant:     kit.TextFieldSecret,
		NoReveal:    true,
		Disabled:    busy,
	}
	_, err := qf.secretInput.Layout(env, gtx, props)
	sc.fail(err)
	return qf.secretInput.Measure(env, gtx, props)
}

func layoutSaveSecretCheck(gtx *layout.Context, env *kit.Env, sc *screens, qf *questionFormState, busy bool) layout.Dimensions {
	val := kit.CheckOff
	if qf.saveSecret {
		val = kit.CheckOn
	}
	props := kit.CheckboxProps{Label: "Save to Secret Store", Value: val, Disabled: busy || sc.vault.Locked()}
	changed, _, err := qf.saveCheck.Layout(env, gtx, props)
	sc.fail(err)
	if changed {
		qf.saveSecret = !qf.saveSecret
	}
	return qf.saveCheck.Measure(env, gtx, props)
}

func secretSaveFields(env *kit.Env, sc *screens, qf *questionFormState, busy bool, wsID, wsName string) []layout.FlexChild {
	return []layout.FlexChild{
		spacerH(theme.SpaceSM),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.TextFieldProps{Label: "Secret name", Placeholder: "API_TOKEN", Disabled: busy}
			_, err := qf.saveName.Layout(env, gtx, props)
			sc.fail(err)
			return qf.saveName.Measure(env, gtx, props)
		}),
		spacerH(theme.SpaceSM),
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layoutSaveSecretScope(gtx, env, sc, qf, busy, wsID, wsName)
		}),
	}
}

func layoutSaveSecretScope(gtx *layout.Context, env *kit.Env, sc *screens, qf *questionFormState, busy bool, wsID, wsName string) layout.Dimensions {
	props := kit.SegmentedProps{
		Options: []kit.SegmentedOption{
			{ID: client.SecretScopeGlobal, Label: labelGlobal},
			{ID: client.SecretScopeWorkspace, Label: workspaceScopeLabel(wsName), Disabled: wsID == ""},
		},
		Selected: qf.saveScopeIdx,
		Disabled: busy,
	}
	changed, next, err := qf.saveScopeSeg.Layout(env, gtx, props)
	sc.fail(err)
	if changed {
		qf.saveScopeIdx = next
	}
	props.Selected = qf.saveScopeIdx
	return qf.saveScopeSeg.Measure(env, gtx, props)
}

func buildSecretQuestionAnswer(questionID string, qf *questionFormState, wsID string) client.InteractionQuestionAnswer {
	if qf.secretMode == 1 {
		return client.InteractionQuestionAnswer{
			QuestionID: questionID,
			SecretID:   qf.selectedSecID,
		}
	}
	val := []byte(qf.secretInput.Text())
	qf.clearSecret()
	ans := client.InteractionQuestionAnswer{
		QuestionID:  questionID,
		SecretValue: val,
	}
	if qf.saveSecret {
		ans.SaveSecretName = strings.TrimSpace(qf.saveName.Text())
		ans.SaveScopeKind = client.SecretScopeGlobal
		if qf.saveScopeIdx == 1 && wsID != "" {
			ans.SaveScopeKind = client.SecretScopeWorkspace
			ans.SaveScopeID = wsID
		}
	}
	return ans
}
