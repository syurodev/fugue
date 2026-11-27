package appwin

import (
	"image"

	"gioui.org/app"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Window chrome (FUG-58, FUG-95). On all desktop platforms, Fugue renders a
// unified 32 dp Top Bar on AppBg carrying the active context pill, window drag
// area, Quick Theme Toggle, and Settings button. On Linux/BSD compositors that
// grant no server-side decorations (Config.Decorated == false), windowOptions
// opts out of Gio's fallback Material title bar and appends CSD Minimize /
// Maximize / Close controls on the far right.

// drawsOwnChrome reports whether goos is a platform whose window chrome may
// need to be drawn by the app: the X11 and Wayland systems.
func drawsOwnChrome(goos string) bool {
	switch goos {
	case "linux", "freebsd", "openbsd", "netbsd":
		return true
	default:
		return false
	}
}

// windowOptions are the platform-specific options of a new window. Only the
// Linux family opts out of Gio's fallback decorations.
func windowOptions(goos string) []app.Option {
	if !drawsOwnChrome(goos) {
		return nil
	}
	return []app.Option{app.Decorated(false)}
}

// titleBar is the app-drawn Top Bar (FUG-70, FUG-95, FUG-96) and optional Linux CSD chrome.
type titleBar struct {
	goos           string
	deco           widget.Decorations
	click          gesture.Click
	csd            bool
	fullscreen     bool
	focused        bool
	maximized      bool
	nativeView     uintptr
	platform       macTitleBarPlatform
	mac            macMetricsState
	invalidate     func()
	lastDragRectDp image.Rectangle
	title          string
	pending        system.Action
	themeBtn       *kit.IconButtonState
	settingsBtn    *kit.IconButtonState
}

func newTitleBar(goos string) *titleBar {
	return &titleBar{
		goos:        goos,
		focused:     true,
		platform:    defaultMacTitleBarPlatform(),
		themeBtn:    kit.NewIconButtonState(),
		settingsBtn: kit.NewIconButtonState(),
	}
}

// applyConfig follows the window's platform state (app.ConfigEvent).
func (b *titleBar) applyConfig(cfg app.Config) {
	if b == nil {
		return
	}
	b.fullscreen = cfg.Mode == app.Fullscreen
	b.csd = drawsOwnChrome(b.goos) && !cfg.Decorated && !b.fullscreen
	b.focused = cfg.Focused
	b.maximized = cfg.Mode == app.Maximized
	b.deco.Maximized = b.maximized
	b.title = cfg.Title
}

// applyViewEvent records the native view handle from app.ViewEvent and applies
// initial platform titlebar styling, clearing any stale handle when the event
// does not carry a valid native view.
func (b *titleBar) applyViewEvent(e event.Event) {
	if b == nil {
		return
	}
	ptr, ok := extractPlatformView(e)
	if !ok {
		b.nativeView = 0
		b.lastDragRectDp = image.Rectangle{}
		return
	}
	b.nativeView = ptr
	b.applyPlatformChrome()
}

// reapplyPlatformChrome restores platform titlebar styling after Gio window
// option updates (which reset macOS fullSizeContentView on v0.10.3).
func (b *titleBar) reapplyPlatformChrome() {
	if b == nil {
		return
	}
	b.applyPlatformChrome()
}

func (b *titleBar) applyPlatformChrome() {
	if b.nativeView == 0 || b.platform == nil {
		return
	}
	b.platform.applyTitleBar(b.nativeView, b.fullscreen, b.setMacMetrics)
}

func (b *titleBar) hasDragArea() bool {
	return b.csd || (b.goos == osDarwin && !b.fullscreen)
}

// takeActions returns the window actions the user asked for and clears them.
func (b *titleBar) takeActions() system.Action {
	if b == nil {
		return 0
	}
	a := b.pending
	b.pending = 0
	return a
}

// height is the bar's height in px, zero when nil.
func (b *titleBar) height(gtx *layout.Context) int {
	if b == nil {
		return 0
	}
	return b.macBarHeight(gtx)
}

func (b *titleBar) ensureButtons() {
	if b.themeBtn == nil {
		b.themeBtn = kit.NewIconButtonState()
	}
	if b.settingsBtn == nil {
		b.settingsBtn = kit.NewIconButtonState()
	}
}

func (b *titleBar) leftInset(gtx *layout.Context, hasSettings bool) int {
	if b.goos == osDarwin && !b.fullscreen {
		return b.macLeftInset(gtx)
	}
	if hasSettings {
		return gtx.Dp(theme.RailWidth)
	}
	return gtx.Dp(theme.SpaceMD)
}

type titleBarMetrics struct {
	btnW         int
	btnGap       int
	csdW         int
	hasTheme     bool
	hasSettings  bool
	rightPad     int
	rightBtnsW   int
	rightEdgeGap int
	rightTotalW  int
}

func csdButtonMetrics(gtx *layout.Context) (btnSide, gap, totalW int) {
	btnSide = gtx.Dp(theme.ControlSM)
	gap = gtx.Dp(theme.SpaceXS)
	padR := gtx.Dp(theme.SpaceMD)
	totalW = 3*btnSide + 2*gap + padR
	return btnSide, gap, totalW
}

func (b *titleBar) computeMetrics(gtx *layout.Context, sc *screens) titleBarMetrics {
	btnW := gtx.Dp(theme.ControlSM)
	btnGap := gtx.Dp(theme.SpaceXS)
	csdW := 0
	if b.csd {
		_, _, csdW = csdButtonMetrics(gtx)
	}
	hasTheme := sc != nil
	hasSettings := sc != nil && !sc.vaultGate.active(sc.vault)
	rightPad := 0
	if !b.csd && (hasTheme || hasSettings) {
		rightPad = max((gtx.Dp(theme.RightStripeWidth)-btnW)/2, 0)
	}
	rightBtnsW := 0
	if hasTheme {
		rightBtnsW += btnW
	}
	if hasSettings {
		if rightBtnsW > 0 {
			rightBtnsW += btnGap
		}
		rightBtnsW += btnW
	}
	rightEdgeGap := 0
	if b.csd && rightBtnsW > 0 {
		rightEdgeGap = btnGap
	}
	return titleBarMetrics{
		btnW:         btnW,
		btnGap:       btnGap,
		csdW:         csdW,
		hasTheme:     hasTheme,
		hasSettings:  hasSettings,
		rightPad:     rightPad,
		rightBtnsW:   rightBtnsW,
		rightEdgeGap: rightEdgeGap,
		rightTotalW:  rightBtnsW + rightEdgeGap + csdW + rightPad,
	}
}

// layout draws the 32 dp Top Bar at the top of the window and returns its height.
func (b *titleBar) layout(env *kit.Env, gtx *layout.Context, sc *screens) int {
	h := b.height(gtx)
	if h == 0 {
		return 0
	}
	b.ensureButtons()
	if b.csd {
		b.pending |= b.deco.Update(*gtx)
	}
	c := env.Th.Chrome()
	width := gtx.Constraints.Max.X
	local := *gtx
	local.Constraints = layout.Exact(image.Pt(width, h))
	paint.FillShape(local.Ops, c.AppBg, clip.Rect{Max: local.Constraints.Max}.Op())

	m := b.computeMetrics(&local, sc)
	leftX := b.leftInset(&local, m.hasSettings)
	maxLeftW := max(width-m.rightTotalW-leftX-gtx.Dp(theme.SpaceMD), 0)
	pillW := b.layoutContextPill(env, &local, sc, leftX, h, maxLeftW)

	var dragArea image.Rectangle
	if b.hasDragArea() {
		dragStart := leftX + pillW
		dragEnd := max(width-m.rightTotalW, dragStart)
		dragArea = image.Rect(dragStart, 0, dragEnd, h)
		b.layoutDragArea(&local, dragArea)
	}
	b.syncPlatformDragRect(&local, dragArea)
	b.layoutRightCluster(env, &local, sc, width, h, m)
	return h
}

func (b *titleBar) layoutRightCluster(env *kit.Env, gtx *layout.Context, sc *screens, width, h int, m titleBarMetrics) {
	btnY := max((h-m.btnW)/2, 0)
	curX := width - m.rightPad - m.csdW - m.rightEdgeGap - m.rightBtnsW
	if m.hasTheme {
		b.layoutThemeToggleBtn(env, gtx, sc, curX, btnY)
		curX += m.btnW + m.btnGap
	}
	if m.hasSettings {
		b.layoutSettingsBtn(env, gtx, sc, curX, btnY, m.btnW)
	}
	if b.csd {
		off := op.Offset(image.Pt(width-m.csdW, 0)).Push(gtx.Ops)
		csdGtx := *gtx
		csdGtx.Constraints = layout.Exact(image.Pt(m.csdW, h))
		b.layoutButtons(env, &csdGtx, sc, h)
		off.Pop()
	}
}

func (b *titleBar) layoutThemeToggleBtn(env *kit.Env, gtx *layout.Context, sc *screens, x, y int) {
	isDark := env.Th.Chrome().CanvasBg.R < 128
	icon, tip := kit.IconMoon, "Switch to dark theme (Ctrl+D)"
	if isDark {
		icon, tip = kit.IconSun, "Switch to light theme (Ctrl+D)"
	}
	env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
		props := kit.IconButtonProps{Icon: icon, Label: tip, Variant: kit.IconButtonGhost, Size: kit.SizeSM}
		clicked, err := b.themeBtn.Layout(env, gtx, &props)
		sc.fail(err)
		if clicked {
			sc.toggleThemeFromUI(env, isDark)
		}
	})
}

