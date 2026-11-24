package appwin

import (
	"bytes"
	"fmt"
	"image/color"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

const (
	minVaultPassphraseBytes = 8
	labelLockVault          = "Lock vault"
)

// vaultGate owns the host-vault setup, unlock, recovery, and lock controls
// (F10.5a, desktop-app §5). Secrets enter through TextFieldSecret with
// NoReveal, copy into []byte on submit, and clear immediately on every path.
type vaultGate struct {
	card      *kit.CardState
	pass      *kit.TextFieldState
	confirm   *kit.TextFieldState
	submitBtn *kit.ButtonState
	modeBtn   *kit.ButtonState
	lockBtn   *kit.IconButtonState
	ackBtn    *kit.ButtonState
	copyBtn   *kit.ButtonState
	savedChk  *kit.CheckboxState
	spin      kit.Spinner
	// saved is the explicit "I stored the key" confirmation and copied the
	// feedback of the copy button; both reset with the key.
	saved        kit.CheckState
	copied       bool
	recoveryMode bool
	recovery     []byte
	busy         bool
	lastErr      string
	retryAfter   int64
}

func newVaultGate() *vaultGate {
	return &vaultGate{
		card: kit.NewCardState(), pass: kit.NewTextFieldState(), confirm: kit.NewTextFieldState(),
		submitBtn: kit.NewButtonState(), modeBtn: kit.NewButtonState(), lockBtn: kit.NewIconButtonState(), ackBtn: kit.NewButtonState(),
		copyBtn: kit.NewButtonState(), savedChk: kit.NewCheckboxState(),
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
	g.saved, g.copied = kit.CheckOff, false
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

func (g *vaultGate) layoutLockButton(gtx *layout.Context, env *kit.Env, sc *screens) {
	if sc.vault.Locked() || sc.vaultWorker == nil {
		return
	}
	props := &kit.IconButtonProps{
		Icon:     kit.IconLock,
		Label:    labelLockVault,
		Variant:  kit.IconButtonGhost,
		Size:     kit.SizeSM,
		Disabled: g.busy,
	}
	clicked, err := g.lockBtn.Layout(env, gtx, props)
	sc.fail(err)
	if clicked && !g.busy {
		g.busy = true
		req := vault.Request{Kind: vault.OpLock}
		sc.vaultWorker.Do(&req)
	}
}

func vaultLabelRow(env *kit.Env, tp theme.Type, fg color.NRGBA, txt string) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Label(env, &l, tp, fg, txt)
	})
}

// vaultFieldSpec is the text of one secret field of a panel.
type vaultFieldSpec struct {
	label, helper string
	disabled      bool
}

// vaultSecretRow lays one secret field out at the panel's full width with a
// show/hide control. Hiding is the default, and resetFields replaces the
// state on every submit, so a revealed field masks again.
func vaultSecretRow(env *kit.Env, field *kit.TextFieldState, spec vaultFieldSpec, submitted *bool, onErr func(error)) layout.FlexChild {
	props := &kit.TextFieldProps{Label: spec.label, Helper: spec.helper, Variant: kit.TextFieldSecret, Disabled: spec.disabled}
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		local := gtx
		local.Constraints.Min.X = local.Constraints.Max.X
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
	wasLocked := s.vault.Locked()
	if u.Snapshot != nil {
		s.fail(s.vault.ApplySnapshot(u.Snapshot.State))
	} else if u.Event != nil {
		s.fail(s.vault.ApplyEvent(u.Event.Type, u.Event.Payload))
	}
	s.syncVaultViews(wasLocked)
}

func (s *screens) applyVaultResults(results []vault.Result) {
	wasLocked := s.vault.Locked()
	for i := range results {
		s.vaultGate.applied(&results[i], s.vault)
	}
	s.syncVaultViews(wasLocked)
}

func (s *screens) syncVaultViews(wasLocked bool) {
	if wasLocked != s.vault.Locked() {
		s.resetTeamPayloads()
	}
	if s.vault.Locked() {
		s.secretsView.clearSecretInputs()
		s.inboxView.clearSecretInputs()
		return
	}
	if wasLocked {
		s.refreshVisibleTranscripts()
	}
}
