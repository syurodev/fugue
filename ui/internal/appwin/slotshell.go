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
	sidebarOpen := s.sidebarVisible()
	if plan.TempSidebarFolded {
		sidebarOpen = false
	}
	totalW, totalH := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	railW := min(gtx.Dp(theme.RailWidth), totalW)
	footerH := gtx.Dp(theme.ControlLG)
	if !sidebarOpen && !s.vault.Locked() && s.vaultWorker != nil {
		footerH += gtx.Dp(theme.ControlSM) + gtx.Dp(theme.SpaceSM) + gtx.Dp(theme.SpaceXS)
	}
	colH := max(totalH-footerH, 0)
	gutter := gtx.Dp(theme.SpaceXS)
	sideW := 0
	if sidebarOpen {
		sideW = min(max(totalW-railW-gutter, 0), gtx.Dp(theme.SidebarWidth))
	}
	railGtx := *gtx
	railGtx.Constraints = layout.Constraints{Min: image.Pt(railW, 0), Max: image.Pt(railW, colH)}
	s.layoutRail(&railGtx, env)
	if sidebarOpen && sideW > 0 {
		env.Offset(gtx, image.Pt(railW, 0), func(gtx *layout.Context) {
			local := *gtx
			local.Constraints = layout.Constraints{Min: image.Pt(sideW, 0), Max: image.Pt(sideW, colH)}
			s.layoutSidebarPanel(&local, env)
		})
	}
	s.layoutShellFooter(gtx, env, sidebarOpen, railW, sideW, colH, footerH)
	gridX := railW + sideW
	if sidebarOpen && sideW > 0 {
		gridX += gutter
	}
	gridW := max(totalW-gridX, 0)
	env.Offset(gtx, image.Pt(gridX, 0), func(gtx *layout.Context) {
		local := *gtx
		local.Constraints = layout.Constraints{Min: image.Pt(gridW, 0), Max: image.Pt(gridW, totalH)}
		s.layoutGridContainer(&local, env, plan)
	})
	return layout.Dimensions{Size: image.Pt(totalW, totalH)}
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
// icons centered in the rail column with kit Tooltips and SelectedWash for
// the active item; the rail never folds.
func (s *screens) layoutRail(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w := gtx.Dp(theme.RailWidth)
	h := gtx.Constraints.Max.Y
	btnW := gtx.Dp(theme.ControlLG)
	x := max((w-btnW)/2, 0)
	items := []struct {
		item railItem
		icon kit.IconName
		hint string
	}{
		{railChat, kit.IconMessageSquare, "Chat"},
		{railTeam, kit.IconUsers, teamLabel},
		{railModules, kit.IconWrench, "Modules"},
	}
	y := gtx.Dp(theme.SpaceXS)
	for _, it := range items {
		btn := s.grid.railBtn(it.item)
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.hint, Variant: kit.IconButtonGhost, Size: kit.SizeLG,
			Toggled: s.grid.rail == it.item,
		}
		var clicked bool
		var err error
		env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			clicked, err = btn.Layout(env, gtx, &props)
		})
		s.fail(err)
		if clicked {
			s.grid.selectRail(it.item)
		}
		y += btnW + gtx.Dp(theme.SpaceXS)
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// layoutShellFooter draws the bottom-left footer row: the sidebar collapse
// chevron in the rail column, the Lock vault IconButton aligned to the
// sidebar icon column when unlocked, and a small connection status dot with
// a tooltip (no raw text while connected; "Reconnecting…" while connecting).
// When the sidebar is collapsed, Lock vault and the connection dot stack in
// the rail column above the collapse chevron.
func (s *screens) layoutShellFooter(gtx *layout.Context, env *kit.Env, sidebarOpen bool, railW, sideW, y, h int) {
	toggle := s.grid.sidebarBtn
	icon := kit.IconChevronLeft
	hint := "Collapse sidebar"
	if !sidebarOpen {
		icon = kit.IconChevronRight
		hint = "Open sidebar"
	}
	toggleProps := kit.IconButtonProps{Icon: icon, Label: hint, Variant: kit.IconButtonBordered, Size: kit.SizeSM}
	toggleSize := toggle.Measure(env, gtx, &toggleProps).Size
	if !sidebarOpen || sideW <= 0 {
		s.layoutCollapsedFooter(gtx, env, toggle, &toggleProps, railW, y, h)
		return
	}
	togglePos := image.Pt(max((railW-toggleSize.X)/2, 0), y+max((h-toggleSize.Y)/2, 0))
	var clicked bool
	var err error
	env.Offset(gtx, togglePos, func(gtx *layout.Context) {
		clicked, err = toggle.Layout(env, gtx, &toggleProps)
	})
	s.fail(err)
	if clicked {
		doc := cloneLayout(s.grid.doc)
		doc.SidebarCollapsed = sidebarOpen
		s.grid.edited(doc)
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

func (s *screens) layoutCollapsedFooter(gtx *layout.Context, env *kit.Env, toggle *kit.IconButtonState, toggleProps *kit.IconButtonProps, railW, y, h int) {
	toggleSize := toggle.Measure(env, gtx, toggleProps).Size
	curY := y + h - toggleSize.Y - gtx.Dp(theme.SpaceXXS)
	env.Offset(gtx, image.Pt(max((railW-toggleSize.X)/2, 0), curY), func(gtx *layout.Context) {
		clicked, err := toggle.Layout(env, gtx, toggleProps)
		s.fail(err)
		if clicked {
			doc := cloneLayout(s.grid.doc)
			doc.SidebarCollapsed = false
			s.grid.edited(doc)
		}
	})
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
