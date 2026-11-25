package appwin

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/kit"
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
	sidebarOpen := s.pollSidebarToggle(gtx, plan.TempSidebarFolded)
	openProg := s.stepSidebarOpen(env, sidebarOpen, plan.TempSidebarFolded)

	totalW, totalH := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	gutter := gtx.Dp(theme.SpaceXS)
	maxSideW := min(max(totalW-railW-gutter, 0), gtx.Dp(theme.SidebarWidth))
	sideW := scaledPx(maxSideW, openProg)

	showLock := !s.vault.Locked() && s.vaultWorker != nil
	footerH := shellFooterHeight(gtx, showLock, openProg)
	colH := max(totalH-footerH, 0)
	railGtx := *gtx
	railGtx.Constraints = layout.Constraints{Min: image.Pt(railW, 0), Max: image.Pt(railW, colH)}
	s.layoutRail(&railGtx, env)
	s.layoutAnimatedSidebar(gtx, env, railW, maxSideW, sideW, colH, openProg)
	s.layoutShellFooter(gtx, env, sidebarOpen, railW, sideW, colH, footerH, openProg)

	gridX := railW + sideW + scaledPx(gutter, openProg)
	gridW := max(totalW-gridX, 0)
	env.Offset(gtx, image.Pt(gridX, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(gridW, 0), Max: image.Pt(gridW, totalH)}
		s.layoutGridContainer(&local, env, plan)
	})
	return layout.Dimensions{Size: image.Pt(totalW, totalH)}
}

func shellFooterHeight(gtx *layout.Context, showLock bool, openProg float32) int {
	h := gtx.Dp(theme.ControlLG)
	if showLock {
		extra := gtx.Dp(theme.ControlSM) + gtx.Dp(theme.SpaceSM) + gtx.Dp(theme.SpaceXS)
		h += scaledPx(extra, 1-openProg)
	}
	return h
}

func (s *screens) pollSidebarToggle(gtx *layout.Context, tempFolded bool) bool {
	open := s.sidebarVisible() && !tempFolded
	if s.grid.sidebarBtn.PollClick(gtx, false, false) {
		doc := cloneLayout(s.grid.doc)
		doc.SidebarCollapsed = open
		s.grid.edited(doc)
		open = s.sidebarVisible() && !tempFolded
	}
	return open
}

func (s *screens) stepSidebarOpen(env *kit.Env, sidebarOpen, tempFolded bool) float32 {
	target := float32(0)
	if sidebarOpen {
		target = 1
	}
	var prog float32
	if env.ReducedMotion || tempFolded || s.grid.wasTempFolded != tempFolded {
		s.grid.sidebarOpenF.Snap(target)
		prog = target
	} else {
		prog = s.grid.sidebarOpenF.Follow(target, env.DT())
		if s.grid.sidebarOpenF.Moving() {
			env.Keep()
		}
	}
	s.grid.wasTempFolded = tempFolded
	return prog
}