func (b *titleBar) layoutSettingsBtn(env *kit.Env, gtx *layout.Context, sc *screens, x, y, btnW int) {
	dirty := len(sc.settings.DirtyFields()) > 0
	tip := "Settings"
	if dirty {
		tip = "Settings (unsaved changes)"
	}
	open := sc.hasSlotKind(KindSettings)
	env.Offset(gtx, image.Pt(x, y), func(gtx *layout.Context) {
		props := kit.IconButtonProps{Icon: kit.IconSettings, Label: tip, Variant: kit.IconButtonGhost, Size: kit.SizeSM, Toggled: open}
		clicked, err := b.settingsBtn.Layout(env, gtx, &props)
		sc.fail(err)
		if dirty {
			drawStatusBadgeDot(gtx, env, btnW, env.Th.Status().Warning)
		}
		if clicked {
			toggleSettingsSlot(sc, open)
		}
	})
}

func toggleSettingsSlot(sc *screens, open bool) {
	if open {
		sc.grid.closeSlotOf(KindSettings)
		return
	}
	sc.openSettings()
}

func (b *titleBar) layoutContextPill(env *kit.Env, gtx *layout.Context, sc *screens, x, h, maxW int) int {
	if maxW <= 0 {
		return 0
	}
	text := b.title
	if sc != nil && !sc.vaultGate.active(sc.vault) {
		text = sc.topBarContext()
	}
	if text == "" {
		text = appName
	}
	c := env.Th.Chrome()
	fg := c.FgMuted
	if b.focused {
		fg = c.Fg
	}
	padX := gtx.Dp(theme.SpaceSM)
	padY := gtx.Dp(theme.SpaceXXS)
	maxTextW := max(maxW-2*padX, 0)
	if maxTextW <= 0 {
		return 0
	}
	measure := *gtx
	measure.Constraints = layout.Constraints{Max: image.Pt(maxTextW, h)}
	m := op.Record(measure.Ops)
	dims := kit.Label(env, &measure, theme.TypeSmall(), fg, text)
	call := m.Stop()

	pillW := min(dims.Size.X+2*padX, maxW)
	pillH := min(dims.Size.Y+2*padY, h)
	pillY := max((h-pillH)/2, 0)
	pillRect := image.Rect(x, pillY, x+pillW, pillY+pillH)
	paint.FillShape(gtx.Ops, c.FillSoft, clip.UniformRRect(pillRect, gtx.Dp(theme.RadiusSM)).Op(gtx.Ops))

	textX := x + padX
	textY := pillY + max((pillH-dims.Size.Y)/2, 0)
	st := clip.Rect(image.Rect(textX, pillY, x+pillW-padX, pillY+pillH)).Push(gtx.Ops)
	off := op.Offset(image.Pt(textX, textY)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	st.Pop()
	return pillW
}

// layoutDragArea registers the middle window move and double-click action area.
func (b *titleBar) layoutDragArea(gtx *layout.Context, area image.Rectangle) {
	size := area.Size()
	if size.X <= 0 || size.Y <= 0 {
		return
	}
	hasDoubleClick := b.csd || (b.goos == osDarwin && !b.fullscreen)
	if hasDoubleClick {
		b.pollDragDoubleClick(gtx)
	}
	off := op.Offset(area.Min).Push(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Exact(size)
	b.deco.LayoutMove(local, func(g layout.Context) layout.Dimensions {
		if hasDoubleClick {
			st := clip.Rect{Max: size}.Push(g.Ops)
			b.click.Add(g.Ops)
			st.Pop()
		}
		return layout.Dimensions{Size: size}
	})
	off.Pop()
}

func (b *titleBar) pollDragDoubleClick(gtx *layout.Context) {
	for {
		ev, ok := b.click.Update(gtx.Source)
		if !ok {
			return
		}
		if ev.Kind == gesture.KindClick && ev.NumClicks == 2 {
			if b.goos == osDarwin && !b.fullscreen {
				b.pending |= b.darwinDoubleClickAction()
				continue
			}
			b.pending |= b.toggleMaximize()
		}
	}
}

func (b *titleBar) toggleMaximize() system.Action {
	if b.maximized {
		return system.ActionUnmaximize
	}
	return system.ActionMaximize
}

// layoutButtons lays out minimize, maximize or restore, and close, right
// aligned as 24×24 dp ghost buttons with 14 dp Lucide icons centered
// vertically in the 32 dp Top Bar (FUG-97). Hover and pressed states are the
// kit washes; close hovers on the danger tint, as the kit's danger button does.
func (b *titleBar) layoutButtons(env *kit.Env, gtx *layout.Context, sc *screens, h int) layout.Dimensions {
	btnSide, gap, totalW := csdButtonMetrics(gtx)
	y := max((h-btnSide)/2, 0)
	buttons := []struct {
		action system.Action
		icon   kit.IconName
	}{
		{system.ActionMinimize, kit.IconMinus},
		{system.ActionMaximize, kit.IconSquare},
		{system.ActionClose, kit.IconClose},
	}
	x := 0
	for _, btn := range buttons {
		icon := btn.icon
		if btn.action == system.ActionMaximize && b.maximized {
			icon = kit.IconCopy
		}
		off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
		b.layoutButton(env, gtx, sc, btn.action, icon, image.Pt(btnSide, btnSide))
		off.Pop()
		x += btnSide + gap
	}
	return layout.Dimensions{Size: image.Pt(totalW, h)}
}

func (b *titleBar) layoutButton(env *kit.Env, gtx *layout.Context, sc *screens, action system.Action, icon kit.IconName, size image.Point) {
	c := env.Th.Chrome()
	click := b.deco.Clickable(action)
	local := *gtx
	local.Constraints = layout.Exact(size)
	radius := gtx.Dp(theme.RadiusMD)
	iconPx := gtx.Dp(theme.IconSM)
	click.Layout(local, func(g layout.Context) layout.Dimensions {
		fg, bg := c.Fg, c.AppBg
		switch {
		case action == system.ActionClose && click.Pressed():
			fg, bg = c.DangerFg, c.DangerSoftPressed
		case action == system.ActionClose && click.Hovered():
			fg, bg = c.DangerFg, c.DangerSoftHover
		case click.Pressed():
			bg = c.PressedWash
		case click.Hovered():
			bg = c.HoverWash
		}
		if bg != c.AppBg {
			paint.FillShape(g.Ops, bg, clip.UniformRRect(image.Rectangle{Max: size}, radius).Op(g.Ops))
		}
		off := op.Offset(image.Pt((size.X-iconPx)/2, (size.Y-iconPx)/2)).Push(g.Ops)
		iconGtx := g
		iconGtx.Constraints = layout.Exact(image.Pt(iconPx, iconPx))
		_, err := env.Icon(&iconGtx, icon, theme.IconSM, fg)
		off.Pop()
		if sc != nil {
			sc.fail(err)
		}
		return layout.Dimensions{Size: size}
	})
}
