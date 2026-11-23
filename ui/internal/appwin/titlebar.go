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

// Window chrome (FUG-58). On Linux, Gio v0.10.3 draws its own Material
// title bar (a saturated blue unrelated to the theme) whenever the platform
// grants no server-side decorations, which is every GNOME Wayland session:
// mutter does not implement xdg-decoration. Fugue opts out of that fallback
// with app.Decorated(false) and draws a themed bar when, and only when, the
// platform reports Config.Decorated == false. Where the compositor or window
// manager supplies decorations (X11, KDE and wlroots Wayland), Config.Decorated
// is true and nothing is drawn. macOS, Windows, Android, iOS and the web keep
// their native chrome: windowOptions adds nothing there.

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

// titleBar is the app-drawn title bar of one window. The zero value draws
// nothing until applyConfig reports a platform without decorations.
type titleBar struct {
	goos      string
	deco      widget.Decorations
	click     gesture.Click
	enabled   bool
	focused   bool
	maximized bool
	title     string
	pending   system.Action
}

func newTitleBar(goos string) *titleBar { return &titleBar{goos: goos} }

// applyConfig follows the window's platform state (app.ConfigEvent).
func (b *titleBar) applyConfig(cfg app.Config) {
	if b == nil {
		return
	}
	b.enabled = drawsOwnChrome(b.goos) && !cfg.Decorated && cfg.Mode != app.Fullscreen
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

// height is the bar's height in px, zero when none is drawn.
func (b *titleBar) height(gtx *layout.Context) int {
	if b == nil || !b.enabled {
		return 0
	}
	return gtx.Dp(theme.Tab)
}

// layout draws the bar at the top of the window and returns its height.
func (b *titleBar) layout(env *kit.Env, gtx *layout.Context) int {
	h := b.height(gtx)
	if h == 0 {
		return 0
	}
	b.pending |= b.deco.Update(*gtx)
	c := env.Th.Chrome()
	width := gtx.Constraints.Max.X
	local := *gtx
	local.Constraints = layout.Exact(image.Pt(width, h))
	paint.FillShape(local.Ops, c.AppBg, clip.Rect{Max: local.Constraints.Max}.Op())
	paint.FillShape(local.Ops, c.BorderSubtle, clip.Rect{Min: image.Pt(0, h-1), Max: image.Pt(width, h)}.Op())
	layout.Flex{Alignment: layout.Middle}.Layout(local,
		layout.Flexed(1, func(g layout.Context) layout.Dimensions { return b.layoutTitle(env, &g, h) }),
		layout.Rigid(func(g layout.Context) layout.Dimensions { return b.layoutButtons(env, &g, h) }),
	)
	return h
}

// layoutTitle is the drag area: a press moves the window, a double click
// toggles maximize. Both ride one input area behind the title text.
func (b *titleBar) layoutTitle(env *kit.Env, gtx *layout.Context, h int) layout.Dimensions {
	size := image.Pt(gtx.Constraints.Max.X, h)
	for {
		ev, ok := b.click.Update(gtx.Source)
		if !ok {
			break
		}
		if ev.Kind == gesture.KindClick && ev.NumClicks == 2 {
			b.pending |= b.toggleMaximize()
		}
	}
	c := env.Th.Chrome()
	fg := c.FgMuted
	if b.focused {
		fg = c.Fg
	}
	b.deco.LayoutMove(*gtx, func(g layout.Context) layout.Dimensions {
		st := clip.Rect{Max: size}.Push(g.Ops)
		b.click.Add(g.Ops)
		st.Pop()
		inset := layout.Inset{Left: theme.SpaceLG}
		inset.Layout(g, func(in layout.Context) layout.Dimensions {
			in.Constraints.Min = image.Pt(0, h)
			return layout.W.Layout(in, func(t layout.Context) layout.Dimensions {
				l := t
				return kit.Label(env, &l, theme.TypeSmall(), fg, b.title)
			})
		})
		return layout.Dimensions{Size: size}
	})
	return layout.Dimensions{Size: size}
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
