package appwin

import (
	"image"
	"image/color"
	"strings"

	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/secrets"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type secretAddDialog struct {
	dialog         *kit.DialogState
	cancel, submit *kit.ButtonState
	scopeSeg       *kit.SegmentedState
	name           *kit.TextFieldState
	description    *kit.TextFieldState
	value          *kit.TextFieldState
	scopeIdx       int
	wsID           string
	busy           bool
	lastErr        string
}

func newSecretAddDialog() *secretAddDialog {
	return &secretAddDialog{
		dialog:      kit.NewDialogState(),
		cancel:      kit.NewButtonState(),
		submit:      kit.NewButtonState(),
		scopeSeg:    kit.NewSegmentedState(),
		name:        kit.NewTextFieldState(),
		description: kit.NewTextFieldState(),
		value:       kit.NewTextFieldState(),
	}
}

func (d *secretAddDialog) clearSecret() {
	d.value.SetText("")
	d.value = kit.NewTextFieldState()
}

func (d *secretAddDialog) open(wsID string) {
	d.wsID = wsID
	d.scopeIdx = 0
	d.name.SetText("")
	d.description.SetText("")
	d.clearSecret()
	d.busy = false
	d.lastErr = ""
	d.dialog.Open()
}

func (d *secretAddDialog) applied(res *secrets.Result, m *secrets.Model) {
	if !d.dialog.IsOpen() {
		return
	}
	d.busy = false
	if res.Err != nil {
		d.lastErr = res.Err.Error()
		return
	}
	d.lastErr = ""
	m.UpsertMeta(&res.Secret)
	d.clearSecret()
	d.dialog.Close()
}

func (d *secretAddDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	wsID, wsName := sc.currentWorkspace()
	d.wsID = wsID
	wsLabel := workspaceScopeLabel(wsName)
	nameTrim := strings.TrimSpace(d.name.Text())
	canSubmit := nameTrim != "" && d.value.Text() != "" && !sc.vault.Locked() && sc.secretsWorker != nil

	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:         kit.DialogForm,
		Title:           labelAddSecret,
		Description:     "Stored encrypted in the host vault. Values are write-only and never displayed again.",
		Cancel:          d.cancel,
		Primary:         d.submit,
		PrimaryLabel:    "Save secret",
		PrimaryVariant:  kit.Primary,
		PrimaryDisabled: !canSubmit,
		Dirty:           nameTrim != "" || d.description.Text() != "" || d.value.Text() != "",
		Busy:            d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newSecretDlgBody(env, gtx, bands, sc.fail)
			d.layIdentity(body, wsLabel, canSubmit)
			d.layDescription(body)
			d.layValue(body, canSubmit)
			if d.lastErr != "" {
				body.label(d.lastErr, env.Th.Status().Danger)
			}
			return body.size()
		},
	})
	sc.fail(err)
	if res.Cancelled {
		d.clearSecret()
		return
	}
	if res.Submitted && canSubmit {
		d.submitSecret(sc)
	}
}

func (d *secretAddDialog) layScope(body *secretDlgBody, wsLabel string) {
	body.field(d.scopeSeg.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		props := d.scopeProps(wsLabel)
		changed, next, err := d.scopeSeg.Layout(body.env, gtx, props)
		if err == nil && changed {
			d.scopeIdx = next
		}
		props.Selected = d.scopeIdx
		return d.scopeSeg.Measure(body.env, gtx, props), err
	})
}

func (d *secretAddDialog) scopeProps(wsLabel string) kit.SegmentedProps {
	return kit.SegmentedProps{
		Options: []kit.SegmentedOption{
			{ID: client.SecretScopeGlobal, Label: labelGlobal},
			{ID: client.SecretScopeWorkspace, Label: wsLabel, Disabled: d.wsID == ""},
		},
		Selected: d.scopeIdx,
		Disabled: d.busy,
	}
}

func (d *secretAddDialog) layIdentity(body *secretDlgBody, wsLabel string, canSubmit bool) {
	right := body.w * 4 / 9
	local := *body.gtx
	local.Constraints.Min = image.Point{}
	scopeWidth := d.scopeSeg.Measure(body.env, &local, d.scopeProps(wsLabel)).Size.X
	nameWidth := body.w - right - body.gap
	if right < scopeWidth || nameWidth < body.gtx.Dp(theme.ControlLG)*4 {
		d.layName(body, canSubmit)
		d.layScope(body, wsLabel)
		return
	}
	name, scope := *body, *body
	name.w = nameWidth
	scope.w = right
	scope.x = nameWidth + body.gap
	d.layName(&name, canSubmit)
	scope.label("Scope", body.env.Th.Chrome().FgMuted)
	scope.y -= scope.gap - scope.gtx.Dp(theme.SpaceXS)
	d.layScope(&scope, wsLabel)
	body.y = max(name.y, scope.y)
}

