package appwin

import (
	"image"
	"image/color"
	"slices"

	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/inbox"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const labelDeny = "Deny"

type questionFormState struct {
	questionID     string
	singleSelected string
	multiSelected  map[string]bool
	optionRows     map[string]*kit.ListRowState
	checks         map[string]*kit.CheckboxState
	textInput      *kit.TextAreaState
	secretMode     int
	modeSeg        *kit.SegmentedState
	secretInput    *kit.TextFieldState
	saveSecret     bool
	saveCheck      *kit.CheckboxState
	saveName       *kit.TextFieldState
	saveScopeIdx   int
	saveScopeSeg   *kit.SegmentedState
	secretPicker   *kit.SelectState
	selectedSecID  string
}

type cardFormState struct {
	busy                                           bool
	lastErr, selectedOption                        string
	optionRows                                     map[string]*kit.ListRowState
	denyNote                                       *kit.TextAreaState
	denyBtn, cancelBtn, allowOnceBtn, allowSessBtn *kit.ButtonState
	submitBtn                                      *kit.ButtonState
	kindTag, riskTag, envTag, agentTag             *kit.TagState
	questions                                      []*questionFormState
}

func (v *inboxView) formFor(req *client.PendingInteraction) *cardFormState {
	pin := inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
	if f, ok := v.forms[pin]; ok {
		return f
	}
	qs := make([]*questionFormState, 0, len(req.Questions))
	for i := range req.Questions {
		qs = append(qs, &questionFormState{
			questionID: req.Questions[i].QuestionID, multiSelected: map[string]bool{},
			optionRows: map[string]*kit.ListRowState{}, checks: map[string]*kit.CheckboxState{},
			textInput: kit.NewTextAreaState(), modeSeg: kit.NewSegmentedState(),
			secretInput: kit.NewTextFieldState(), saveCheck: kit.NewCheckboxState(),
			saveName: kit.NewTextFieldState(), saveScopeSeg: kit.NewSegmentedState(),
			secretPicker: kit.NewSelectState(),
		})
	}
	f := &cardFormState{
		optionRows: map[string]*kit.ListRowState{}, denyNote: kit.NewTextAreaState(),
		denyBtn: kit.NewButtonState(), cancelBtn: kit.NewButtonState(),
		allowOnceBtn: kit.NewButtonState(), allowSessBtn: kit.NewButtonState(), submitBtn: kit.NewButtonState(),
		kindTag: kit.NewTagState(), riskTag: kit.NewTagState(), envTag: kit.NewTagState(), agentTag: kit.NewTagState(),
		questions: qs,
	}
	v.forms[pin] = f
	return f
}

func (v *inboxView) layoutActiveCard(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction) layout.Dimensions {
	pin := inbox.Pin{RequestID: req.RequestID, Incarnation: req.Incarnation}
	form := v.formFor(req)
	safeBtn := form.denyBtn
	if req.Kind == client.InteractionKindQuestion {
		safeBtn = form.cancelBtn
	}
	if v.focusedPin != pin {
		v.focusedPin = pin
		gtx.Execute(key.FocusCmd{Tag: safeBtn.Tag()})
	}
	dims, _, err := v.card.Layout(env, gtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return layoutCardHeader(gtx, env, sc, req, form) }),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Divider(env, gtx, kit.DividerProps{Orientation: kit.Horizontal})
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return v.scroll.Layout(env, &local, kit.ScrollAreaProps{Axis: layout.Vertical}, func(gtx *layout.Context) layout.Dimensions {
					return layoutCardScrollBody(gtx, env, sc, req, form)
				})
			}),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				return kit.Divider(env, gtx, kit.DividerProps{Orientation: kit.Horizontal})
			}),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				if form.lastErr == "" {
					return layout.Dimensions{}
				}
				d, _, berr := sc.inboxView.errBanner.Layout(env, gtx, kit.BannerProps{
					Kind: kit.BannerDanger, Title: "Could not submit response", Message: form.lastErr, Visible: true,
				})
				sc.fail(berr)
				return d
			}),
			spacerH(theme.SpaceSM),
			rigidWidget(func(gtx *layout.Context) layout.Dimensions { return layoutCardFooter(gtx, env, sc, req, form, pin) }),
		)
	})
	sc.fail(err)
	return dims
}

