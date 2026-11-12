package main

import (
	"image"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// Menu geometry in dp.
const (
	menuW     unit.Dp = 240
	triggerH  unit.Dp = 36
	itemH     unit.Dp = 30
	menuInset unit.Dp = 5
)

func menuOptions() []string { return []string{"Low", "Medium", "High", "Extra high", "Max"} }

// dropdown is a menu that grows out of its button. Opening and closing are
// one Presence, so a click while it is still opening sends it back from
// where it is. A highlight slides between hovered items, and the chosen
// value swaps in the button with a short vertical crossfade.
type dropdown struct {
	trigger  widget.Clickable
	items    []widget.Clickable
	scrim    widget.Clickable
	open     motion.Presence
	hoverY   motion.Follower
	hoverA   motion.Follower
	swap     motion.Presence
	selected int
	previous int
	forced   int // hovered item set by the autoplay script, -1 for none
}

func newDropdown() dropdown {
	return dropdown{
		trigger: widget.Clickable{}, items: make([]widget.Clickable, len(menuOptions())), scrim: widget.Clickable{},
		open: motion.NewPresence(motion.Snappy(), motion.Exit()), hoverY: motion.NewFollower(motion.Snappy()),
		hoverA: motion.NewFollower(motion.Smooth()), swap: motion.NewPresence(motion.Smooth(), motion.Smooth()),
		selected: 1, previous: 1, forced: -1,
	}
}

// choose selects item i and closes the menu.
func (d *dropdown) choose(i int) {
	if i != d.selected {
		d.previous, d.selected = d.selected, i
		d.swap = motion.NewPresence(motion.Smooth(), motion.Smooth())
		d.swap.Show(true)
	}
	d.open.Show(false)
}

func (d *dropdown) events(gtx *layout.Context) {
	if d.trigger.Clicked(*gtx) {
		d.open.Show(!d.open.Shown())
	}
	if d.scrim.Clicked(*gtx) {
		d.open.Show(false)
	}
	for i := range d.items {
		if d.items[i].Clicked(*gtx) {
			d.choose(i)
		}
	}
}

func (d *dropdown) layout(gtx *layout.Context, g *gallery) layout.Dimensions {
	d.events(gtx)
	g.keep(d.open.Advance(g.dt))
	g.keep(d.swap.Advance(g.dt))
	w, h := gtx.Dp(menuW), gtx.Dp(triggerH)
	dims := d.trigger.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		d.drawTrigger(&gtx, g, w, h)
		return layout.Dimensions{Size: image.Pt(w, h), Baseline: 0}
	})
	if d.open.Visible() {
		m := op.Record(gtx.Ops)
		d.drawPanel(gtx, g, image.Pt(0, h+gtx.Dp(6)), w)
		op.Defer(gtx.Ops, m.Stop())
	}
	// The open menu floats over the cards below, as menus do.
	return dims
}

// drawTrigger draws the button with the current value and a chevron that
// turns with the menu.
func (d *dropdown) drawTrigger(gtx *layout.Context, g *gallery, w, h int) {
	c := g.th.Chrome()
	r := image.Rect(0, 0, w, h)
	bg := c.SurfaceRaised
	if d.trigger.Hovered() {
		bg = mix(bg, c.SurfaceTinted, 0.6)
	}
	fillRRect(gtx.Ops, r, gtx.Dp(9), bg)
	strokeRRect(gtx.Ops, r, gtx.Dp(9), float32(gtx.Dp(1)), c.Border)
	ty := (h - gtx.Sp(13)*13/10) / 2
	lw := at(gtx, image.Pt(gtx.Dp(12), ty), func(gtx *layout.Context) layout.Dimensions {
		return textAt(gtx, g.sh, body(), w, "Effort", c.FgMuted)
	}).Size.X
	x := gtx.Dp(12) + lw + gtx.Dp(10)
	defer clip.Rect(image.Rect(x, 0, w-gtx.Dp(30), h)).Push(gtx.Ops).Pop()
	p := d.swap.Progress()
	if p < 1 {
		shift := int(float32(gtx.Dp(10)) * p)
		at(gtx, image.Pt(x, ty-shift), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, label(), w, menuOptions()[d.previous], fade(c.Fg, 1-p))
		})
	}
	shift := int(float32(gtx.Dp(10)) * (1 - p))
	if d.swap.Progress() == 0 && !d.swap.Shown() {
		shift = 0
		p = 1
	}
	at(gtx, image.Pt(x, ty+shift), func(gtx *layout.Context) layout.Dimensions {
		return textAt(gtx, g.sh, label(), w, menuOptions()[d.selected], fade(c.Fg, p))
	})
	chevron(gtx.Ops, f32.Pt(float32(w-gtx.Dp(18)), float32(h)/2), float32(gtx.Dp(8)), 3.1416*d.open.Progress(), float32(gtx.Dp(1.5)), c.IconMuted)
}

