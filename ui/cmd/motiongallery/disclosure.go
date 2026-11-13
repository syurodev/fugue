package main

import (
	"image"
	"math"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/widget"

	"github.com/syurodev/fugue/ui/internal/motion"
)

// disclosure is a section that opens and closes: its height springs to the
// content's measured height, the content fades and settles into place, and
// the chevron turns. Content is measured every frame, so a section whose
// content changes while open resizes smoothly too.
type disclosure struct {
	header widget.Clickable
	open   bool
	height motion.Follower
	turn   motion.Follower
}

func newDisclosure() disclosure {
	return disclosure{header: widget.Clickable{}, open: false, height: motion.NewFollower(motion.Smooth()), turn: motion.NewFollower(motion.Snappy())}
}

func sectionLines() []string {
	return []string{
		"internal/app/vendor_sessions.go   +42 −7",
		"internal/app/vendor_session_actor.go   +118 −31",
		"internal/vendor/port.go   +3 −1",
		"3 files changed, tests pass under -race",
	}
}

func (d *disclosure) layout(gtx *layout.Context, g *gallery) layout.Dimensions {
	if d.header.Clicked(*gtx) {
		d.open = !d.open
	}
	c := g.th.Chrome()
	w, rowH := gtx.Constraints.Max.X, gtx.Dp(32)
	turn := d.turn.Follow(boolf(d.open), g.dt)
	d.header.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		chevron(gtx.Ops, f32.Pt(float32(gtx.Dp(8)), float32(rowH)/2), float32(gtx.Dp(8)), float32(-math.Pi/2)*(1-turn), float32(gtx.Dp(1.5)), c.IconMuted)
		at(&gtx, image.Pt(gtx.Dp(24), (rowH-gtx.Sp(13)*13/10)/2), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, label(), w, "Tool output · 3 files", c.Fg)
		})
		return layout.Dimensions{Size: image.Pt(w, rowH), Baseline: 0}
	})
	m := op.Record(gtx.Ops)
	content := d.content(gtx, g, w)
	call := m.Stop()
	full := float32(content.Size.Y + gtx.Dp(8))
	h := d.height.Follow(full*boolf(d.open), g.dt)
	g.keep(d.height.Moving() || d.turn.Moving())
	if h > 0.5 {
		ratio := min(h/full, 1)
		stack := clip.Rect(image.Rect(0, rowH, w, rowH+int(h))).Push(gtx.Ops)
		offset := op.Offset(image.Pt(0, rowH+gtx.Dp(8)-int(float32(gtx.Dp(8))*(1-ratio)))).Push(gtx.Ops)
		opacity := paint.PushOpacity(gtx.Ops, ratio*ratio)
		call.Add(gtx.Ops)
		opacity.Pop()
		offset.Pop()
		stack.Pop()
	}
	return layout.Dimensions{Size: image.Pt(w, rowH+int(h)), Baseline: 0}
}

// content draws the section's lines in a tinted box.
func (d *disclosure) content(gtx *layout.Context, g *gallery, w int) layout.Dimensions {
	c := g.th.Chrome()
	pad := gtx.Dp(12)
	m := op.Record(gtx.Ops)
	y := pad
	for _, line := range sectionLines() {
		y += at(gtx, image.Pt(pad, y), func(gtx *layout.Context) layout.Dimensions {
			return textAt(gtx, g.sh, small(), w-2*pad, line, c.FgMuted)
		}).Size.Y + gtx.Dp(4)
	}
	call := m.Stop()
	r := image.Rect(0, 0, w, y+pad-gtx.Dp(4))
	fillRRect(gtx.Ops, r, gtx.Dp(9), c.SurfaceTinted)
	call.Add(gtx.Ops)
	return layout.Dimensions{Size: r.Size(), Baseline: 0}
}