func (s *screens) layoutAnimatedSidebar(gtx *layout.Context, env *kit.Env, railW, maxSideW, sideW, colH int, openProg float32) {
	if sideW <= 0 || openProg <= 0 {
		return
	}
	env.Offset(gtx, image.Pt(railW, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(maxSideW, 0), Max: image.Pt(maxSideW, colH)}
		if openProg < 1 {
			defer clip.Rect{Max: image.Pt(sideW, colH)}.Push(local.Ops).Pop()
			defer paint.PushOpacity(local.Ops, min(max(openProg, 0), 1)).Pop()
		}
		s.layoutSidebarPanel(&local, env)
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

// layoutGridContainer frames the whole slot grid as one rounded panel: the
// darker AppBg frames it, the panel fills with the lighter CanvasBg behind a
// hairline BorderSubtle outline, and every boundary inside stays a hairline
// SplitPane divider (§5.3, ui-kit §6.27).
func (s *screens) layoutGridContainer(gtx *layout.Context, env *kit.Env, plan PlanFoldResult) layout.Dimensions {
	local := *gtx
	gridRect := image.Rectangle{Max: local.Constraints.Max}
	if gridRect.Empty() {
		return layout.Dimensions{}
	}
	radius := gtx.Dp(theme.RadiusMD)
	panel := clip.UniformRRect(gridRect, radius)
	paint.FillShape(local.Ops, env.Th.Chrome().CanvasBg, panel.Op(local.Ops))
	area := gridRect
	local.Constraints = layout.Constraints{Min: image.Pt(0, 0), Max: gridRect.Size()}
	stack := panel.Push(local.Ops)
	s.grid.layoutArea(&local, env, s, area, plan)
	stack.Pop()
	// The crisp outline rides on top of the cells' edges.
	paint.FillShape(local.Ops, env.Th.Chrome().BorderSubtle,
		clip.Stroke{Path: panel.Path(local.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1))}.Op())
	return layout.Dimensions{Size: gridRect.Size()}
}

// layoutRail draws the rail (§5.3) on the shell's AppBg: the rail items'
// icons centered in the rail column with kit Tooltips and a Smooth sliding
// SelectedWash pill for the active item; the rail never folds.
func (s *screens) layoutRail(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w := gtx.Dp(theme.RailWidth)
	h := gtx.Constraints.Max.Y
	btnW := gtx.Dp(theme.ControlLG)
	x := max((w-btnW)/2, 0)
	gap := gtx.Dp(theme.SpaceXS)
	items := []struct {
		item railItem
		icon kit.IconName
		hint string
	}{
		{railChat, kit.IconMessageSquare, "Chat"},
		{railTeam, kit.IconUsers, teamLabel},
		{railModules, kit.IconWrench, "Modules"},
	}
	// Process rail clicks before building Toggled and stepping the indicator so
	// the highlight follows the click in the same frame.
	for _, it := range items {
		if s.grid.railBtn(it.item).PollClick(gtx, false, false) {
			s.grid.selectRail(it.item)
		}
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
	pillRect := image.Rect(x, int(pillY+0.5), x+btnW, int(pillY+0.5)+btnW)
	paint.FillShape(gtx.Ops, env.Th.Chrome().SelectedWash, clip.UniformRRect(pillRect, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops))

	y := gap
	for _, it := range items {
		btn := s.grid.railBtn(it.item)
		toggled := s.grid.rail == it.item
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.hint, Variant: kit.IconButtonGhost, Size: kit.SizeLG,
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

// layoutShellFooter draws the bottom-left footer row: the sidebar collapse
// chevron in the rail column, the Lock vault IconButton aligned to the
// sidebar icon column when unlocked, and a small connection status dot with
// a tooltip (no raw text while connected; "Reconnecting…" while connecting).
// When the sidebar is collapsed, Lock vault and the connection dot stack in
// the rail column above the collapse chevron.
func (s *screens) layoutShellFooter(gtx *layout.Context, env *kit.Env, sidebarOpen bool, railW, sideW, y, h int, openProg float32) {
	toggle := s.grid.sidebarBtn
	icon := kit.IconChevronLeft
	hint := "Collapse sidebar"
	if !sidebarOpen {
		icon = kit.IconChevronRight
		hint = "Open sidebar"
	}
	toggleProps := kit.IconButtonProps{Icon: icon, Label: hint, Variant: kit.IconButtonBordered, Size: kit.SizeSM}
	toggleSize := toggle.Measure(env, gtx, &toggleProps).Size
	closedY := y + h - toggleSize.Y - gtx.Dp(theme.SpaceXXS)
	openY := y + max((gtx.Dp(theme.ControlLG)-toggleSize.Y)/2, 0)
	toggleY := closedY + int(float32(openY-closedY)*min(max(openProg, 0), 1)+0.5)
	if openProg <= 0 {
		toggleY = closedY
	} else if openProg >= 1 {
		toggleY = openY
	}
	env.Offset(gtx, image.Pt(max((railW-toggleSize.X)/2, 0), toggleY), func(gtx *layout.Context) {
		_, err := toggle.Layout(env, gtx, &toggleProps)
		s.fail(err)
	})
	if openProg < 0.5 {
		defer clip.Rect(image.Rect(0, y, railW, y+h)).Push(gtx.Ops).Pop()
		if openProg > 0 {
			defer paint.PushOpacity(gtx.Ops, 1-2*openProg).Pop()
		}
		s.layoutCollapsedFooterItems(gtx, env, railW, toggleY)
		return
	}
	if openProg < 1 {
		defer clip.Rect(image.Rect(railW, y, railW+sideW, y+h)).Push(gtx.Ops).Pop()
		defer paint.PushOpacity(gtx.Ops, 2*(openProg-0.5)).Pop()
	}
	// Align Lock vault to the sidebar icon column (SpaceXS pill inset + SpaceMD icon inset).
	lockProps := kit.IconButtonProps{Icon: kit.IconLock, Label: labelLockVault, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
	lockSize := s.vaultGate.lockBtn.Measure(env, gtx, &lockProps).Size
	iconColX := railW + gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.SpaceMD) - max((lockSize.X-gtx.Dp(theme.IconMD))/2, 0)
	lockY := y + max((h-lockSize.Y)/2, 0)
	env.Offset(gtx, image.Pt(iconColX, lockY), func(gtx *layout.Context) {
		local := *gtx
		s.vaultGate.layoutLockButton(&local, env, s)
	})
	dotX := iconColX + lockSize.X + gtx.Dp(theme.SpaceSM)
	if s.vault.Locked() || s.vaultWorker == nil {
		dotX = railW + gtx.Dp(theme.SpaceXS) + gtx.Dp(theme.SpaceMD)
	}
	s.layoutConnectionDot(gtx, env, image.Pt(dotX, y+h/2), true)
}

func (s *screens) layoutCollapsedFooterItems(gtx *layout.Context, env *kit.Env, railW, curY int) {
	if !s.vault.Locked() && s.vaultWorker != nil {
		lockProps := kit.IconButtonProps{Icon: kit.IconLock, Label: labelLockVault, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
		lockSize := s.vaultGate.lockBtn.Measure(env, gtx, &lockProps).Size
		curY -= lockSize.Y + gtx.Dp(theme.SpaceXS)
		env.Offset(gtx, image.Pt(max((railW-lockSize.X)/2, 0), curY), func(gtx *layout.Context) {
			local := *gtx
			s.vaultGate.layoutLockButton(&local, env, s)
		})
	}
	dotD := max(gtx.Dp(theme.SpaceSM), 4)
	curY -= dotD + gtx.Dp(theme.SpaceXS)
	s.layoutConnectionDot(gtx, env, image.Pt(max((railW-dotD)/2, 0), curY+dotD/2), false)
}

func (s *screens) layoutConnectionDot(gtx *layout.Context, env *kit.Env, center image.Point, showLabel bool) {
	state, reason := s.m.State()
	if s.hostState != "" {
		state, reason = s.hostState, s.hostReason
	}
	if state == "" {
		return
	}
	dotD := max(gtx.Dp(theme.SpaceSM), 4)
	r := image.Rect(0, 0, dotD, dotD)
	pos := image.Pt(center.X, center.Y-dotD/2)
	tip := "Host connected"
	col := env.Th.Chrome().FgSubtle
	if state != hostconn.StateConnected {
		tip = statusLine(state, reason)
		col = statusColor(env.Th, state)
	}
	env.Offset(gtx, pos, func(gtx *layout.Context) {
		paint.FillShape(gtx.Ops, col, clip.UniformRRect(r, dotD/2).Op(gtx.Ops))
		area := clip.Rect(r).Push(gtx.Ops)
		event.Op(gtx.Ops, &s.connDotTag)
		s.pollConnDotEvents(gtx)
		area.Pop()
		env.TooltipAnchor(&s.connDotTag, r, s.connDotHovered, false, s.connDotHasPtr, s.connDotPtr, kit.TooltipProps{Text: tip})
	})
	if showLabel && state == hostconn.StateConnecting {
		labelX := center.X + dotD + gtx.Dp(theme.SpaceXS)
		labelY := center.Y - gtx.Sp(theme.TypeCaption().Line)/2
		env.Offset(gtx, image.Pt(labelX, labelY), func(gtx *layout.Context) {
			local := *gtx
			local.Constraints.Min = image.Point{}
			kit.Label(env, &local, theme.TypeCaption(), env.Th.Chrome().FgMuted, "Reconnecting…")
		})
	}
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

// pxToDp converts a pixel width to dp at the frame's metric.
func pxToDp(gtx *layout.Context, px int) int {
	if gtx.Metric.PxPerDp <= 0 {
		return px
	}
	return int(float32(px)/gtx.Metric.PxPerDp + 0.5)
}
