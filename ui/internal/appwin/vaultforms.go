package appwin

import (
	"image"
	"io"
	"strings"

	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The form panels of the vault gate. Every field fills the panel's width and
// reveals on request; the secret enters through the field's own state, is
// copied into []byte on submit and the states are replaced right after
// (vaultdlg.go), so no panel keeps a passphrase or recovery key as a string.

func (g *vaultGate) layoutSetup(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	submitted := false
	head := panelHead{icon: kit.IconKeyRound, title: "Create your vault", purpose: "Choose a master passphrase to protect sessions and secrets."}
	return g.layoutHead(gtx, env, sc.fail, head,
		layout.Rigid(layout.Spacer{Height: theme.SpaceXXL}.Layout),
		vaultSecretRow(env, g.pass, vaultFieldSpec{label: "Master passphrase", helper: "At least 8 characters.", disabled: g.busy}, &submitted, sc.fail),
		layout.Rigid(layout.Spacer{Height: theme.SpaceLG}.Layout),
		vaultSecretRow(env, g.confirm, vaultFieldSpec{label: "Confirm passphrase", disabled: g.busy}, &submitted, sc.fail),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.layoutErrorLine(&gtx, env) }),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			props := kit.ButtonProps{Text: "Create vault", Variant: kit.Primary, Size: kit.SizeMD, Block: true, Busy: g.busy}
			dims, clicked := layoutVaultButton(env, &gtx, g.submitBtn, props, sc.fail)
			submitted = submitted || clicked
			return dims
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceLG}.Layout),
		vaultParagraphRow(env, theme.TypeSmall(), c.FgMuted, "You get a recovery key next. Without the passphrase or that key, the vault cannot be opened."),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if submitted && !g.busy {
				g.submitSetup(sc)
			}
			return layout.Dimensions{}
		}),
	)
}

func (g *vaultGate) layoutUnlock(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	head := panelHead{icon: kit.IconLock, title: "Unlock your vault", purpose: "Enter your passphrase to open sessions and secrets."}
	return g.layoutEntry(gtx, env, sc, head, vaultEntry{field: "Master passphrase", action: "Unlock", toggle: "Use recovery key"})
}

func (g *vaultGate) layoutRecovery(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	head := panelHead{icon: kit.IconKeyRound, title: "Use your recovery key", purpose: "Enter the recovery key you saved when you created the vault."}
	return g.layoutEntry(gtx, env, sc, head, vaultEntry{field: "Recovery key", action: "Unlock with recovery key", toggle: "Use master passphrase"})
}

// vaultEntry is the text of the single-field entry panels: unlock and
// recovery differ only in words.
type vaultEntry struct{ field, action, toggle string }

func (g *vaultGate) layoutEntry(gtx *layout.Context, env *kit.Env, sc *screens, head panelHead, e vaultEntry) layout.Dimensions {
	submitted, switched := false, false
	dims := g.layoutHead(gtx, env, sc.fail, head,
		g.layoutWaitingRow(env, sc),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXXL}.Layout),
		vaultSecretRow(env, g.pass, vaultFieldSpec{label: e.field, disabled: g.busy}, &submitted, sc.fail),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return g.layoutErrorLine(&gtx, env) }),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			props := kit.ButtonProps{Text: e.action, Variant: kit.Primary, Size: kit.SizeMD, Block: true, Busy: g.busy}
			dims, clicked := layoutVaultButton(env, &gtx, g.submitBtn, props, sc.fail)
			submitted = submitted || clicked
			return dims
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				props := kit.ButtonProps{Text: e.toggle, Variant: kit.Link, Disabled: g.busy}
				dims, clicked := layoutVaultButton(env, &gtx, g.modeBtn, props, sc.fail)
				switched = switched || clicked
				return dims
			})
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

// layoutRecoveryKey shows the key a setup returned. Leaving takes an explicit
// confirmation that the key is stored: the key is shown once.
func (g *vaultGate) layoutRecoveryKey(gtx *layout.Context, env *kit.Env, onErr func(error)) layout.Dimensions {
	head := panelHead{icon: kit.IconKeyRound, title: "Save your recovery key", purpose: "Shown once. It opens the vault if you forget your passphrase."}
	return g.layoutHead(gtx, env, onErr, head,
		layout.Rigid(layout.Spacer{Height: theme.SpaceXXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return g.layoutKeyBox(&gtx, env)
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceMD}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := "Copy key"
			if g.copied {
				label = "Copied"
			}
			dims, clicked := layoutVaultButton(env, &gtx, g.copyBtn, kit.ButtonProps{Text: label, Icon: kit.IconCopy, Variant: kit.Secondary}, nil)
			if clicked {
				g.copyRecovery(&gtx)
			}
			return dims
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			props := kit.CheckboxProps{Label: "I stored this key somewhere safe", Value: g.saved}
			changed, next, err := g.savedChk.Layout(env, &gtx, props)
			if err != nil {
				onErr(err)
			}
			if changed {
				g.saved = next
			}
			return g.savedChk.Measure(env, &gtx, props)
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			props := kit.ButtonProps{Text: "Continue", Variant: kit.Primary, Size: kit.SizeMD, Block: true, Disabled: g.saved != kit.CheckOn}
			dims, clicked := layoutVaultButton(env, &gtx, g.ackBtn, props, nil)
			if clicked {
				g.clearRecovery()
			}
			return dims
		}),
	)
}

// layoutKeyBox draws the key in code type on a tinted tile; a long key wraps
// inside the tile instead of growing the panel.
func (g *vaultGate) layoutKeyBox(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	c := env.Th.Chrome()
	return layout.Background{}.Layout(*gtx,
		func(gtx layout.Context) layout.Dimensions {
			size := gtx.Constraints.Min
			paint.FillShape(gtx.Ops, c.SurfaceTinted, clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops))
			return layout.Dimensions{Size: size}
		},
		func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(theme.SpaceLG).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				local.Constraints.Min.X = local.Constraints.Max.X
				return kit.Paragraph(env, &local, theme.TypeCode(), c.Fg, string(g.recovery))
			})
		},
	)
}

// copyRecovery puts the key on the clipboard. The clipboard takes a copy the
// app cannot clear; that is the user's explicit request, and the key bytes
// the gate holds are zeroed when the panel is confirmed.
func (g *vaultGate) copyRecovery(gtx *layout.Context) {
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(string(g.recovery)))})
	g.copied = true
}
