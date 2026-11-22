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

// The app shell (desktop-app §5.3, DA-10): the rail on the left — chat,
// team, then the registered modules in install order, never folding — the
// per-rail sidebar that can collapse to just the rail, and the slot grid
// taking the rest. A narrow window folds regular columns into the edge tab
// strip first and the sidebar second; the main slot keeps its minimum.

// layoutShell draws the failure strip, then the rail, the sidebar and the
// grid (F10.5e keeps the strip above the panes).
func (s *screens) layoutShell(gtx *layout.Context, env *kit.Env) {
	if s.vaultGate.active(s.vault) {
		s.vaultGate.layout(gtx, env, s)
		return
	}
	s.layoutShortcuts(gtx)
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

// layoutShellBody lays out rail, sidebar and grid; a width-driven sidebar
// fold removes the sidebar before the grid measures.
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
			return layout.Inset{Right: theme.SpaceXS}.Layout(local, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return s.layoutRail(&local, env)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return s.layoutSidebarOrRail(&local, env, sidebarOpen)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			area := image.Rectangle{Max: local.Constraints.Max}
			s.grid.layoutArea(&local, env, s, area, plan)
			return layout.Dimensions{Size: local.Constraints.Max}
		}),
	)
}

// sidebarVisible is the sidebar's user state: the persisted collapsed flag.
func (s *screens) sidebarVisible() bool { return !s.grid.doc.SidebarCollapsed }

// layoutSidebarOrRail draws the full sidebar, or — collapsed — the thin
// reopen strip next to the rail.
func (s *screens) layoutSidebarOrRail(gtx *layout.Context, env *kit.Env, open bool) layout.Dimensions {
	if open {
		return layout.Inset{Right: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			local.Constraints.Max.X = min(local.Constraints.Max.X, gtx.Dp(theme.SidebarWidth))
			return s.layoutSidebarPanel(&local, env)
		})
	}
	return s.layoutSidebarExpand(gtx, env)
}

// layoutSidebarExpand is the collapsed sidebar's strip: one button to
// reopen.
func (s *screens) layoutSidebarExpand(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	return layout.Flex{Axis: layout.Vertical, Alignment: layout.End}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			props := kit.IconButtonProps{Icon: kit.IconChevronRight, Label: "Open sidebar", Variant: kit.IconButtonGhost, Size: kit.SizeSM}
			clicked, err := s.grid.sidebarBtn.Layout(env, &local, &props)
			s.fail(err)
			if clicked {
				doc := cloneLayout(s.grid.doc)
				doc.SidebarCollapsed = false
				s.grid.edited(doc)
			}
			return s.grid.sidebarBtn.Measure(env, &local, &props)
		}),
	)
}

// layoutRail draws the rail (§5.3): one icon per rail item, the selected one
// washed; the rail never folds.
func (s *screens) layoutRail(gtx *layout.Context, env *kit.Env) layout.Dimensions {
	w := gtx.Dp(theme.RailWidth)
	local := *gtx
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Constraints.Max.Y)}
	gtx = &local
	paint.FillShape(gtx.Ops, env.Th.Chrome().SurfaceBase, clip.Rect{Max: image.Pt(w, gtx.Constraints.Max.Y)}.Op())
	items := []struct {
		item railItem
		icon kit.IconName
		hint string
	}{
		{railChat, kit.IconMessageSquare, "Chat"},
		{railTeam, kit.IconUsers, "Team"},
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
		atPoint(gtx, image.Pt(0, y), func(gtx *layout.Context) {
			clicked, err = btn.Layout(env, gtx, &props)
		})
		s.fail(err)
		if clicked {
			s.grid.selectRail(it.item)
		}
		y += gtx.Dp(theme.ControlMD) + gtx.Dp(theme.SpaceXS)
	}
	return layout.Dimensions{Size: image.Pt(w, gtx.Constraints.Max.Y)}
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