func layoutCardHeader(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) layout.Dimensions {
	c := env.Th.Chrome()
	title := req.Tool
	if req.Kind == client.InteractionKindQuestion {
		title = "Agent question"
	} else if title == "" {
		title = "Approval request"
	}
	tags := []layout.FlexChild{rigidTag(env, sc.fail, form.kindTag, kit.TagProps{Text: req.Kind, Variant: kit.TagNeutral})}
	if req.Kind == client.InteractionKindQuestion {
		tags = append(tags, spacerW(theme.SpaceXS), rigidTag(env, sc.fail, form.agentTag, kit.TagProps{Text: "Model output", Variant: kit.TagStatus, Status: theme.StatusInfo}))
	}
	if req.Risk != "" {
		st := theme.StatusInfo
		if req.Risk != "read" {
			st = theme.StatusWarning
		}
		tags = append(tags, spacerW(theme.SpaceXS), rigidTag(env, sc.fail, form.riskTag, kit.TagProps{Text: req.Risk, Variant: kit.TagStatus, Status: st}))
	}
	if req.EnvTag != "" {
		tags = append(tags, spacerW(theme.SpaceXS), rigidTag(env, sc.fail, form.envTag, kit.TagProps{Env: req.EnvTag, Variant: kit.TagEnv}))
	}
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		rigidWidget(func(gtx *layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
				append([]layout.FlexChild{rigidLabel(env, theme.TypeTitle(), c.Fg, title), spacerW(theme.SpaceSM)}, tags...)...,
			)
		}),
		spacerH(theme.SpaceXXS),
		rigidLabel(env, theme.TypeCaption(), c.FgMuted, "session "+req.SessionID+" · request "+req.RequestID),
	)
}

func layoutCardScrollBody(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) layout.Dimensions {
	c := env.Th.Chrome()
	children := make([]layout.FlexChild, 0, 8)
	for _, block := range []struct {
		label, text string
		tp          theme.Type
	}{
		{"", req.Summary, theme.TypeBody()},
		{"", req.Body, theme.TypeBody()},
		{"Full detail", req.Detail, theme.TypeCode()},
	} {
		if block.text == "" {
			continue
		}
		children = append(children, spacerH(theme.SpaceMD))
		if block.label != "" {
			children = append(children, rigidLabel(env, theme.TypeCaption(), c.FgMuted, block.label), spacerH(theme.SpaceXS))
		}
		children = append(children, rigidParagraph(env, block.tp, c.Fg, block.text))
	}
	if req.GrantScopeLabel != "" {
		children = append(children, spacerH(theme.SpaceSM),
			rigidParagraph(env, theme.TypeSmall(), c.FgMuted, "Session scope: "+req.GrantScopeLabel))
	}
	children = append(children, layoutCardInteractiveBody(env, sc, req, form)...)
	children = append(children, spacerH(theme.SpaceMD))
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

func layoutCardInteractiveBody(env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) []layout.FlexChild {
	switch req.Kind {
	case client.InteractionKindSelect:
		return layoutOptionsSection(env, req.Options, form)
	case client.InteractionKindQuestion:
		return layoutQuestionsSection(env, sc, req, form)
	case client.InteractionKindConfirm:
		var out []layout.FlexChild
		if len(req.Options) > 0 {
			out = append(out, layoutOptionsSection(env, req.Options, form)...)
		}
		return append(out, spacerH(theme.SpaceMD),
			rigidTextArea(env, form.denyNote, "Deny note (optional)", "Optional reason sent on Deny", form.busy))
	default:
		return nil
	}
}

func layoutOptionsSection(env *kit.Env, opts []client.InteractionOption, form *cardFormState) []layout.FlexChild {
	out := make([]layout.FlexChild, 0, 2+len(opts))
	out = append(out, spacerH(theme.SpaceMD), rigidLabel(env, theme.TypeCaption(), env.Th.Chrome().FgMuted, "Options"))
	for i := range opts {
		opt := opts[i]
		row := ensureOptionRow(form.optionRows, opt.ID)
		out = append(out, rigidOptionRow(env, row, opt, form.selectedOption == opt.ID, form.busy, func() {
			form.selectedOption = opt.ID
		}))
	}
	return out
}

func layoutQuestionsSection(env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState) []layout.FlexChild {
	out := make([]layout.FlexChild, 0, len(req.Questions)*4)
	for qi := range req.Questions {
		if qi >= len(form.questions) {
			break
		}
		q, qf := req.Questions[qi], form.questions[qi]
		out = append(out, spacerH(theme.SpaceMD), rigidParagraph(env, theme.TypeLabel(), env.Th.Chrome().Fg, q.Prompt))
		if q.Secret {
			out = append(out, layoutSecretQuestion(env, sc, qf, form.busy)...)
			continue
		}
		out = append(out, layoutQuestionOptions(env, &q, qf, form.busy)...)
		if q.AllowText || len(q.Options) == 0 {
			out = append(out, spacerH(theme.SpaceSM), rigidTextArea(env, qf.textInput, "Answer", "Type your answer…", form.busy))
		}
	}
	return out
}

func layoutQuestionOptions(env *kit.Env, q *client.InteractionQuestion, qf *questionFormState, busy bool) []layout.FlexChild {
	out := make([]layout.FlexChild, 0, len(q.Options))
	for oi := range q.Options {
		opt := q.Options[oi]
		if q.MultiSelect {
			cb, ok := qf.checks[opt.ID]
			if !ok {
				cb = kit.NewCheckboxState()
				qf.checks[opt.ID] = cb
			}
			out = append(out, rigidWidget(func(gtx *layout.Context) layout.Dimensions {
				val := kit.CheckOff
				if qf.multiSelected[opt.ID] {
					val = kit.CheckOn
				}
				pr := kit.CheckboxProps{Label: opt.Label, Value: val, Disabled: busy}
				if changed, _, err := cb.Layout(env, gtx, pr); err == nil && changed {
					qf.multiSelected[opt.ID] = !qf.multiSelected[opt.ID]
				}
				return cb.Measure(env, gtx, pr)
			}))
			continue
		}
		row := ensureOptionRow(qf.optionRows, opt.ID)
		out = append(out, rigidOptionRow(env, row, opt, qf.singleSelected == opt.ID, busy, func() {
			qf.singleSelected = opt.ID
		}))
	}
	return out
}

func layoutCardFooter(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState, pin inbox.Pin) layout.Dimensions {
	switch req.Kind {
	case client.InteractionKindConfirm:
		return layoutConfirmFooter(gtx, env, sc, req, form, pin)
	case client.InteractionKindSelect:
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
			rigidInboxButton(env, sc.fail, form.denyBtn, kit.ButtonProps{Text: labelDeny, Variant: kit.Danger, Busy: form.busy}, func() {
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: client.InteractionOutcomeDenied})
			}),
			spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, form.submitBtn, kit.ButtonProps{Text: "Choose option", Variant: kit.Primary, Disabled: form.selectedOption == "", Busy: form.busy}, func() {
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: client.InteractionOutcomeAllowedOnce, OptionID: form.selectedOption})
			}),
		)
	case client.InteractionKindQuestion:
		cancelLabel, cancelOutcome := labelCancel, client.InteractionOutcomeCancelled
		if !slices.Contains(req.OfferedOutcomes, client.InteractionOutcomeCancelled) && slices.Contains(req.OfferedOutcomes, client.InteractionOutcomeDenied) {
			cancelLabel, cancelOutcome = labelDeny, client.InteractionOutcomeDenied
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
			rigidInboxButton(env, sc.fail, form.cancelBtn, kit.ButtonProps{Text: cancelLabel, Variant: kit.Secondary, Busy: form.busy}, func() {
				form.clearSecrets()
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: cancelOutcome})
			}),
			spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, form.submitBtn, kit.ButtonProps{Text: "Submit answer", Variant: kit.Primary, Disabled: !canSubmitQuestions(req, form), Busy: form.busy}, func() {
				wsID, _ := sc.currentWorkspace()
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: client.InteractionOutcomeAnswered, Answers: buildQuestionAnswers(req, form, wsID)})
			}),
		)
	default:
		return layout.Dimensions{}
	}
}

