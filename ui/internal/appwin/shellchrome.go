package appwin

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/settings"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	appName      = "Fugue"
	modulesLabel = "Modules"
	osDarwin     = "darwin"
)

func (s *screens) toggleThemeFromUI(env *kit.Env, darkNow bool) {
	mode := settings.ThemeDark
	if darkNow {
		mode = settings.ThemeLight
	}
	if !s.settings.Prefs.Loaded {
		s.settings.Prefs.Current.ThemeMode = mode
		env.Keep()
		return
	}
	next := s.settings.Prefs.Current
	next.ThemeMode = mode
	s.updatePreferences(next)
	env.Keep()
}

func (s *screens) activeSessionParts() (wsName, sessName string) {
	main := s.grid.mainRef()
	if main.Kind != KindSession || main.ID == "" {
		return "", ""
	}
	sess, ok := s.sidebar.Lookup(main.ID)
	if !ok {
		return "", ""
	}
	sessName = sessionName(sess)
	if sess.WorkspaceID == "" {
		return "", sessName
	}
	if ws, ok := s.workspaces.Lookup(sess.WorkspaceID); ok {
		wsName = ws.Name
	}
	return wsName, sessName
}

func (s *screens) topBarContext() string {
	wsName, sessName := s.activeSessionParts()
	if wsName != "" && sessName != "" {
		return wsName + " / " + sessName
	}
	if sessName != "" {
		return sessName
	}
	switch s.grid.rail {
	case railChat:
	case railTeam:
		return teamLabel
	case railModules:
		return modulesLabel
	}
	if s.titleBar != nil && s.titleBar.title != "" {
		return s.titleBar.title
	}
	return appName
}

func (s *screens) statusBarBreadcrumb() string {
	wsName, sessName := s.activeSessionParts()
	if wsName != "" && sessName != "" {
		return wsName + " > " + sessName
	}
	if sessName != "" {
		return sessName
	}
	switch s.grid.rail {
	case railChat:
	case railTeam:
		return teamLabel
	case railModules:
		return modulesLabel
	}
	return ""
}

// stripeBtn returns the Right Stripe companion button state, building it on first use.
func (g *slotGrid) stripeBtn(kind SlotKind) *kit.IconButtonState {
	btn, ok := g.stripeBtns[kind]
	if !ok {
		btn = kit.NewIconButtonState()
		g.stripeBtns[kind] = btn
	}
	return btn
}

type rightStripeItem struct {
	kind    SlotKind
	icon    kit.IconName
	tip     string
	warnDot bool
}

func (s *screens) rightStripeCompanionItems() []rightStripeItem {
	inboxLabel, _ := inboxRowState(s.inbox.PendingCount())
	inboxTip := "Approvals"
	if inboxLabel != "" {
		inboxTip = "Approvals (" + inboxLabel + ")"
	}
	accLabel, _ := accountsRowState(s.accounts.HasSnapshot(), s.accounts.NeedsAttentionCount())
	accTip := "Accounts"
	if accLabel != "" {
		accTip = "Accounts (" + accLabel + ")"
	}
	secIcon := kit.IconKeyRound
	if s.vault.Locked() {
		secIcon = kit.IconLock
	}
	secLabel, _, secBadge := secretsRowState(s.vault.Locked(), s.secrets.HasSnapshot(), len(s.secrets.All()))
	secTip := "Secrets"
	switch {
	case secLabel != "":
		secTip = "Secrets (" + secLabel + ")"
	case secBadge != "":
		secTip = "Secrets (" + secBadge + ")"
	}
	return []rightStripeItem{
		{KindInbox, kit.IconInbox, inboxTip, s.inbox.PendingCount() > 0},
		{KindAccounts, kit.IconCircleUser, accTip, accLabel != ""},
		{KindSecrets, secIcon, secTip, s.vault.Locked()},
	}
}

// pollRightStripe drains pending Right Stripe companion and folded-slot clicks
// before the grid lays out so same-frame slot/drawer updates land immediately
// even under ReducedMotion.
func (s *screens) pollRightStripe(gtx *layout.Context, plan PlanFoldResult) {
	for _, kind := range [...]SlotKind{KindInbox, KindAccounts, KindSecrets} {
		if s.grid.stripeBtn(kind).PollClick(gtx, false, false) {
			s.toggleCompanion(kind)
		}
	}
	if plan.FoldedCols > 0 {
		for _, p := range s.grid.foldedCells(plan) {
			if ensureIconButton(&s.grid.tabBtns, p.key()).PollClick(gtx, false, false) {
				s.toggleFoldedDrawer(p)
			}
		}
	}
}

