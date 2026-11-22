package appwin

import (
	"bytes"
	"fmt"
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

const minVaultPassphraseBytes = 8

// vaultGate owns the host-vault setup, unlock, recovery, and lock controls
// (F10.5a, desktop-app §5). Secrets enter through TextFieldSecret with
// NoReveal, copy into []byte on submit, and clear immediately on every path.
type vaultGate struct {
	card         *kit.CardState
	pass         *kit.TextFieldState
	confirm      *kit.TextFieldState
	submitBtn    *kit.ButtonState
	modeBtn      *kit.ButtonState
	lockBtn      *kit.ButtonState
	ackBtn       *kit.ButtonState
	recoveryMode bool
	recovery     []byte
	busy         bool
	lastErr      string
	retryAfter   int64
}

func newVaultGate() *vaultGate {
	return &vaultGate{
		card: kit.NewCardState(), pass: kit.NewTextFieldState(), confirm: kit.NewTextFieldState(),
		submitBtn: kit.NewButtonState(), modeBtn: kit.NewButtonState(), lockBtn: kit.NewButtonState(), ackBtn: kit.NewButtonState(),
	}
}

func (g *vaultGate) active(m *vault.Model) bool { return m.Locked() || len(g.recovery) > 0 }

func (g *vaultGate) resetFields() {
	g.pass.SetText("")
	g.confirm.SetText("")
	g.pass, g.confirm = kit.NewTextFieldState(), kit.NewTextFieldState()
}

func (g *vaultGate) clearRecovery() {
	clear(g.recovery)
	g.recovery = nil
}

func (g *vaultGate) applied(res *vault.Result, m *vault.Model) {
	g.busy = false
	if res.Err != nil {
		res.Clear()
		g.retryAfter = res.RetryAfter()
		g.lastErr = vaultErrorMessage(res.Code, g.retryAfter, res.Err)
		return
	}
	if res.Status != nil {
		m.ApplyStatus(*res.Status)
		g.retryAfter = res.Status.RetryAfterSeconds
	}
	g.lastErr = ""
	if res.Kind == vault.OpSetup && len(res.RecoveryKey) > 0 {
		g.clearRecovery()
		g.recovery, res.RecoveryKey = res.RecoveryKey, nil
		return
	}
	res.Clear()
	if res.Kind == vault.OpLock {
		g.recoveryMode = false
		g.resetFields()
	}
}

func vaultErrorMessage(code string, retryAfter int64, err error) string {
	switch code {
	case "vault.invalid_passphrase":
		return "Invalid passphrase or recovery key."
	case "vault.rate_limited":
		if retryAfter > 0 {
			return fmt.Sprintf("Too many attempts. Wait %ds before retrying.", retryAfter)
		}
		return "Too many attempts. Wait before retrying."
	case "vault.not_configured":
		return "Vault is not configured yet."
	case "vault.already_configured":
		return "Vault is already configured."
	default:
		return err.Error()
	}
}

func (g *vaultGate) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Center.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		cardGtx := gtx
		cardW := min(gtx.Dp(theme.DialogWidth), max(gtx.Constraints.Max.X-2*gtx.Dp(theme.SpaceLG), 0))
		cardGtx.Constraints = layout.Constraints{Min: image.Pt(cardW, 0), Max: image.Pt(cardW, gtx.Constraints.Max.Y)}
		dims, _, err := g.card.Layout(env, &cardGtx, kit.CardProps{}, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return g.layoutCardBody(&local, env, sc)
		})
		if err != nil {
			sc.fail(err)
		}
		return dims
	})
}

func (g *vaultGate) layoutCardBody(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	if len(g.recovery) > 0 {
		return g.layoutRecoveryAck(gtx, env)
	}
	if sc.vault.Status().State == client.VaultStateNotConfigured {
		return g.layoutSetup(gtx, env, sc)
	}
	return g.layoutUnlock(gtx, env, sc)
}

