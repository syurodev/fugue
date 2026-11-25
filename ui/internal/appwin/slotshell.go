package appwin

import (
	"image"
	"image/color"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// atPoint runs fn with the layout origin moved to p; anchors laid out inside
// must go through env.Offset instead, which keeps the window-relative
// record.
func atPoint(gtx *layout.Context, p image.Point, fn func(*layout.Context)) {
	defer op.Offset(p).Push(gtx.Ops).Pop()
	fn(gtx)
}

// The app shell (desktop-app §5.3, DA-10): a darker AppBg base carrying the
// rail and the sidebar directly on it, and the slot grid inside one rounded
// CanvasBg container framed by the darker base. A narrow window folds
// regular columns into the edge tab strip first and the sidebar second; the
// main slot keeps its minimum.

// layoutShell draws the failure strip over the shell base, then rail,
// sidebar and grid (F10.5e keeps the strip above the panes).
func (s *screens) layoutShell(gtx *layout.Context, env *kit.Env) {
	if s.vaultGate.active(s.vault) {
		s.vaultGate.layout(gtx, env, s)
		return
	}
	s.layoutShortcuts(gtx)
	// The outer shell is the darker AppBg everywhere: rail and sidebar sit
	// on it directly, the grid floats in its rounded container (§5.3).
	paint.FillShape(gtx.Ops, env.Th.Chrome().AppBg, clip.Rect{Max: gtx.Constraints.Max}.Op())
	failGtx := *gtx
	failGtx.Constraints.Min = image.Point{}
	failDims := s.failure.layout(env, &failGtx, s)
	bodyH := max(gtx.Constraints.Max.Y-failDims.Size.Y, 0)
	env.Offset(gtx, image.Pt(0, failDims.Size.Y), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Max: image.Pt(gtx.Constraints.Max.X, bodyH)}
		s.layoutShellBody(&local, env)
	})
}

// layoutShortcuts consumes the shell-level shortcuts (§5.3): Ctrl/Cmd+, opens
// or closes the settings slot and arms its reads; Ctrl/Cmd+Shift+A and
// Ctrl/Cmd+Shift+I switch to the accounts and approvals slots, closing the
// other one — the pane switches of F10.5b and F10.5c over slots.
func (s *screens) layoutShortcuts(gtx *layout.Context) {
	s.switchSlotShortcut(gtx, keyAccounts, KindAccounts, KindInbox)
	s.switchSlotShortcut(gtx, keyApprovals, KindInbox, KindAccounts)
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModShortcut, Name: key.Name(keySettings)})
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			if s.hasSlotKind(KindSettings) {
				s.grid.closeSlotOf(KindSettings)
			} else {
				s.openSettings()
			}
		}
	}
}

// The shortcut keys of the shell toggles.
const (
	keySettings  = ","
	keyAccounts  = "A"
	keyApprovals = "I"
)

// switchSlotShortcut binds Ctrl/Cmd+Shift+key: the press opens the kind's
// slot and closes the other pane slot, or closes it when it is already the
// one open.
func (s *screens) switchSlotShortcut(gtx *layout.Context, name string, kind, other SlotKind) {
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModShortcut | key.ModShift, Name: key.Name(name)})
		if !ok {
			return
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			s.grid.closeSlotOf(other)
			if s.hasSlotKind(kind) {
				s.grid.closeSlotOf(kind)
			} else {
				s.grid.openCompanion(SlotRef{Kind: kind})
			}
		}
	}
}

// toggleCompanion opens the kind's slot or closes the one that exists — the
// keyboard mirror of the sidebar rows.
func (s *screens) toggleCompanion(kind SlotKind) {
	for _, p := range regularCells(s.grid.doc) {
		if slot, _ := slotAt(s.grid.doc, p); slot.Ref.Kind == kind {
			s.grid.edited(CloseSlot(s.grid.doc, p))
			return
		}
	}
	s.grid.openCompanion(SlotRef{Kind: kind})
}