func (d *secretAddDialog) layName(body *secretDlgBody, canSubmit bool) {
	body.textField(d.name, &kit.TextFieldProps{
		Label:       labelName,
		Placeholder: "OPENAI_API_KEY",
		Disabled:    d.busy,
	}, canSubmit, d.dialog)
}

func (d *secretAddDialog) layDescription(body *secretDlgBody) {
	body.textField(d.description, &kit.TextFieldProps{
		Label:       "Description (optional)",
		Placeholder: "Used by vendor sessions or modules",
		Disabled:    d.busy,
	}, false, d.dialog)
}

func (d *secretAddDialog) layValue(body *secretDlgBody, canSubmit bool) {
	body.textField(d.value, &kit.TextFieldProps{
		Label:       labelSecretValue,
		Placeholder: placeholderSecretValue,
		Variant:     kit.TextFieldSecret,
		NoReveal:    true,
		Disabled:    d.busy,
	}, canSubmit, d.dialog)
}

func (d *secretAddDialog) submitSecret(sc *screens) {
	if d.busy || sc.secretsWorker == nil {
		return
	}
	scopeKind, scopeID := client.SecretScopeGlobal, ""
	if d.scopeIdx == 1 && d.wsID != "" {
		scopeKind, scopeID = client.SecretScopeWorkspace, d.wsID
	}
	val := []byte(d.value.Text())
	d.clearSecret()
	d.busy = true
	d.lastErr = ""
	req := secrets.Request{
		Kind: secrets.OpPutSecret,
		Put: client.PutSecretInput{
			ScopeKind:   scopeKind,
			ScopeID:     scopeID,
			Name:        strings.TrimSpace(d.name.Text()),
			Description: strings.TrimSpace(d.description.Text()),
			Value:       val,
		},
	}
	sc.secretsWorker.Do(&req)
}

type secretReplaceDialog struct {
	dialog         *kit.DialogState
	cancel, submit *kit.ButtonState
	value          *kit.TextFieldState
	target         client.SecretMeta
	busy           bool
	lastErr        string
}

func newSecretReplaceDialog() *secretReplaceDialog {
	return &secretReplaceDialog{
		dialog: kit.NewDialogState(),
		cancel: kit.NewButtonState(),
		submit: kit.NewButtonState(),
		value:  kit.NewTextFieldState(),
	}
}

func (d *secretReplaceDialog) clearSecret() {
	d.value.SetText("")
	d.value = kit.NewTextFieldState()
}

func (d *secretReplaceDialog) open(meta *client.SecretMeta) {
	d.target = *meta
	d.clearSecret()
	d.busy = false
	d.lastErr = ""
	d.dialog.Open()
}

func (d *secretReplaceDialog) applied(res *secrets.Result, m *secrets.Model) {
	if !d.dialog.IsOpen() {
		return
	}
	d.busy = false
	if res.Err != nil {
		d.lastErr = res.Err.Error()
		return
	}
	d.lastErr = ""
	m.UpsertMeta(&res.Secret)
	d.clearSecret()
	d.dialog.Close()
}

func (d *secretReplaceDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	canSubmit := d.value.Text() != "" && !sc.vault.Locked() && sc.secretsWorker != nil
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:         kit.DialogForm,
		Title:           "Replace " + d.target.Name,
		Description:     "Enter a new value. The previous secret value cannot be viewed and will be overwritten.",
		Cancel:          d.cancel,
		Primary:         d.submit,
		PrimaryLabel:    "Replace value",
		PrimaryVariant:  kit.Primary,
		PrimaryDisabled: !canSubmit,
		Dirty:           d.value.Text() != "",
		Busy:            d.busy,
		Body: func(gtx *layout.Context, bands *kit.DialogBands) image.Point {
			body := newSecretDlgBody(env, gtx, bands, sc.fail)
			body.textField(d.value, &kit.TextFieldProps{
				Label:       "New secret value",
				Placeholder: "Enter new secret value…",
				Variant:     kit.TextFieldSecret,
				NoReveal:    true,
				Disabled:    d.busy,
			}, canSubmit, d.dialog)
			if d.lastErr != "" {
				body.label(d.lastErr, env.Th.Status().Danger)
			}
			return body.size()
		},
	})
	sc.fail(err)
	if res.Cancelled {
		d.clearSecret()
		return
	}
	if res.Submitted && canSubmit {
		d.submitReplace(sc)
	}
}

func (d *secretReplaceDialog) submitReplace(sc *screens) {
	if d.busy || sc.secretsWorker == nil {
		return
	}
	val := []byte(d.value.Text())
	d.clearSecret()
	d.busy = true
	d.lastErr = ""
	req := secrets.Request{
		Kind: secrets.OpPutSecret,
		Put: client.PutSecretInput{
			ScopeKind:   d.target.ScopeKind,
			ScopeID:     d.target.ScopeID,
			Name:        d.target.Name,
			Description: d.target.Description,
			Value:       val,
		},
	}
	sc.secretsWorker.Do(&req)
}