// drawPanel draws the open menu at origin: a scrim that closes it on an
// outside click, then the panel scaled and faded from the button's edge.
func (d *dropdown) drawPanel(gtx *layout.Context, g *gallery, origin image.Point, w int) {
	if d.open.Shown() {
		far := 1 << 14
		s := op.Offset(image.Pt(-far, -far)).Push(gtx.Ops)
		big := *gtx
		big.Constraints = layout.Exact(image.Pt(2*far, 2*far))
		d.scrim.Layout(big, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: gtx.Constraints.Min, Baseline: 0}
		})
		s.Pop()
	}
	p := d.open.Progress()
	inset := gtx.Dp(menuInset)
	h := 2*inset + gtx.Dp(itemH)*len(d.items)
	defer op.Offset(origin.Add(image.Pt(0, -int(float32(gtx.Dp(6))*(1-p))))).Push(gtx.Ops).Pop()
	defer scaleAround(gtx.Ops, f32.Pt(float32(w)/2, 0), 0.94+0.06*p).Pop()
	defer paint.PushOpacity(gtx.Ops, min(max(p*1.6, 0), 1)).Pop()
	c := g.th.Chrome()
	r := image.Rect(0, 0, w, h)
	shadow(gtx, r, gtx.Dp(11), 1)
	fillRRect(gtx.Ops, r, gtx.Dp(11), c.SurfaceRaised)
	strokeRRect(gtx.Ops, r, gtx.Dp(11), float32(gtx.Dp(1)), c.Border)
	d.drawHighlight(gtx, g, inset, w)
	for i, name := range menuOptions() {
		at(gtx, image.Pt(inset, inset+i*gtx.Dp(itemH)), func(gtx *layout.Context) layout.Dimensions {
			return d.item(gtx, g, i, name, w-2*inset)
		})
	}
}

// drawHighlight slides the hover highlight to the hovered item and fades it
// when the pointer leaves the items.
func (d *dropdown) drawHighlight(gtx *layout.Context, g *gallery, inset, w int) {
	hovered := d.forced
	for i := range d.items {
		if hovered < 0 && d.items[i].Hovered() {
			hovered = i
		}
	}
	target := d.hoverY.Value()
	if hovered >= 0 {
		target = float32(hovered * gtx.Dp(itemH))
	}
	y := d.hoverY.Follow(target, g.dt)
	alpha := d.hoverA.Follow(boolf(hovered >= 0), g.dt)
	g.keep(d.hoverY.Moving() || d.hoverA.Moving())
	r := image.Rect(inset, inset+int(y), w-inset, inset+int(y)+gtx.Dp(itemH))
	fillRRect(gtx.Ops, r, gtx.Dp(7), fade(g.th.Chrome().SurfaceTinted, alpha))
}

func (d *dropdown) item(gtx *layout.Context, g *gallery, i int, name string, w int) layout.Dimensions {
	c := g.th.Chrome()
	h := gtx.Dp(itemH)
	return d.items[i].Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		at(&gtx, image.Pt(gtx.Dp(10), (h-gtx.Sp(13)*13/10)/2), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, body(), w, name, c.Fg)
		})
		if i == d.selected {
			check(gtx.Ops, f32.Pt(float32(w-gtx.Dp(16)), float32(h)/2), float32(gtx.Dp(10)), float32(gtx.Dp(1.6)), c.Action)
		}
		return layout.Dimensions{Size: image.Pt(w, h), Baseline: 0}
	})
}

func boolf(v bool) float32 {
	if v {
		return 1
	}
	return 0
}
