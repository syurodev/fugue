package appwin

import (
	"image"
	"image/color"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/gesture"
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

// titleBar is the app-drawn 32 dp Top Bar (FUG-95, FUG-96) and optional Linux CSD chrome.
type titleBar struct {
	goos        string
	deco        widget.Decorations
	click       gesture.Click
	csd         bool
	fullscreen  bool
	focused     bool
	maximized   bool
	title       string
	pending     system.Action
	themeBtn    *kit.IconButtonState
	settingsBtn *kit.IconButtonState
}

func newTitleBar(goos string) *titleBar {
	return &titleBar{
		goos:        goos,
		focused:     true,
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
	return gtx.Dp(theme.TopBarHeight)
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
		return gtx.Dp(theme.MacTrafficLightInset)
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

func (b *titleBar) computeMetrics(gtx *layout.Context, sc *screens) titleBarMetrics {
	btnW := gtx.Dp(theme.ControlSM)
	btnGap := gtx.Dp(theme.SpaceXS)
	csdW := 0
	if b.csd {
		csdW = 3 * (gtx.Dp(theme.SpaceXXXL) + gtx.Dp(theme.SpaceMD))
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

	if b.csd {
		dragStart := leftX + pillW
		dragEnd := max(width-m.rightTotalW, dragStart)
		b.layoutDragArea(&local, image.Rect(dragStart, 0, dragEnd, h))
	}
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
		b.layoutButtons(env, &csdGtx, h)
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
			sc.toggleThemeFromUI(isDark)
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

// layoutDragArea registers the middle window move and double-click maximize area.
func (b *titleBar) layoutDragArea(gtx *layout.Context, area image.Rectangle) {
	size := area.Size()
	if size.X <= 0 || size.Y <= 0 {
		return
	}
	for {
		ev, ok := b.click.Update(gtx.Source)
		if !ok {
			break
		}
		if ev.Kind == gesture.KindClick && ev.NumClicks == 2 {
			b.pending |= b.toggleMaximize()
		}
	}
	off := op.Offset(area.Min).Push(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Exact(size)
	b.deco.LayoutMove(local, func(g layout.Context) layout.Dimensions {
		st := clip.Rect{Max: size}.Push(g.Ops)
		b.click.Add(g.Ops)
		st.Pop()
		return layout.Dimensions{Size: size}
	})
	off.Pop()
}

func (b *titleBar) toggleMaximize() system.Action {
	if b.maximized {
		return system.ActionUnmaximize
	}
	return system.ActionMaximize
}

// layoutButtons lays out minimize, maximize or restore, and close, right
// aligned. Hover and pressed states are the kit washes; close hovers on the
// danger tint, as the kit's danger button does.
func (b *titleBar) layoutButtons(env *kit.Env, gtx *layout.Context, h int) layout.Dimensions {
	side := gtx.Dp(theme.SpaceXXXL) + gtx.Dp(theme.SpaceMD)
	buttons := []struct {
		action system.Action
		glyph  glyphKind
	}{
		{system.ActionMinimize, glyphMinimize},
		{system.ActionMaximize, glyphMaximize},
		{system.ActionClose, glyphClose},
	}
	x := 0
	for _, btn := range buttons {
		kind := btn.glyph
		if kind == glyphMaximize && b.maximized {
			kind = glyphRestore
		}
		off := op.Offset(image.Pt(x, 0)).Push(gtx.Ops)
		b.layoutButton(env, gtx, btn.action, kind, image.Pt(side, h))
		off.Pop()
		x += side
	}
	return layout.Dimensions{Size: image.Pt(x, h)}
}

func (b *titleBar) layoutButton(env *kit.Env, gtx *layout.Context, action system.Action, kind glyphKind, size image.Point) {
	c := env.Th.Chrome()
	click := b.deco.Clickable(action)
	local := *gtx
	local.Constraints = layout.Exact(size)
	click.Layout(local, func(g layout.Context) layout.Dimensions {
		fg, bg := c.Fg, c.AppBg
		switch {
		case kind == glyphClose && click.Pressed():
			fg, bg = c.DangerFg, c.DangerSoftPressed
		case kind == glyphClose && click.Hovered():
			fg, bg = c.DangerFg, c.DangerSoftHover
		case click.Pressed():
			bg = c.PressedWash
		case click.Hovered():
			bg = c.HoverWash
		}
		if bg != c.AppBg {
			paint.FillShape(g.Ops, bg, clip.Rect{Max: size}.Op())
		}
		drawGlyph(&g, kind, fg, size)
		return layout.Dimensions{Size: size}
	})
}

type glyphKind int

const (
	glyphMinimize glyphKind = iota
	glyphMaximize
	glyphRestore
	glyphClose
)

// drawGlyph strokes the button's symbol centered in size. The glyph box and
// stroke come from spacing tokens, so the symbols scale with the UI scale.
func drawGlyph(gtx *layout.Context, kind glyphKind, fg color.NRGBA, size image.Point) {
	box := gtx.Dp(theme.SpaceLG)
	stroke := float32(max(1, gtx.Dp(theme.SpaceXXS)/2))
	x0, y0 := (size.X-box)/2, (size.Y-box)/2
	r := image.Rect(x0, y0, x0+box, y0+box)
	var p clip.Path
	p.Begin(gtx.Ops)
	switch kind {
	case glyphMinimize:
		mid := float32(y0 + box/2)
		p.MoveTo(f32.Pt(float32(r.Min.X), mid))
		p.LineTo(f32.Pt(float32(r.Max.X), mid))
	case glyphMaximize:
		rect(&p, r)
	case glyphRestore:
		gap := box / 4
		rect(&p, image.Rect(r.Min.X, r.Min.Y+gap, r.Max.X-gap, r.Max.Y))
		p.MoveTo(f32.Pt(float32(r.Min.X+gap), float32(r.Min.Y+gap)))
		p.LineTo(f32.Pt(float32(r.Min.X+gap), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Max.X), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Max.X), float32(r.Max.Y-gap)))
		p.LineTo(f32.Pt(float32(r.Max.X-gap), float32(r.Max.Y-gap)))
	case glyphClose:
		p.MoveTo(f32.Pt(float32(r.Min.X), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Max.X), float32(r.Max.Y)))
		p.MoveTo(f32.Pt(float32(r.Max.X), float32(r.Min.Y)))
		p.LineTo(f32.Pt(float32(r.Min.X), float32(r.Max.Y)))
	}
	st := clip.Stroke{Path: p.End(), Width: stroke}.Op().Push(gtx.Ops)
	paint.ColorOp{Color: fg}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	st.Pop()
}

// rect appends a closed rectangle subpath.
func rect(p *clip.Path, r image.Rectangle) {
	p.MoveTo(f32.Pt(float32(r.Min.X), float32(r.Min.Y)))
	p.LineTo(f32.Pt(float32(r.Max.X), float32(r.Min.Y)))
	p.LineTo(f32.Pt(float32(r.Max.X), float32(r.Max.Y)))
	p.LineTo(f32.Pt(float32(r.Min.X), float32(r.Max.Y)))
	p.Close()
}