type secretDeleteDialog struct {
	dialog          *kit.DialogState
	cancel, confirm *kit.ButtonState
	target          client.SecretMeta
	busy            bool
	lastErr         string
}

func newSecretDeleteDialog() *secretDeleteDialog {
	return &secretDeleteDialog{
		dialog:  kit.NewDialogState(),
		cancel:  kit.NewButtonState(),
		confirm: kit.NewButtonState(),
	}
}

func (d *secretDeleteDialog) open(meta *client.SecretMeta) {
	d.target = *meta
	d.busy = false
	d.lastErr = ""
	d.dialog.Open()
}

func (d *secretDeleteDialog) applied(res *secrets.Result, m *secrets.Model) {
	if !d.dialog.IsOpen() {
		return
	}
	d.busy = false
	if res.Err != nil {
		d.lastErr = res.Err.Error()
		return
	}
	d.lastErr = ""
	m.RemoveByID(res.SecretID)
	d.dialog.Close()
}

func (d *secretDeleteDialog) layout(env *kit.Env, gtx *layout.Context, sc *screens) {
	if !d.dialog.IsOpen() {
		return
	}
	res, err := d.dialog.Layout(env, gtx, &kit.DialogProps{
		Variant:         kit.DialogConfirm,
		Title:           "Delete " + d.target.Name,
		Description:     "Permanently removes this secret from the vault. Sessions or modules referencing it can no longer resolve it.",
		Cancel:          d.cancel,
		Primary:         d.confirm,
		PrimaryLabel:    labelDelete,
		PrimaryVariant:  kit.Danger,
		PrimaryDisabled: sc.vault.Locked() || sc.secretsWorker == nil,
		Busy:            d.busy,
		Body: func(gtx *layout.Context, _ *kit.DialogBands) image.Point {
			if d.lastErr == "" {
				return image.Point{}
			}
			dims := kit.Paragraph(env, gtx, theme.TypeSmall(), env.Th.Status().Danger, d.lastErr)
			return dims.Size
		},
	})
	sc.fail(err)
	if res.Submitted && !d.busy && sc.secretsWorker != nil && !sc.vault.Locked() {
		d.busy = true
		d.lastErr = ""
		req := secrets.Request{
			Kind:     secrets.OpDeleteSecret,
			SecretID: d.target.SecretID,
		}
		sc.secretsWorker.Do(&req)
	}
}

type secretDlgBody struct {
	env   *kit.Env
	bands *kit.DialogBands
	gtx   *layout.Context
	fail  func(error)
	w     int
	x     int
	y     int
	gap   int
}

func newSecretDlgBody(env *kit.Env, gtx *layout.Context, bands *kit.DialogBands, fail func(error)) *secretDlgBody {
	return &secretDlgBody{env: env, bands: bands, gtx: gtx, fail: fail, w: gtx.Constraints.Max.X, gap: gtx.Dp(theme.SpaceMD)}
}

func (b *secretDlgBody) field(tag event.Tag, draw func(gtx *layout.Context) (layout.Dimensions, error)) {
	local := *b.gtx
	// The dialog's body has a large height limit for scrolling. Each field
	// needs its natural height so the next focus band starts just below it.
	local.Constraints = layout.Constraints{
		Min: image.Pt(b.w, 0),
		Max: image.Pt(b.w, b.gtx.Constraints.Max.Y),
	}
	stack := op.Offset(image.Pt(b.x, b.y)).Push(local.Ops)
	dims, err := draw(&local)
	stack.Pop()
	if err != nil {
		b.fail(err)
	}
	if tag != nil {
		b.bands.Focusable(tag, b.y, b.y+dims.Size.Y)
	}
	b.y += dims.Size.Y + b.gap
}

func (b *secretDlgBody) textField(field *kit.TextFieldState, props *kit.TextFieldProps, canSubmit bool, dialog *kit.DialogState) {
	b.field(field.Tag(), func(gtx *layout.Context) (layout.Dimensions, error) {
		ev, err := field.Layout(b.env, gtx, props)
		if err == nil && ev.Submitted && canSubmit {
			dialog.Submit()
		}
		return field.Measure(b.env, gtx, props), err
	})
}

func (b *secretDlgBody) label(text string, c color.NRGBA) {
	b.field(nil, func(gtx *layout.Context) (layout.Dimensions, error) {
		l := *gtx
		return kit.Label(b.env, &l, theme.TypeSmall(), c, text), nil
	})
}

func (b *secretDlgBody) size() image.Point { return image.Pt(b.w, max(b.y-b.gap, 0)) }
