package main

import (
	"image"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// Page geometry in dp.
const (
	pagePad   unit.Dp = 28
	columnGap unit.Dp = 20
	cardGap   unit.Dp = 16
	cardPad   unit.Dp = 16
	cardR     unit.Dp = 14
)

// gallery is the window: the theme, the frame driver and every demo.
type gallery struct {
	th      theme.Theme
	sh      *text.Shaper
	driver  motion.Driver
	dt      time.Duration
	now     time.Time
	menu    dropdown
	run     pressButton
	toggle  toggleSwitch
	seg     segmented
	section disclosure
	feed    feed
	script  *autoplay
}

func newGallery(th *theme.Theme, autoplay bool) *gallery {
	g := &gallery{
		th: *th, sh: th.Shaper(), driver: motion.Driver{}, dt: 0, now: time.Time{},
		menu: newDropdown(), run: newPressButton(), toggle: newToggle(), seg: newSegmented(),
		section: newDisclosure(), feed: newFeed(), script: nil,
	}
	if autoplay {
		g.script = newAutoplay()
	}
	return g
}

// keep asks for the next frame when moving is true.
func (g *gallery) keep(moving bool) {
	if moving {
		g.driver.Keep()
	}
}

// frame draws one frame at gtx.Now and schedules the next only if something
// moves or a scripted step is due.
func (g *gallery) frame(gtx *layout.Context) {
	g.dt = g.driver.Begin(gtx.Now)
	g.now = gtx.Now
	if g.script != nil {
		g.script.step(g)
	}
	paint.Fill(gtx.Ops, g.th.Chrome().AppBg)
	p := gtx.Dp(pagePad)
	inner := *gtx
	inner.Constraints = layout.Exact(gtx.Constraints.Max.Sub(image.Pt(2*p, 2*p)))
	at(gtx, image.Pt(p, p), func(*layout.Context) layout.Dimensions { return g.page(&inner) })
	if at, ok := g.driver.Next(); ok {
		gtx.Execute(op.InvalidateCmd{At: at})
	}
}

// page lays out the title and the two columns of cards.
func (g *gallery) page(gtx *layout.Context) layout.Dimensions {
	c := g.th.Chrome()
	head := textAt(gtx, g.sh, heading(), gtx.Constraints.Max.X, "Motion gallery", c.Fg)
	sub := at(gtx, image.Pt(0, head.Size.Y+gtx.Dp(4)), func(gtx *layout.Context) layout.Dimensions {
		return textAt(gtx, g.sh, body(), gtx.Constraints.Max.X, "Every animation is a spring: click again mid-way and it turns around from where it is.", c.FgMuted)
	})
	top := head.Size.Y + sub.Size.Y + gtx.Dp(24)
	gap := gtx.Dp(columnGap)
	colW := (gtx.Constraints.Max.X - gap) / 2
	left := []cardFn{
		{"Menu", g.menu.layout},
		{"Controls", g.controls},
	}
	right := []cardFn{
		{"Disclosure", g.section.layout},
		{"Messages", g.feed.layout},
	}
	g.column(gtx, image.Pt(0, top), colW, left)
	g.column(gtx, image.Pt(colW+gap, top), colW, right)
	return layout.Dimensions{Size: gtx.Constraints.Max, Baseline: 0}
}

// cardFn is one titled card and the demo inside it.
type cardFn struct {
	title string
	body  func(gtx *layout.Context, g *gallery) layout.Dimensions
}

// column stacks cards from origin down.
func (g *gallery) column(gtx *layout.Context, origin image.Point, width int, cards []cardFn) {
	y := origin.Y
	for _, cf := range cards {
		dims := at(gtx, image.Pt(origin.X, y), func(gtx *layout.Context) layout.Dimensions {
			return g.card(gtx, width, cf)
		})
		y += dims.Size.Y + gtx.Dp(cardGap)
	}
}

// card draws a surface with a small title and the demo inside.
func (g *gallery) card(gtx *layout.Context, width int, cf cardFn) layout.Dimensions {
	c := g.th.Chrome()
	pad := gtx.Dp(cardPad)
	m := op.Record(gtx.Ops)
	title := textAt(gtx, g.sh, small(), width, strings.ToUpper(cf.title), c.FgSubtle)
	inner := *gtx
	inner.Constraints = layout.Constraints{Min: image.Point{}, Max: image.Pt(width-2*pad, gtx.Constraints.Max.Y)}
	content := at(gtx, image.Pt(0, title.Size.Y+gtx.Dp(12)), func(*layout.Context) layout.Dimensions {
		return cf.body(&inner, g)
	})
	call := m.Stop()
	size := image.Pt(width, title.Size.Y+gtx.Dp(12)+content.Size.Y+2*pad)
	r := image.Rectangle{Max: size}
	fillRRect(gtx.Ops, r, gtx.Dp(cardR), c.SurfaceBase)
	strokeRRect(gtx.Ops, r, gtx.Dp(cardR), float32(gtx.Dp(1)), c.Border)
	at(gtx, image.Pt(pad, pad), func(gtx *layout.Context) layout.Dimensions {
		call.Add(gtx.Ops)
		return layout.Dimensions{Size: size, Baseline: 0}
	})
	return layout.Dimensions{Size: size, Baseline: 0}
}

// controls is the card with the button, the switch and the segmented
// control, one per row.
func (g *gallery) controls(gtx *layout.Context, _ *gallery) layout.Dimensions {
	c := g.th.Chrome()
	y := 0
	row := func(name string, w func(gtx *layout.Context) layout.Dimensions) {
		dims := at(gtx, image.Pt(0, y), w)
		textH := at(gtx, image.Pt(dims.Size.X+gtx.Dp(14), y+(dims.Size.Y-gtx.Sp(13)*13/10)/2), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, body(), gtx.Constraints.Max.X, name, c.FgMuted)
		})
		y += max(dims.Size.Y, textH.Size.Y) + gtx.Dp(16)
	}
	row("Press and hold: it sinks, then springs back", func(gtx *layout.Context) layout.Dimensions {
		dims, _ := g.run.layout(gtx, g, "Run checks")
		return dims
	})
	row("Auto-approve reads", func(gtx *layout.Context) layout.Dimensions { return g.toggle.layout(gtx, g) })
	row("", func(gtx *layout.Context) layout.Dimensions { return g.seg.layout(gtx, g) })
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y-gtx.Dp(16)), Baseline: 0}
}
