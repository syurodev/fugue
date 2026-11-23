package appwin

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/vault"
)

// vaultPanel names the one panel the vault gate shows. Exactly one fits each
// pair of host state and vault state (desktop-app §5, FUG-57): the gate never
// offers a passphrase form unless the host is connected and the vault state
// is known, so a state the window cannot read fails closed.
type vaultPanel int

const (
	// panelConnecting waits for the first answer of the host connection.
	panelConnecting vaultPanel = iota
	// panelUnavailable names why the host cannot be reached and what helps.
	panelUnavailable
	// panelChecking waits for the first vault snapshot of a live host.
	panelChecking
	// panelSetup creates the vault of a first run.
	panelSetup
	// panelUnlock opens a locked vault with the master passphrase.
	panelUnlock
	// panelRecovery opens a locked vault with the recovery key.
	panelRecovery
	// panelRecoveryKey shows the one-time key a setup returned; it outranks
	// every other panel so the key is never lost to a connection change.
	panelRecoveryKey
)

// panel picks the panel of the current host and vault state.
func (g *vaultGate) panel(host hostconn.State, m *vault.Model) vaultPanel {
	if len(g.recovery) > 0 {
		return panelRecoveryKey
	}
	switch host {
	case hostconn.StateConnected:
	case hostconn.StateDisconnected, hostconn.StateIncompatible:
		return panelUnavailable
	case hostconn.StateConnecting:
		return panelConnecting
	default:
		// No update has landed yet: still connecting, never a form.
		return panelConnecting
	}
	switch {
	case !m.Known():
		return panelChecking
	case m.Status().State == client.VaultStateNotConfigured:
		return panelSetup
	case g.recoveryMode:
		return panelRecovery
	default:
		return panelUnlock
	}
}

// panelHead is the shared top of every panel: a mark, the heading and one
// line of purpose.
type panelHead struct {
	icon    kit.IconName
	danger  bool
	title   string
	purpose string
}

const (
	panelBadge    = 40 // dp: the mark's tile
	panelPad      = theme.SpaceXXL
	panelMaxInner = 372 // dp: the DialogWidth card minus its padding
)

func (g *vaultGate) layout(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	return layout.Center.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		cardGtx := gtx
		cardW := min(gtx.Dp(theme.DialogWidth), max(gtx.Constraints.Max.X, 0))
		cardGtx.Constraints = layout.Constraints{Min: image.Pt(cardW, 0), Max: image.Pt(cardW, gtx.Constraints.Max.Y)}
		dims, _, err := g.card.Layout(env, &cardGtx, kit.CardProps{Padding: panelPad, Radius: theme.RadiusXL}, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Min.X = local.Constraints.Max.X
			return g.layoutPanel(&local, env, sc)
		})
		if err != nil {
			sc.fail(err)
		}
		return dims
	})
}

func (g *vaultGate) layoutPanel(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	switch g.panel(sc.hostState, sc.vault) {
	case panelRecoveryKey:
		return g.layoutRecoveryKey(gtx, env, sc.fail)
	case panelUnavailable:
		return g.layoutUnavailable(gtx, env, sc)
	case panelConnecting:
		return g.layoutWaiting(gtx, env, sc, panelHead{icon: kit.IconGlobe, title: "Connecting to Fugue", purpose: "Starting the Fugue core. The vault opens once it answers."})
	case panelChecking:
		return g.layoutWaiting(gtx, env, sc, panelHead{icon: kit.IconLock, title: "Checking the vault", purpose: "Reading the vault state from the Fugue core."})
	case panelSetup:
		return g.layoutSetup(gtx, env, sc)
	case panelRecovery:
		return g.layoutRecovery(gtx, env, sc)
	case panelUnlock:
		return g.layoutUnlock(gtx, env, sc)
	}
	return g.layoutUnlock(gtx, env, sc)
}

// layoutHead draws the mark, heading and purpose, then the body under them.
func (g *vaultGate) layoutHead(gtx *layout.Context, env *kit.Env, onErr func(error), h panelHead, body ...layout.FlexChild) layout.Dimensions {
	c := env.Th.Chrome()
	children := make([]layout.FlexChild, 0, 5+len(body))
	children = append(children,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layoutBadge(&local, env, onErr, h.icon, h.danger)
		}),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		vaultLabelRow(env, theme.TypeHeading(), c.Fg, h.title),
		layout.Rigid(layout.Spacer{Height: theme.SpaceXS}.Layout),
		vaultParagraphRow(env, theme.TypeBody(), c.FgMuted, h.purpose),
	)
	children = append(children, body...)
	return layout.Flex{Axis: layout.Vertical}.Layout(*gtx, children...)
}

// layoutBadge draws the panel's mark: a glyph on a soft rounded tile.
func layoutBadge(gtx *layout.Context, env *kit.Env, onErr func(error), icon kit.IconName, danger bool) layout.Dimensions {
	c := env.Th.Chrome()
	fill, fg := c.FillSoft, c.Fg
	if danger {
		fill, fg = c.DangerSoft, c.DangerFg
	}
	side := gtx.Dp(panelBadge)
	rect := image.Rect(0, 0, side, side)
	paint.FillShape(gtx.Ops, fill, clip.UniformRRect(rect, gtx.Dp(theme.RadiusLG)).Op(gtx.Ops))
	glyph := gtx.Dp(theme.IconLG)
	off := (side - glyph) / 2
	local := *gtx
	defer op.Offset(image.Pt(off, off)).Push(local.Ops).Pop()
	if _, err := env.Icon(&local, icon, theme.IconLG, fg); err != nil {
		onErr(err)
	}
	return layout.Dimensions{Size: rect.Max}
}

// layoutWaiting is the panel of a state still being read: the heading and a
// spinner, no field.
func (g *vaultGate) layoutWaiting(gtx *layout.Context, env *kit.Env, sc *screens, h panelHead) layout.Dimensions {
	return g.layoutHead(gtx, env, sc.fail, h,
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return g.spin.Layout(env, &local, gtx.Dp(theme.IconLG), env.Th.Chrome().IconMuted)
		}),
	)
}

func (g *vaultGate) layoutUnavailable(gtx *layout.Context, env *kit.Env, sc *screens) layout.Dimensions {
	c := env.Th.Chrome()
	p := describeHostProblem(sc.hostState, sc.hostReason)
	head := panelHead{icon: kit.IconGlobe, danger: true, title: p.title, purpose: p.detail}
	body := []layout.FlexChild{
		layout.Rigid(layout.Spacer{Height: theme.SpaceXL}.Layout),
		vaultParagraphRow(env, theme.TypeBody(), c.Fg, p.action),
	}
	if dir := sc.m.DataDir(); dir != "" {
		body = append(body,
			layout.Rigid(layout.Spacer{Height: theme.SpaceLG}.Layout),
			vaultLabelRow(env, theme.TypeSmall(), c.FgSubtle, "Data directory"),
			vaultParagraphRow(env, theme.TypeCodeSmall(), c.FgMuted, dir),
		)
	}
	return g.layoutHead(gtx, env, sc.fail, head, body...)
}

func vaultParagraphRow(env *kit.Env, tp theme.Type, fg color.NRGBA, txt string) layout.FlexChild {
	return layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return kit.Paragraph(env, &l, tp, fg, txt)
	})
}