func (g *vaultGate) layoutRecoveryAck(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	c := env.Th.Chrome()
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		vaultLabelRow(env, theme.TypeTitle(), c.Fg, "Save your recovery key"),
		layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
		vaultLabelRow(env, theme.TypeSmall(), c.FgMuted, "Shown once. Store it somewhere safe before continuing."),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		vaultLabelRow(env, theme.TypeCode(), c.Fg, string(g.recovery)),
		layout.Rigid(layout.Spacer{Height: theme.SpaceLG}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dims, clicked := layoutVaultButton(env, &gtx, g.ackBtn, kit.ButtonProps{Text: "I saved my recovery key", Variant: kit.Primary}, nil)
			if clicked {
				g.clearRecovery()
			}
			return dims
		}),
	)
}

func (g *vaultGate) layoutSetup(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	submitted := false
	dims := layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		vaultLabelRow(env, theme.TypeTitle(), c.Fg, "Set up vault"),
		layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
		vaultLabelRow(env, theme.TypeSmall(), c.FgMuted, "Create a master passphrase (at least 8 characters)."),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		vaultSecretRow(env, g.pass, "Passphrase", g.busy, &submitted, sc.fail),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		vaultSecretRow(env, g.confirm, "Confirm passphrase", g.busy, &submitted, sc.fail),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.layoutErrorLine(&gtx, env) }),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dims, clicked := layoutVaultButton(env, &gtx, g.submitBtn, kit.ButtonProps{Text: "Create vault", Variant: kit.Primary, Busy: g.busy}, sc.fail)
			if clicked {
				submitted = true
			}
			return dims
		}),
	)
	if submitted && !g.busy {
		g.submitSetup(sc)
	}
	return dims
}

func (g *vaultGate) layoutUnlock(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	fieldLabel, toggleLabel := "Master passphrase", "Use recovery key"
	if g.recoveryMode {
		fieldLabel, toggleLabel = "Recovery key", "Use master passphrase"
	}
	submitted, switched := false, false
	dims := layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		vaultLabelRow(env, theme.TypeTitle(), c.Fg, "Unlock vault"),
		layout.Rigid(layout.Spacer{Height: theme.SpaceSM}.Layout),
		vaultLabelRow(env, theme.TypeSmall(), c.FgMuted, "Enter your passphrase to unlock sessions and protected history."),
		g.layoutWaitingRow(env, sc),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		vaultSecretRow(env, g.pass, fieldLabel, g.busy, &submitted, sc.fail),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.layoutErrorLine(&gtx, env) }),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dims, clicked := layoutVaultButton(env, &gtx, g.submitBtn, kit.ButtonProps{Text: "Unlock", Variant: kit.Primary, Busy: g.busy}, sc.fail)
					if clicked {
						submitted = true
					}
					return dims
				}),
				layout.Rigid(layout.Spacer{Width: theme.SpaceSM}.Layout),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					dims, clicked := layoutVaultButton(env, &gtx, g.modeBtn, kit.ButtonProps{Text: toggleLabel, Variant: kit.Ghost, Disabled: g.busy}, sc.fail)
					if clicked {
						switched = true
					}
					return dims
				}),
			)
		}),
	)
	if switched {
		g.recoveryMode, g.lastErr = !g.recoveryMode, ""
		g.resetFields()
	} else if submitted && !g.busy {
		g.submitUnlock(sc)
	}
	return dims
}

func (g *vaultGate) layoutErrorLine(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	if g.lastErr == "" {
		return layout.Dimensions{}
	}
	return layout.Inset{Top: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Label(env, &l, theme.TypeSmall(), env.Th.Status().Danger, g.lastErr)
	})
}

// layoutWaitingRow names the sessions the lock suspends (activation §3:
// `Waiting(vault_locked)` with the number of waiting runs); unlocking moves
// them back to ready on the host.
func (g *vaultGate) layoutWaitingRow(env *kit.Env, sc *screens) layout.FlexChild {
	n := vaultWaitingCount(sc.sidebar.List())
	if n == 0 {
		return layout.Rigid(layout.Spacer{Height: 0}.Layout)
	}
	word := "sessions wait"
	if n == 1 {
		word = "session waits"
	}
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return layout.Inset{Top: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			l := gtx
			return kit.Label(env, &l, theme.TypeSmall(), env.Th.Status().Warning,
				fmt.Sprintf("%d %s for the vault.", n, word))
		})
	})
}

