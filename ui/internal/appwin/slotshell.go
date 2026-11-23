package appwin

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

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
	layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.failure.layout(env, &local, s)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutShellBody(&local, env)
		}),
	)
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
	railW := gtx.Dp(theme.RailWidth)
	sidebarOpen := s.sidebarVisible()
	gridW := gtx.Constraints.Max.X - railW
	if sidebarOpen {
		gridW -= gtx.Dp(theme.SidebarWidth)
	}
	plan := PlanFold(pxToDp(gtx, gridW), sidebarOpen, len(s.grid.doc.Columns))
	if plan.TempSidebarFolded {
		sidebarOpen = false
	}
	return layout.Flex{Axis: layout.Horizontal}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutRail(&local, env, sidebarOpen)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			if !sidebarOpen {
				// Collapsed, the sidebar leaves only the rail (§5.3); the
				// rail's bottom toggle reopens it.
				return layout.Dimensions{}
			}
			return layout.Inset{Right: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				local.Constraints.Max.X = min(local.Constraints.Max.X, gtx.Dp(theme.SidebarWidth))
				return s.layoutSidebarPanel(&local, env)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutGridContainer(&local, env, plan)
		}),
	)
}

// sidebarVisible is the sidebar's user state: the persisted collapsed flag.
func (s *screens) sidebarVisible() bool { return !s.grid.doc.SidebarCollapsed }

// layoutGridContainer frames the whole slot grid as one rounded panel: the
// darker AppBg frames it, the panel fills with the lighter CanvasBg behind a
// hairline BorderSubtle outline, and every boundary inside stays a hairline
// SplitPane divider (§5.3, ui-kit §6.27).
func (s *screens) layoutGridContainer(gtx *layout.Context, env *kit.Env, plan PlanFoldResult) layout.Dimensions {
	inset := layout.Inset{
		Left:   theme.SpaceXS / 2,
		Top:    theme.SpaceXS,
		Right:  theme.SpaceXS,
		Bottom: theme.SpaceXS,
	}
	return inset.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		local := gtx
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
	})
}

// layoutRail draws the rail (§5.3) on the shell's AppBg: the rail items'
// icons centered in the rail column, the sidebar toggle at the bottom —
// collapse points left when the sidebar is open, right when it is not; the
// rail never folds.
func (s *screens) layoutRail(gtx *layout.Context, env *kit.Env, sidebarOpen bool) layout.Dimensions {
	w := gtx.Dp(theme.RailWidth)
	local := *gtx
	h := local.Constraints.Max.Y
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, h)}
	gtx = &local
	btnW := gtx.Dp(theme.ControlMD)
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
	y := gtx.Dp(theme.SpaceSM)
	for _, it := range items {
		btn := s.grid.railBtn(it.item)
		props := kit.IconButtonProps{
			Icon: it.icon, Label: it.hint, Variant: kit.IconButtonGhost, Size: kit.SizeMD,
			Toggled: s.grid.rail == it.item,
		}
		var clicked bool
		var err error
		atPoint(gtx, image.Pt(x, y), func(gtx *layout.Context) {
			clicked, err = btn.Layout(env, gtx, &props)
		})
		s.fail(err)
		if clicked {
			s.grid.selectRail(it.item)
		}
		y += btnW + gtx.Dp(theme.SpaceXS)
	}
	// The sidebar's toggle lives at the bottom of the rail itself, so a
	// collapsed sidebar leaves only the rail (§5.3).
	toggle := s.grid.sidebarBtn
	icon := kit.IconChevronLeft
	hint := "Collapse sidebar"
	if !sidebarOpen {
		icon = kit.IconChevronRight
		hint = "Open sidebar"
	}
	props := kit.IconButtonProps{Icon: icon, Label: hint, Variant: kit.IconButtonBordered, Size: kit.SizeSM}
	toggleSize := toggle.Measure(env, gtx, &props).Size
	var clicked bool
	var err error
	atPoint(gtx, image.Pt(max((w-toggleSize.X)/2, 0), max(h-toggleSize.Y-gtx.Dp(theme.SpaceSM), y)), func(gtx *layout.Context) {
		clicked, err = toggle.Layout(env, gtx, &props)
	})
	s.fail(err)
	if clicked {
		doc := cloneLayout(s.grid.doc)
		doc.SidebarCollapsed = sidebarOpen
		s.grid.edited(doc)
	}
	return layout.Dimensions{Size: image.Pt(w, h)}
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
