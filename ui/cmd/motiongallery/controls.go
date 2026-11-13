package main

import (
	"image"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// pressButton sinks while pressed and springs back on release, and warms up
// under the pointer.
type pressButton struct {
	click  widget.Clickable
	scale  motion.Follower
	hover  motion.Follower
	forced bool // held down by the autoplay script
}

func newPressButton() pressButton {
	return pressButton{click: widget.Clickable{}, scale: motion.NewFollower(motion.Snappy()), hover: motion.NewFollower(motion.Smooth()), forced: false}
}

// layout draws the button and reports whether it was clicked this frame.
func (b *pressButton) layout(gtx *layout.Context, g *gallery, txt string) (layout.Dimensions, bool) {
	clicked := b.click.Clicked(*gtx)
	c := g.th.Chrome()
	pad, h := gtx.Dp(16), gtx.Dp(34)
	w := textWidth(gtx, g.sh, label(), txt) + 2*pad
	down := b.click.Pressed() || b.forced
	scale := b.scale.Follow(1-0.05*boolf(down), g.dt)
	warm := b.hover.Follow(boolf(b.click.Hovered()), g.dt)
	g.keep(b.scale.Moving() || b.hover.Moving())
	dims := b.click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		defer scaleAround(gtx.Ops, f32.Pt(float32(w)/2, float32(h)/2), scale).Pop()
		bg := mix(c.Action, c.ActionHover, warm)
		if down {
			bg = c.ActionPressed
		}
		fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, bg)
		at(&gtx, image.Pt(pad, (h-gtx.Sp(13)*13/10)/2), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, label(), w, txt, c.ActionFg)
		})
		return layout.Dimensions{Size: image.Pt(w, h), Baseline: 0}
	})
	return dims, clicked
}

// Switch geometry in dp.
const (
	switchW unit.Dp = 46
	switchH unit.Dp = 28
	knobD   unit.Dp = 22
)

// toggleSwitch slides its knob with a little bounce, and the knob stretches
// while pressed, the way a finger squashes it.
type toggleSwitch struct {
	click   widget.Clickable
	on      bool
	knob    motion.Follower
	tint    motion.Follower
	stretch motion.Follower
}

func newToggle() toggleSwitch {
	return toggleSwitch{click: widget.Clickable{}, on: false, knob: motion.NewFollower(motion.Bouncy()), tint: motion.NewFollower(motion.Smooth()), stretch: motion.NewFollower(motion.Snappy())}
}

func (t *toggleSwitch) layout(gtx *layout.Context, g *gallery) layout.Dimensions {
	if t.click.Clicked(*gtx) {
		t.on = !t.on
	}
	c := g.th.Chrome()
	w, h, d := gtx.Dp(switchW), gtx.Dp(switchH), gtx.Dp(knobD)
	inset := (h - d) / 2
	pos := t.knob.Follow(boolf(t.on), g.dt)
	tint := t.tint.Follow(boolf(t.on), g.dt)
	stretch := t.stretch.Follow(boolf(t.click.Pressed())*float32(gtx.Dp(6)), g.dt)
	g.keep(t.knob.Moving() || t.tint.Moving() || t.stretch.Moving())
	return t.click.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, mix(c.Border, c.Action, tint))
		travel := float32(w - 2*inset - d - int(stretch))
		x := inset + int(travel*pos)
		knob := image.Rect(x, inset, x+d+int(stretch), inset+d)
		shadow(&gtx, knob, d/2, 0.6)
		fillRRect(gtx.Ops, knob, d/2, c.ActionFg)
		return layout.Dimensions{Size: image.Pt(w, h), Baseline: 0}
	})
}

// segmented slides a raised pill to the chosen segment; labels brighten as
// the pill passes under them.
type segmented struct {
	clicks   []widget.Clickable
	selected int
	pillX    motion.Follower
	pillW    motion.Follower
}

func segmentNames() []string { return []string{"Plan", "Ask", "Auto", "Full"} }

func newSegmented() segmented {
	return segmented{clicks: make([]widget.Clickable, len(segmentNames())), selected: 2, pillX: motion.NewFollower(motion.Snappy()), pillW: motion.NewFollower(motion.Snappy())}
}

func (s *segmented) layout(gtx *layout.Context, g *gallery) layout.Dimensions {
	for i := range s.clicks {
		if s.clicks[i].Clicked(*gtx) {
			s.selected = i
		}
	}
	c := g.th.Chrome()
	pad, h, inset := gtx.Dp(14), gtx.Dp(34), gtx.Dp(3)
	xs, ws := make([]int, len(s.clicks)), make([]int, len(s.clicks))
	x := inset
	for i, name := range segmentNames() {
		xs[i], ws[i] = x, textWidth(gtx, g.sh, label(), name)+2*pad
		x += ws[i]
	}
	total := x + inset
	px := s.pillX.Follow(float32(xs[s.selected]), g.dt)
	pw := s.pillW.Follow(float32(ws[s.selected]), g.dt)
	g.keep(s.pillX.Moving() || s.pillW.Moving())
	fillRRect(gtx.Ops, image.Rect(0, 0, total, h), h/2, c.SurfaceTinted)
	pill := image.Rect(int(px), inset, int(px+pw), h-inset)
	shadow(gtx, pill, pill.Dy()/2, 0.5)
	fillRRect(gtx.Ops, pill, pill.Dy()/2, c.SurfaceRaised)
	for i, name := range segmentNames() {
		mid := float32(xs[i]) + float32(ws[i])/2
		near := 1 - min(abs32(mid-(px+pw/2))/float32(ws[i]), 1)
		at(gtx, image.Pt(xs[i], 0), func(gtx *layout.Context) layout.Dimensions {
			return s.clicks[i].Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
				at(&gtx, image.Pt(pad, (h-gtx.Sp(13)*13/10)/2), func(gtx *layout.Context) layout.Dimensions {
					return textAt(gtx, g.sh, label(), ws[i], name, mix(c.FgMuted, c.Fg, near))
				})
				return layout.Dimensions{Size: image.Pt(ws[i], h), Baseline: 0}
			})
		})
	}
	return layout.Dimensions{Size: image.Pt(total, h), Baseline: 0}
}

func abs32(v float32) float32 {
	if v < 0 {
		return -v
	}
	return v
}