func layoutConfirmFooter(gtx *layout.Context, env *kit.Env, sc *screens, req *client.PendingInteraction, form *cardFormState, pin inbox.Pin) layout.Dimensions {
	btns := []layout.FlexChild{
		rigidInboxButton(env, sc.fail, form.denyBtn, kit.ButtonProps{Text: labelDeny, Variant: kit.Danger, Busy: form.busy}, func() {
			submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: client.InteractionOutcomeDenied, Text: form.denyNote.Text()})
		}),
	}
	if len(req.Options) > 0 && slices.Contains(req.OfferedOutcomes, client.InteractionOutcomeAnswered) {
		btns = append(btns, spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, form.submitBtn, kit.ButtonProps{Text: "Confirm choice", Variant: kit.Primary, Disabled: form.selectedOption == "", Busy: form.busy}, func() {
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: client.InteractionOutcomeAnswered, OptionID: form.selectedOption})
			}))
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, btns...)
	}
	for _, opt := range []struct {
		outcome, label string
		variant        kit.ButtonVariant
		btn            *kit.ButtonState
	}{
		{client.InteractionOutcomeAllowedOnce, "Allow once", kit.Secondary, form.allowOnceBtn},
		{client.InteractionOutcomeAllowedSession, "Allow for session", kit.Primary, form.allowSessBtn},
	} {
		if !slices.Contains(req.OfferedOutcomes, opt.outcome) {
			continue
		}
		btns = append(btns, spacerW(theme.SpaceSM),
			rigidInboxButton(env, sc.fail, opt.btn, kit.ButtonProps{Text: opt.label, Variant: opt.variant, Busy: form.busy}, func() {
				submitInboxAnswer(sc, form, pin, &client.AnswerInteractionInput{Outcome: opt.outcome})
			}))
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx, btns...)
}