func (g *vaultGate) submitSetup(sc *screens) {
	p, c := []byte(g.pass.Text()), []byte(g.confirm.Text())
	defer clear(p)
	defer clear(c)
	g.resetFields()
	if len(p) < minVaultPassphraseBytes {
		g.lastErr = "Passphrase must be at least 8 characters."
		return
	}
	if !bytes.Equal(p, c) {
		g.lastErr = "Passphrases do not match."
		return
	}
	if sc.vaultWorker == nil {
		return
	}
	g.busy, g.lastErr = true, ""
	req := vault.Request{Kind: vault.OpSetup, Secret: bytes.Clone(p)}
	sc.vaultWorker.Do(&req)
}

func (g *vaultGate) submitUnlock(sc *screens) {
	s := []byte(g.pass.Text())
	defer clear(s)
	g.resetFields()
	if len(s) == 0 {
		g.lastErr = "Enter a passphrase or recovery key."
		return
	}
	if sc.vaultWorker == nil {
		return
	}
	g.busy, g.lastErr = true, ""
	kind := vault.OpUnlock
	if g.recoveryMode {
		kind = vault.OpUnlockRecovery
	}
	req := vault.Request{Kind: kind, Secret: bytes.Clone(s)}
	sc.vaultWorker.Do(&req)
}

func (g *vaultGate) layoutLockButton(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	if sc.vault.Locked() || sc.vaultWorker == nil {
		return layout.Dimensions{}
	}
	dims, clicked := layoutVaultButton(env, gtx, g.lockBtn, kit.ButtonProps{Text: "Lock vault", Variant: kit.Secondary, Size: kit.SizeSM, Busy: g.busy}, sc.fail)
	if clicked && !g.busy {
		g.busy = true
		req := vault.Request{Kind: vault.OpLock}
		sc.vaultWorker.Do(&req)
	}
	return dims
}

func vaultLabelRow(env *kit.Env, tp theme.Type, fg color.NRGBA, txt string) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Label(env, &l, tp, fg, txt)
	})
}

func vaultSecretRow(env *kit.Env, field *kit.TextFieldState, label string, disabled bool, submitted *bool, onErr func(error)) layout.FlexChild {
	props := &kit.TextFieldProps{Label: label, Variant: kit.TextFieldSecret, NoReveal: true, Disabled: disabled}
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		ev, err := field.Layout(env, &local, props)
		if err != nil && onErr != nil {
			onErr(err)
		}
		if ev.Submitted {
			*submitted = true
		}
		return field.Measure(env, &local, props)
	})
}

func layoutVaultButton(env *kit.Env, gtx *layout.Context, btn *kit.ButtonState, props kit.ButtonProps, onErr func(error)) (layout.Dimensions, bool) {
	clicked, err := btn.Layout(env, gtx, props)
	if err != nil && onErr != nil {
		onErr(err)
	}
	return btn.Measure(env, gtx, props), clicked
}

func (s *screens) applyVaultDelivery(u hostconn.Update) {
	if u.Snapshot != nil {
		s.fail(s.vault.ApplySnapshot(u.Snapshot.State))
	} else if u.Event != nil {
		s.fail(s.vault.ApplyEvent(u.Event.Type, u.Event.Payload))
	}
	if s.vault.Locked() {
		s.secretsView.clearSecretInputs()
		s.inboxView.clearSecretInputs()
	}
}

func (s *screens) applyVaultResults(results []vault.Result) {
	for i := range results {
		s.vaultGate.applied(&results[i], s.vault)
	}
	if s.vault.Locked() {
		s.secretsView.clearSecretInputs()
		s.inboxView.clearSecretInputs()
	}
}