// layoutShellBody lays out rail, sidebar and the grid container; a
// width-driven sidebar fold removes the sidebar before the grid measures.
func (s *screens) layoutShellBody(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	plan := s.planShell(gtx)
	sidebarOpen := s.pollRailAndSidebar(gtx, plan.TempSidebarFolded)
	openProg, fadeProg := s.stepSidebarOpen(env, sidebarOpen, plan.TempSidebarFolded)

	totalW, totalH := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	rightW := min(gtx.Dp(theme.RightStripeWidth), max(totalW-railW, 0))
	gutter := gtx.Dp(theme.IslandGap)
	maxSideW := min(max(totalW-railW-rightW-gutter, 0), gtx.Dp(theme.SidebarWidth))
	sideW := scaledPx(maxSideW, openProg)

	railGtx := *gtx
	railGtx.Constraints = layout.Constraints{Min: image.Pt(railW, 0), Max: image.Pt(railW, totalH)}
	s.layoutRail(&railGtx, env, sidebarOpen)
	s.layoutAnimatedSidebar(gtx, env, railW, maxSideW, sideW, totalH, openProg, fadeProg)

	gridX := railW + sideW + scaledPx(gutter, openProg)
	gridW := max(totalW-gridX-rightW, 0)
	env.Offset(gtx, image.Pt(gridX, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(gridW, 0), Max: image.Pt(gridW, totalH)}
		s.layoutGridContainer(&local, env, plan)
	})
	if rightW > 0 {
		env.Offset(gtx, image.Pt(totalW-rightW, 0), func(gtx *layout.Context) {
			local := *gtx
			local.Constraints = layout.Constraints{Min: image.Pt(rightW, 0), Max: image.Pt(rightW, totalH)}
			s.layoutRightStripe(&local, env, plan)
		})
	}
	return layout.Dimensions{Size: image.Pt(totalW, totalH)}
}

func (s *screens) pollRailAndSidebar(gtx *layout.Context, tempFolded bool) bool {
	for _, item := range []railItem{railChat, railTeam, railModules} {
		if s.grid.railBtn(item).PollClick(gtx, false, false) {
			if s.grid.rail == item {
				doc := cloneLayout(s.grid.doc)
				doc.SidebarCollapsed = s.sidebarVisible()
				s.grid.edited(doc)
			} else {
				s.grid.selectRail(item)
				if !s.sidebarVisible() {
					doc := cloneLayout(s.grid.doc)
					doc.SidebarCollapsed = false
					s.grid.edited(doc)
				}
			}
		}
	}
	return s.sidebarVisible() && !tempFolded
}

func (s *screens) stepSidebarOpen(env *kit.Env, sidebarOpen, tempFolded bool) (openProg, fadeProg float32) {
	target := float32(0)
	if sidebarOpen {
		target = 1
	}
	snap := env.ReducedMotion || tempFolded || s.grid.wasTempFolded != tempFolded
	s.grid.wasTempFolded = tempFolded
	if snap {
		s.grid.sidebarOpenF.Snap(target)
		s.grid.sidebarFadeF.Snap(target)
		return target, target
	}
	openProg = s.grid.sidebarOpenF.Follow(target, env.DT())
	if s.grid.sidebarOpenF.Moving() {
		env.Keep()
	}
	fadeSpec := motion.Exit()
	if sidebarOpen {
		fadeSpec = motion.Snappy()
	}
	s.grid.sidebarFadeF.Use(fadeSpec)
	fadeProg = s.grid.sidebarFadeF.Follow(target, env.DT())
	if s.grid.sidebarFadeF.Moving() {
		env.Keep()
	}
	return openProg, fadeProg
}

func (s *screens) layoutAnimatedSidebar(gtx *layout.Context, env *kit.Env, railW, maxSideW, sideW, colH int, openProg, fadeProg float32) {
	if sideW <= 0 || openProg <= 0 || fadeProg <= 0 || colH <= 0 {
		return
	}
	env.Offset(gtx, image.Pt(railW, 0), func(gtx *layout.Context) {
		box := image.Rect(0, 0, sideW, colH)
		radius := gtx.Dp(theme.RadiusIsland)
		rr := clip.UniformRRect(box, radius)
		alpha := min(max(fadeProg, 0), 1)
		if alpha < 1 {
			defer paint.PushOpacity(gtx.Ops, alpha).Pop()
		}
		paint.FillShape(gtx.Ops, env.Th.Chrome().CanvasBg, rr.Op(gtx.Ops))
		stack := rr.Push(gtx.Ops)
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(maxSideW, 0), Max: image.Pt(maxSideW, colH)}
		s.layoutSidebarPanel(&local, env)
		stack.Pop()
		paint.FillShape(gtx.Ops, env.Th.Chrome().BorderSubtle,
			clip.Stroke{Path: rr.Path(gtx.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1))}.Op())
	})
}

func scaledPx(full int, prog float32) int {
	if prog <= 0 {
		return 0
	}
	if prog >= 1 {
		return full
	}
	return int(float32(full)*prog + 0.5)
}

// planShell derives the narrow-window fold for the shell body. Max.X is the
// width rail, sidebar and grid share (drawWindow's side insets are already
// gone), and layoutMinWidthDp counts exactly those three.
func (s *screens) planShell(gtx *layout.Context) PlanFoldResult {
	return PlanFold(pxToDp(gtx, gtx.Constraints.Max.X), s.sidebarVisible(), len(s.grid.doc.Columns))
}