// layoutRightStripe draws the permanent 30 dp Right Stripe (FUG-94) on AppBg:
// companion tool-window toggles (Approvals, Accounts, Secrets) at the top, and
// folded slot tabs at the bottom when narrow-window columns fold.
func (s *screens) layoutRightStripe(gtx *layout.Context, env *kit.Env, plan PlanFoldResult) layout.Dimensions {
	w := gtx.Dp(theme.RightStripeWidth)
	h := gtx.Constraints.Max.Y
	btnW := gtx.Dp(theme.ControlSM)
	x := max((w-btnW)/2, 0)
	gap := gtx.Dp(theme.SpaceXS)
	y := s.layoutRightStripeCompanions(gtx, env, x, gap, btnW, gap)
	if plan.FoldedCols > 0 {
		s.layoutRightStripeFolded(gtx, env, plan, w, h, x, y, btnW, gap)
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func (s *screens) layoutRightStripeCompanions(gtx *layout.Context, env *kit.Env, x, y, btnW, gap int) int {
	for _, it := range s.rightStripeCompanionItems() {
		btn := s.grid.stripeBtn(it.kind)
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.tip, Variant: kit.IconButtonGhost, Size: kit.SizeSM,
			Toggled: s.hasSlotKind(it.kind),
		}
		var err error
		env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			_, err = btn.Layout(env, gtx, &props)
			if it.warnDot {
				drawStatusBadgeDot(gtx, env, btnW, env.Th.Status().Warning)
			}
		})
		s.fail(err)
		y += btnW + gap
	}
	return y
}

func (s *screens) layoutRightStripeFolded(gtx *layout.Context, env *kit.Env, plan PlanFoldResult, w, h, x, minY, btnW, gap int) {
	folded := s.grid.foldedCells(plan)
	if len(folded) == 0 {
		return
	}
	groupH := len(folded)*btnW + max(len(folded)-1, 0)*gap
	startY := max(h-gap-groupH, minY)
	if startY > minY+gap {
		lineW := min(gtx.Dp(theme.SpaceLG), max(w-2*gtx.Dp(theme.SpaceXXS), 0))
		lineX := max((w-lineW)/2, 0)
		lineH := max(gtx.Dp(theme.Hairline), 1)
		lineY := startY - gap - lineH/2
		paint.FillShape(gtx.Ops, env.Th.Chrome().BorderSubtle,
			clip.Rect(image.Rect(lineX, lineY, lineX+lineW, lineY+lineH)).Op())
	}
	curY := startY
	for _, p := range folded {
		btn := ensureIconButton(&s.grid.tabBtns, p.key())
		ref := refAt(s.grid.doc, p)
		props := kit.IconButtonProps{
			Icon: slotIcon(ref.Kind), Label: slotTitle(s, ref),
			Variant: kit.IconButtonGhost, Size: kit.SizeSM, Toggled: s.grid.drawer == p.key(),
		}
		var err error
		env.Offset(gtx, image.Pt(x, curY), func(gtx *layout.Context) {
			_, err = btn.Layout(env, gtx, &props)
		})
		s.fail(err)
		curY += btnW + gap
	}
}

func (s *screens) toggleFoldedDrawer(p Pos) {
	if s.grid.drawer == p.key() {
		s.grid.drawerPresence.Show(false)
		return
	}
	s.grid.drawer = p.key()
	s.grid.drawerPresence.Show(true)
}

// drawStatusBadgeDot draws a compact status dot at the top-right corner of a
// ControlSM button with a thin AppBg halo so it reads crisply over icons.
func drawStatusBadgeDot(gtx *layout.Context, env *kit.Env, btnSize int, col color.NRGBA) {
	dotD := max(gtx.Dp(theme.SpaceSM), 6)
	ring := max(gtx.Dp(theme.SpaceXXS)/2, 1)
	pos := image.Pt(max(btnSize-dotD+1, 0), 0)
	outer := image.Rect(pos.X-ring, pos.Y-ring, pos.X+dotD+ring, pos.Y+dotD+ring)
	inner := image.Rect(pos.X, pos.Y, pos.X+dotD, pos.Y+dotD)
	paint.FillShape(gtx.Ops, env.Th.Chrome().AppBg, clip.UniformRRect(outer, (dotD+2*ring)/2).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, col, clip.UniformRRect(inner, dotD/2).Op(gtx.Ops))
}

// layoutStatusBar draws the full-width 22 dp Bottom Status Bar (FUG-95) on
// AppBg: left breadcrumb (workspace > session or view title), right Lock vault
// action (centered with a full 24 dp HitMin target) and host connection status
// dot/tooltip/"Reconnecting…".
func (s *screens) layoutStatusBar(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	padX := gtx.Dp(theme.SpaceMD)
	rightStartX := max(w-padX, 0)
	rightStartX, hasConn := s.layoutStatusBarConn(gtx, env, rightStartX, h)
	rightStartX = s.layoutStatusBarLock(gtx, env, rightStartX, h, hasConn)
	s.layoutStatusBarBreadcrumb(gtx, env, padX, rightStartX, h)
	return layout.Dimensions{Size: image.Pt(w, h)}
}

