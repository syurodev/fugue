package appwin

import (
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

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
			s.handleRailClick(item)
		}
	}
	return s.sidebarVisible() && !tempFolded
}

func (s *screens) handleRailClick(item railItem) {
	if s.grid.rail == item {
		doc := cloneLayout(s.grid.doc)
		doc.SidebarCollapsed = s.sidebarVisible()
		s.grid.edited(doc)
		return
	}
	s.grid.selectRail(item)
	if !s.sidebarVisible() {
		doc := cloneLayout(s.grid.doc)
		doc.SidebarCollapsed = false
		s.grid.edited(doc)
	}
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
