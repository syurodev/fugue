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

func layoutSecretQuestion(env *kit.Env, sc *screens, qf *questionFormState, busy bool) []layout.FlexChild {
	wsID, wsName := sc.currentWorkspace()
	available := sc.secrets.AllForScope(wsID)
	out := make([]layout.FlexChild, 0, 10)

	out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		opts := []kit.SegmentedOption{
			{ID: "enter", Label: "Enter value"},
			{ID: "pick", Label: "From Secret Store", Disabled: len(available) == 0},
		}
		changed, next, err := qf.modeSeg.Layout(env, gtx, kit.SegmentedProps{
			Options:  opts,
			Selected: qf.secretMode,
			Disabled: busy,
		})
		sc.fail(err)
		if changed {
			qf.secretMode = next
		}
		return qf.modeSeg.Measure(env, gtx, kit.SegmentedProps{Options: opts, Selected: qf.secretMode})
	}))

	if qf.secretMode == 1 {
		out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			opts := make([]kit.SelectOption, 0, len(available))
			for i := range available {
				m := available[i]
				scopeLabel := "Global"
				if m.ScopeKind == client.SecretScopeWorkspace {
					scopeLabel = "Workspace"
					if wsName != "" {
						scopeLabel = wsName
					}
				}
				opts = append(opts, kit.SelectOption{
					ID:    m.SecretID,
					Label: fmt.Sprintf("%s (%s)", m.Name, scopeLabel),
				})
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
		}))
		return out
	}

	out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		props := &kit.TextFieldProps{
			Label:       "Secret value",
			Placeholder: "Enter secret value…",
			Helper:      "Masked input; zeroed from memory immediately after sending.",
			Variant:     kit.TextFieldSecret,
			NoReveal:    true,
			Disabled:    busy,
		}
		_, err := qf.secretInput.Layout(env, gtx, props)
		sc.fail(err)
		return qf.secretInput.Measure(env, gtx, props)
	}))

	out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		val := kit.CheckOff
		if qf.saveSecret {
			val = kit.CheckOn
		}
		pr := kit.CheckboxProps{
			Label:    "Save to Secret Store",
			Value:    val,
			Disabled: busy || sc.vault.Locked(),
		}
		changed, _, err := qf.saveCheck.Layout(env, gtx, pr)
		sc.fail(err)
		if changed {
			qf.saveSecret = !qf.saveSecret
		}
		return qf.saveCheck.Measure(env, gtx, pr)
	}))

	if qf.saveSecret {
		wsLabel := "Workspace"
		if wsName != "" {
			wsLabel = wsName
		}
		out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			props := &kit.TextFieldProps{
				Label:       "Secret name",
				Placeholder: "API_TOKEN",
				Disabled:    busy,
			}
			_, err := qf.saveName.Layout(env, gtx, props)
			sc.fail(err)
			return qf.saveName.Measure(env, gtx, props)
		}))
		out = append(out, spacerH(theme.SpaceSM), rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			opts := []kit.SegmentedOption{
				{ID: client.SecretScopeGlobal, Label: "Global"},
				{ID: client.SecretScopeWorkspace, Label: wsLabel, Disabled: wsID == ""},
			}
			changed, next, err := qf.saveScopeSeg.Layout(env, gtx, kit.SegmentedProps{
				Options:  opts,
				Selected: qf.saveScopeIdx,
				Disabled: busy,
			})
			sc.fail(err)
			if changed {
				qf.saveScopeIdx = next
			}
			return qf.saveScopeSeg.Measure(env, gtx, kit.SegmentedProps{Options: opts, Selected: qf.saveScopeIdx})
		}))
	}
	return out
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