func (s *screens) layoutStatusBarConn(gtx *layout.Context, env *kit.Env, rightStartX, h int) (int, bool) {
	state, reason := s.m.State()
	if s.hostState != "" {
		state, reason = s.hostState, s.hostReason
	}
	if state == "" {
		return rightStartX, false
	}
	dotD := max(gtx.Dp(theme.SpaceSM), 6)
	var reconnCall op.CallOp
	var reconnDims layout.Dimensions
	if state == hostconn.StateConnecting {
		measure := *gtx
		measure.Constraints.Min = image.Point{}
		m := op.Record(measure.Ops)
		reconnDims = kit.Label(env, &measure, theme.TypeCaption(), env.Th.Chrome().FgMuted, "Reconnecting…")
		reconnCall = m.Stop()
		rightStartX -= reconnDims.Size.X + gtx.Dp(theme.SpaceXS)
	}
	dotX := rightStartX - dotD
	dotY := max((h-dotD)/2, 0)
	tip := "Host connected"
	col := env.Th.Chrome().FgSubtle
	if state != hostconn.StateConnected {
		tip = statusLine(state, reason)
		col = statusColor(env.Th, state)
	}
	r := image.Rect(0, 0, dotD, dotD)
	env.Offset(gtx, image.Pt(dotX, dotY), func(gtx *layout.Context) {
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(r, dotD/2).Op(gtx.Ops))
		area := clip.Rect(r).Push(gtx.Ops)
		event.Op(gtx.Ops, &s.connDotTag)
		s.pollConnDotEvents(gtx)
		area.Pop()
		env.TooltipAnchor(&s.connDotTag, r, s.connDotHovered, false, s.connDotHasPtr, s.connDotPtr, kit.TooltipProps{Text: tip})
	})
	if reconnDims.Size.X > 0 {
		off := op.Offset(image.Pt(dotX+dotD+gtx.Dp(theme.SpaceXS), (h-reconnDims.Size.Y)/2)).Push(gtx.Ops)
		reconnCall.Add(gtx.Ops)
		off.Pop()
	}
	return dotX, true
}

func (s *screens) layoutStatusBarLock(gtx *layout.Context, env *kit.Env, rightStartX, h int, hasConn bool) int {
	if s.vault.Locked() || s.vaultWorker == nil {
		return rightStartX
	}
	lockProps := kit.IconButtonProps{Icon: kit.IconLock, Label: labelLockVault, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
	lockSize := s.vaultGate.lockBtn.Measure(env, gtx, &lockProps).Size
	if hasConn {
		rightStartX -= gtx.Dp(theme.SpaceMD)
	}
	lockX := max(rightStartX-lockSize.X, 0)
	env.Offset(gtx, image.Pt(lockX, (h-lockSize.Y)/2), func(gtx *layout.Context) {
		local := *gtx
		s.vaultGate.layoutLockButton(&local, env, s)
	})
	return lockX
}

func (s *screens) layoutStatusBarBreadcrumb(gtx *layout.Context, env *kit.Env, padX, rightStartX, h int) {
	crumb := s.statusBarBreadcrumb()
	if crumb == "" {
		return
	}
	maxCrumbW := max(rightStartX-padX-gtx.Dp(theme.SpaceMD), 0)
	if maxCrumbW <= 0 {
		return
	}
	lineH := gtx.Sp(theme.TypeCaption().Line)
	env.Offset(gtx, image.Pt(padX, (h-lineH)/2), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: image.Pt(maxCrumbW, h)}
		defer clip.Rect{Max: image.Pt(maxCrumbW, lineH)}.Push(local.Ops).Pop()
		kit.Label(env, &local, theme.TypeCaption(), env.Th.Chrome().FgMuted, crumb)
	})
}

func (s *screens) pollConnDotEvents(gtx *layout.Context) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.connDotTag, Kinds: pointer.Enter | pointer.Move | pointer.Leave | pointer.Cancel})
		if !ok {
			return
		}
		pe, is := ev.(pointer.Event)
		if !is {
			continue
		}
		switch pe.Kind {
		case pointer.Enter, pointer.Move:
			s.connDotHovered, s.connDotHasPtr = true, true
			s.connDotPtr = pe.Position.Round()
		case pointer.Leave, pointer.Cancel:
			s.connDotHovered, s.connDotHasPtr = false, false
		case pointer.Press, pointer.Release, pointer.Drag, pointer.Scroll:
		}
	}
}