// sidebarVisible is the sidebar's user state: the persisted collapsed flag.
func (s *screens) sidebarVisible() bool { return !s.grid.doc.SidebarCollapsed }

// layoutGridContainer lays the slot grid directly over the shell's AppBg so
// each slot draws as its own 8 dp rounded CanvasBg island separated by 2 dp
// gutters (FUG-92).
func (s *screens) layoutGridContainer(gtx *layout.Context, env *kit.Env, plan PlanFoldResult) layout.Dimensions {
	local := *gtx
	gridRect := image.Rectangle{Max: local.Constraints.Max}
	if gridRect.Empty() {
		return layout.Dimensions{}
	}
	local.Constraints = layout.Constraints{Min: image.Pt(0, 0), Max: gridRect.Size()}
	s.grid.layoutArea(&local, env, s, gridRect, plan)
	return layout.Dimensions{Size: gridRect.Size()}
}

// layoutRail draws the 30 dp rail (§5.3, FUG-93) on the shell's AppBg: 24 dp
// icon buttons centered in the column with kit Tooltips and a Smooth sliding
// SelectedWash pill while the sidebar is open. Clicking an active icon toggles
// SidebarCollapsed; clicking an inactive icon switches mode and opens the sidebar.
func (s *screens) layoutRail(gtx *layout.Context, env *kit.Env, sidebarOpen bool) layout.Dimensions {
	w := gtx.Dp(theme.RailWidth)
	h := gtx.Constraints.Max.Y
	btnW := gtx.Dp(theme.ControlSM)
	x := max((w-btnW)/2, 0)
	gap := gtx.Dp(theme.SpaceXS)
	items := []struct {
		item railItem
		icon kit.IconName
		hint string
	}{
		{railChat, kit.IconMessageSquare, "Chat (click again to toggle sidebar)"},
		{railTeam, kit.IconUsers, teamLabel + " (click again to toggle sidebar)"},
		{railModules, kit.IconWrench, "Modules (click again to toggle sidebar)"},
	}
	targetY := gap
	for i, it := range items {
		if s.grid.rail == it.item {
			targetY = gap + i*(btnW+gap)
			break
		}
	}
	var pillY float32
	if env.ReducedMotion {
		s.grid.railY.Snap(float32(targetY))
		pillY = float32(targetY)
	} else {
		pillY = s.grid.railY.Follow(float32(targetY), env.DT())
		if s.grid.railY.Moving() {
			env.Keep()
		}
	}
	if sidebarOpen {
		pillRect := image.Rect(x, int(pillY+0.5), x+btnW, int(pillY+0.5)+btnW)
		paint.FillShape(gtx.Ops, env.Th.Chrome().SelectedWash, clip.UniformRRect(pillRect, gtx.Dp(theme.RadiusSM)).Op(gtx.Ops))
	}

	y := gap
	for _, it := range items {
		btn := s.grid.railBtn(it.item)
		// Tie Toggled to the user's persisted SidebarCollapsed state so a
		// width-driven TempSidebarFolded snap never starts an icon fade.
		toggled := s.sidebarVisible() && s.grid.rail == it.item
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.hint, Variant: kit.IconButtonGhost, Size: kit.SizeSM,
			Toggled: toggled, NoSelectedFill: true,
		}
		var err error
		env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			_, err = btn.Layout(env, gtx, &props)
		})
		s.fail(err)
		y += btnW + gap
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// layoutStatusBar draws the full-width 22 dp Bottom Status Bar (FUG-95) on
// AppBg: left breadcrumb (workspace > session or view title), right Lock vault
// action (centered with a full 24 dp HitMin target) and host connection status
// dot/tooltip/"Reconnecting…".
func (s *screens) layoutStatusBar(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w, h := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	padX := gtx.Dp(theme.SpaceMD)
	state, reason := s.m.State()
	if s.hostState != "" {
		state, reason = s.hostState, s.hostReason
	}
	hasConn := state != ""
	showLock := !s.vault.Locked() && s.vaultWorker != nil

	// Pre-measure right cluster to position elements right-aligned and bound the breadcrumb.
	rightStartX := max(w-padX, 0)
	dotD := max(gtx.Dp(theme.SpaceSM), 6)
	var reconnCall op.CallOp
	var reconnDims layout.Dimensions
	if hasConn {
		if state == hostconn.StateConnecting {
			measure := *gtx
			measure.Constraints.Min = image.Point{}
			m := op.Record(measure.Ops)
			reconnDims = kit.Label(env, &measure, theme.TypeCaption(), env.Th.Chrome().FgMuted, "Reconnecting…")
			reconnCall = m.Stop()
			rightStartX -= reconnDims.Size.X + gtx.Dp(theme.SpaceXS)
		}
		rightStartX -= dotD
		dotX := rightStartX
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
			labelX := dotX + dotD + gtx.Dp(theme.SpaceXS)
			labelY := (h - reconnDims.Size.Y) / 2
			off := op.Offset(image.Pt(labelX, labelY)).Push(gtx.Ops)
			reconnCall.Add(gtx.Ops)
			off.Pop()
		}
	}

	if showLock {
		lockProps := kit.IconButtonProps{Icon: kit.IconLock, Label: labelLockVault, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
		lockSize := s.vaultGate.lockBtn.Measure(env, gtx, &lockProps).Size
		if hasConn {
			rightStartX -= gtx.Dp(theme.SpaceMD)
		}
		lockX := max(rightStartX-lockSize.X, 0)
		lockY := (h - lockSize.Y) / 2
		env.Offset(gtx, image.Pt(lockX, lockY), func(gtx *layout.Context) {
			local := *gtx
			s.vaultGate.layoutLockButton(&local, env, s)
		})
		rightStartX = lockX
	}

	if crumb := s.statusBarBreadcrumb(); crumb != "" {
		maxCrumbW := max(rightStartX-padX-gtx.Dp(theme.SpaceMD), 0)
		lineH := gtx.Sp(theme.TypeCaption().Line)
		if maxCrumbW > 0 {
			env.Offset(gtx, image.Pt(padX, (h-lineH)/2), func(gtx *layout.Context) {
				local := *gtx
				local.Constraints = layout.Constraints{Max: image.Pt(maxCrumbW, h)}
				defer clip.Rect{Max: image.Pt(maxCrumbW, lineH)}.Push(local.Ops).Pop()
				kit.Label(env, &local, theme.TypeCaption(), env.Th.Chrome().FgMuted, crumb)
			})
		}
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
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

// railBtn returns the rail item's button state, building it on first use.
func (g *slotGrid) railBtn(item railItem) *kit.IconButtonState {
	btn, ok := g.railBtns[item]
	if !ok {
		btn = kit.NewIconButtonState()
		g.railBtns[item] = btn
	}
	return btn
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

// layoutRightStripe draws the permanent 30 dp Right Stripe (FUG-94) on AppBg:
// companion tool-window toggles (Approvals, Accounts, Secrets) at the top, and
// folded slot tabs at the bottom when narrow-window columns fold.
func (s *screens) layoutRightStripe(gtx *layout.Context, env *kit.Env, plan PlanFoldResult) layout.Dimensions {
	w := gtx.Dp(theme.RightStripeWidth)
	h := gtx.Constraints.Max.Y
	btnW := gtx.Dp(theme.ControlSM)
	x := max((w-btnW)/2, 0)
	gap := gtx.Dp(theme.SpaceXS)

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

	items := []struct {
		kind    SlotKind
		icon    kit.IconName
		tip     string
		warnDot bool
	}{
		{KindInbox, kit.IconInbox, inboxTip, s.inbox.PendingCount() > 0},
		{KindAccounts, kit.IconCircleUser, accTip, accLabel != ""},
		{KindSecrets, secIcon, secTip, s.vault.Locked()},
	}

	y := gap
	for _, it := range items {
		btn := s.grid.stripeBtn(it.kind)
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.tip, Variant: kit.IconButtonGhost, Size: kit.SizeSM,
			Toggled: s.hasSlotKind(it.kind),
		}
		var clicked bool
		var err error
		env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			clicked, err = btn.Layout(env, gtx, &props)
			if it.warnDot {
				drawStatusBadgeDot(gtx, env, btnW, env.Th.Status().Warning)
			}
		})
		s.fail(err)
		if clicked {
			s.toggleCompanion(it.kind)
		}
		y += btnW + gap
	}

	if plan.FoldedCols > 0 {
		folded := s.grid.foldedCells(plan)
		if len(folded) > 0 {
			groupH := len(folded)*btnW + max(len(folded)-1, 0)*gap
			startY := max(h-gap-groupH, y)
			if startY > y+gap {
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
				var clicked bool
				var err error
				env.Offset(gtx, image.Pt(x, curY), func(gtx *layout.Context) {
					clicked, err = btn.Layout(env, gtx, &props)
				})
				s.fail(err)
				if clicked {
					if s.grid.drawer == p.key() {
						s.grid.drawerPresence.Show(false)
					} else {
						s.grid.drawer = p.key()
						s.grid.drawerPresence.Show(true)
					}
				}
				curY += btnW + gap
			}
		}
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
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

// pxToDp converts a pixel width to dp at the frame's metric.
func pxToDp(gtx *layout.Context, px int) int {
	if gtx.Metric.PxPerDp <= 0 {
		return px
	}
	return int(float32(px)/gtx.Metric.PxPerDp + 0.5)
}