func canSubmitQuestions(req *client.PendingInteraction, form *cardFormState) bool {
	if len(req.Questions) == 0 || len(form.questions) < len(req.Questions) {
		return false
	}
	for i := range req.Questions {
		if !canSubmitOneQuestion(&req.Questions[i], form.questions[i]) {
			return false
		}
	}
	return true
}

func canSubmitOneQuestion(q *client.InteractionQuestion, qf *questionFormState) bool {
	if q.Secret {
		return canSubmitSecretQuestion(qf)
	}
	if qf.textInput.Text() != "" {
		return true
	}
	if !q.MultiSelect {
		return qf.singleSelected != ""
	}
	return slices.ContainsFunc(q.Options, func(o client.InteractionOption) bool { return qf.multiSelected[o.ID] })
}

func buildQuestionAnswers(req *client.PendingInteraction, form *cardFormState, wsID string) []client.InteractionQuestionAnswer {
	out := make([]client.InteractionQuestionAnswer, 0, len(req.Questions))
	for i := range req.Questions {
		q, qf := req.Questions[i], form.questions[i]
		if q.Secret {
			out = append(out, buildSecretQuestionAnswer(q.QuestionID, qf, wsID))
			continue
		}
		var optIDs []string
		if q.MultiSelect {
			for oi := range q.Options {
				if id := q.Options[oi].ID; qf.multiSelected[id] {
					optIDs = append(optIDs, id)
				}
			}
		} else if qf.singleSelected != "" {
			optIDs = []string{qf.singleSelected}
		}
		out = append(out, client.InteractionQuestionAnswer{QuestionID: q.QuestionID, OptionIDs: optIDs, Text: qf.textInput.Text()})
	}
	return out
}

func submitInboxAnswer(sc *screens, form *cardFormState, pin inbox.Pin, ans *client.AnswerInteractionInput) {
	if form.busy || sc.inboxWorker == nil || !sc.inbox.CanAnswer(pin) {
		return
	}
	form.busy, form.lastErr = true, ""
	req := inbox.Request{Kind: inbox.OpAnswer, Pin: pin, Answer: *ans}
	sc.inboxWorker.Do(&req)
}

func ensureOptionRow[K comparable](m map[K]*kit.ListRowState, id K) *kit.ListRowState {
	if r, ok := m[id]; ok {
		return r
	}
	r := kit.NewListRowState()
	r.Enter()
	m[id] = r
	return r
}

func rigidOptionRow(env *kit.Env, row *kit.ListRowState, opt client.InteractionOption, selected, busy bool, onSelect func()) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		dims, ev := row.Layout(env, gtx, &kit.ListRowProps{Label: opt.Label, Secondary: opt.Description, Selected: selected, Disabled: busy})
		if ev.Clicked || ev.Opened {
			onSelect()
		}
		return dims
	})
}

func rigidTextArea(env *kit.Env, st *kit.TextAreaState, label, placeholder string, busy bool) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		props := &kit.TextAreaProps{Label: label, Placeholder: placeholder, Disabled: busy}
		st.Layout(env, gtx, props)
		return st.Measure(env, gtx, props)
	})
}

func rigidInboxButton(env *kit.Env, onErr func(error), btn *kit.ButtonState, props kit.ButtonProps, onClick func()) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		clicked, err := btn.Layout(env, gtx, props)
		if err != nil && onErr != nil {
			onErr(err)
		}
		if clicked && !props.Disabled && !props.Busy && onClick != nil {
			onClick()
		}
		return btn.Measure(env, gtx, props)
	})
}

func rigidTag(env *kit.Env, onErr func(error), st *kit.TagState, props kit.TagProps) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions {
		dims, err := st.Layout(env, gtx, props)
		if err != nil && onErr != nil {
			onErr(err)
		}
		return dims
	})
}

func rigidWidget(fn func(*layout.Context) layout.Dimensions) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		return fn(&local)
	})
}

func rigidLabel(env *kit.Env, tp theme.Type, fg color.NRGBA, txt string) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions { return kit.Label(env, gtx, tp, fg, txt) })
}

func rigidParagraph(env *kit.Env, tp theme.Type, fg color.NRGBA, txt string) layout.FlexChild {
	return rigidWidget(func(gtx *layout.Context) layout.Dimensions { return kit.Paragraph(env, gtx, tp, fg, txt) })
}

// measureLabel lays one label into a throwaway macro with a dead input
// source and returns its size, the number the row's height is computed from.
func measureLabel(env *kit.Env, gtx *layout.Context, t theme.Type, c color.NRGBA, txt string, width int) image.Point {
	m := op.Record(gtx.Ops)
	g := *gtx
	var dead input.Source
	g.Source = dead
	g.Constraints = layout.Constraints{Max: image.Pt(width, 1<<20)}
	dims := kit.Label(env, &g, t, c, txt)
	m.Stop()
	return dims.Size
}
