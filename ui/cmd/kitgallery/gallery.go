package main

import (
	"fmt"
	"image"

	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The gallery window: a nav rail of kit Buttons, one page per component,
// and the three modes — light/dark (Ctrl+D), reduced motion (M) and
// keyboard-only (K), which keeps the focus-visible modality on so rings
// show for every focused control (UK-05, UK-09). Everything on screen is
// assembled from kit; the gallery draws none of its own controls.

// The window geometry in dp: tall enough for the whole Button matrix at
// 100%, since the pages clip nothing (UK-E09).
const (
	windowW = unit.Dp(920)
	windowH = unit.Dp(900)
	navRail = unit.Dp(140)
	chrome  = unit.Dp(24)
)

// pageNames are the nav labels, one page per component (UK-09).
func pageNames() []string { return []string{"Button", "IconButton"} }

// pageW is the content width of the page column: the window minus the nav
// rail and the frame padding.
func pageW(gtx *layout.Context) int {
	return gtx.Constraints.Max.X - gtx.Dp(navRail) - 3*gtx.Dp(chrome)
}

// gallery is one window's state: the env, the pages, the current page and
// the mode the theme is in.
type gallery struct {
	env   *kit.Env
	pgs   *pages
	cur   int
	light bool
}

// newGallery builds the window state over env, starting in the mode light
// says.
func newGallery(env *kit.Env, light bool) *gallery {
	return &gallery{env: env, pgs: newPages(env), light: light}
}

// frame lays one frame out: the driver, the window keys, the modality, the
// background, the nav and the current page, then the frame-end
// invalidation. It returns the first error a page hit, so the caller fails
// loudly instead of drawing a broken control forever.
func (g *gallery) frame(gtx *layout.Context) error {
	g.env.Begin(gtx.Now)
	g.keys(gtx)
	g.env.TrackInput(gtx)
	paint.Fill(gtx.Ops, g.env.Th.Chrome().AppBg)
	g.pgs.beginFrame()
	err := g.layout(gtx)
	g.env.FrameEnd(gtx)
	return err
}

// layout draws the nav rail, the mode line and the current page.
func (g *gallery) layout(gtx *layout.Context) error {
	p := g.pgs
	c := g.env.Th.Chrome()

	// Nav rail: one kit button per page, the current one secondary.
	x := gtx.Dp(chrome)
	y := gtx.Dp(chrome)
	for i, name := range pageNames() {
		variant := kit.Ghost
		if i == g.cur {
			variant = kit.Secondary
		}
		pr := kit.ButtonProps{Text: name, Variant: variant, Size: kit.SizeMD}
		var clicked bool
		var navErr error
		box := measure(gtx, func(gtx *layout.Context) image.Point {
			return p.nav[i].Measure(g.env, gtx, pr).Size
		})
		p.put(gtx, image.Pt(x, y), box, func(gtx *layout.Context) {
			clicked, navErr = p.nav[i].Layout(g.env, gtx, pr)
		})
		if navErr != nil {
			return fmt.Errorf("nav %s: %w", name, navErr)
		}
		if clicked {
			g.cur = i
		}
		y += box.Y + gtx.Dp(theme.SpaceSM)
	}

	// The mode line, so a shot tells which modes it was taken in.
	y += gtx.Dp(theme.SpaceXL)
	for _, m := range g.modes() {
		r := p.caption(gtx, image.Pt(x, y), m, c.FgSubtle)
		y += r.Dy() + gtx.Dp(theme.SpaceXS)
	}

	// The current page in its column, measured from the top of the frame.
	origin := image.Pt(x+gtx.Dp(navRail), gtx.Dp(chrome))
	p.origin = origin
	at(gtx, origin, func(gtx *layout.Context) layout.Dimensions {
		col := *gtx
		col.Constraints.Min = image.Point{}
		col.Constraints.Max.X = pageW(gtx)
		if g.cur == 0 {
			p.layoutButtons(&col, 0)
		} else {
			p.layoutIcons(&col, 0)
		}
		return layout.Dimensions{}
	})
	if p.err != nil {
		return fmt.Errorf("page %s: %w", pageNames()[g.cur], p.err)
	}
	return nil
}

// modes names the modes the window is in, for the mode line.
func (g *gallery) modes() []string {
	m := []string{"dark"}
	if g.light {
		m[0] = "light"
	}
	if g.env.ReducedMotion {
		m = append(m, "reduced motion")
	}
	if g.env.KeyboardOnly {
		m = append(m, "keyboard-only")
	}
	return m
}

// keys handles the window-level shortcuts. Every filter names its key, so
// nothing here can swallow keys meant for a control (ui-guide §11.7).
func (g *gallery) keys(gtx *layout.Context) {
	if keyPressed(gtx, key.Filter{Required: key.ModCtrl, Name: "D"}) {
		g.toggleTheme()
	}
	if keyPressed(gtx, key.Filter{Name: "M"}) {
		g.env.ReducedMotion = !g.env.ReducedMotion
	}
	if keyPressed(gtx, key.Filter{Name: "K"}) {
		g.env.KeyboardOnly = !g.env.KeyboardOnly
	}
}

// keyPressed reports whether f's key was pressed this frame, draining the
// filter's events.
func keyPressed(gtx *layout.Context, f key.Filter) bool {
	pressed := false
	for {
		ev, ok := gtx.Event(f)
		if !ok {
			break
		}
		if ke, is := ev.(key.Event); is && ke.State == key.Press {
			pressed = true
		}
	}
	return pressed
}

// toggleTheme swaps light for dark over the same shaper.
func (g *gallery) toggleTheme() {
	g.light = !g.light
	var th theme.Theme
	if g.light {
		th = theme.Light(g.env.Th.Shaper())
	} else {
		th = theme.Dark(g.env.Th.Shaper())
	}
	g.env.Th = &th
}
