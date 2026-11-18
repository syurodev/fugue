package kit

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/motion"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// insetFocusRing strokes the §3.4 inset focus ring 1 dp (theme.Hairline) inside
// r, for items inside clipped containers such as list rows, menu items and
// table cells.
func insetFocusRing(gtx *layout.Context, r image.Rectangle, radius int, c color.NRGBA, a float32) {
	if a <= 0 || c.A == 0 {
		return
	}
	inset := gtx.Dp(theme.Hairline) + gtx.Dp(theme.FocusStroke)/2
	strokeRRect(gtx.Ops, r.Inset(inset), max(radius-inset, 0), float32(gtx.Dp(theme.FocusStroke)), fade(c, a))
}

// pillRect returns r inset horizontally by theme.SpaceXS (4 dp) so list hover
// and selection fills form a rounded pill rather than an edge-to-edge band
// (§6.20).
func pillRect(gtx *layout.Context, r image.Rectangle) image.Rectangle {
	dx := gtx.Dp(theme.SpaceXS)
	return image.Rect(r.Min.X+dx, r.Min.Y, max(r.Max.X-dx, r.Min.X+dx), r.Max.Y)
}

// slidingPill animates a rounded selection or highlight pill's position and
// size on Smooth while fading its opacity on Snappy.
type slidingPill struct {
	x, y, w, h, a motion.Follower
	rect          image.Rectangle
	alpha         float32
	seeded        bool
}

func newSlidingPill() slidingPill {
	sm := motion.Smooth()
	return slidingPill{
		x: motion.NewFollower(sm),
		y: motion.NewFollower(sm),
		w: motion.NewFollower(sm),
		h: motion.NewFollower(sm),
		a: motion.NewFollower(motion.Snappy()),
	}
}

func (p *slidingPill) step(env *Env, target image.Rectangle, active bool) {
	if env.ReducedMotion {
		if active {
			p.rect = target
			p.alpha = 1
			p.x.Snap(float32(target.Min.X))
			p.y.Snap(float32(target.Min.Y))
			p.w.Snap(float32(target.Dx()))
			p.h.Snap(float32(target.Dy()))
			p.a.Snap(1)
			p.seeded = true
		} else {
			p.alpha = 0
			p.a.Snap(0)
			p.seeded = false
		}
		return
	}
	if active {
		if !p.seeded || p.alpha == 0 {
			p.x.Snap(float32(target.Min.X))
			p.y.Snap(float32(target.Min.Y))
			p.w.Snap(float32(target.Dx()))
			p.h.Snap(float32(target.Dy()))
			p.seeded = true
		}
		x := int(followSmooth(env, &p.x, float32(target.Min.X)) + 0.5)
		y := int(followSmooth(env, &p.y, float32(target.Min.Y)) + 0.5)
		w := int(followSmooth(env, &p.w, float32(target.Dx())) + 0.5)
		h := int(followSmooth(env, &p.h, float32(target.Dy())) + 0.5)
		p.rect = image.Rect(x, y, x+max(w, 0), y+max(h, 0))
		p.alpha = followSmooth(env, &p.a, 1)
		return
	}
	p.alpha = followSmooth(env, &p.a, 0)
	if p.alpha == 0 {
		p.seeded = false
	}
}

func (p *slidingPill) draw(gtx *layout.Context, radius int, c color.NRGBA) {
	fillLayer(gtx, p.rect, radius, c, p.alpha)
}

func (p *slidingPill) drawBordered(gtx *layout.Context, radius int, fill, border color.NRGBA) {
	fillLayer(gtx, p.rect, radius, fill, p.alpha)
	if border.A != 0 && p.alpha > 0 {
		strokeRRect(gtx.Ops, p.rect, radius, float32(gtx.Dp(theme.Hairline)), fade(border, p.alpha))
	}
}

func boolFloat(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func stepPresence(env *Env, p *motion.Presence) float32 {
	if env.ReducedMotion {
		p.Snap(p.Shown())
		return p.Progress()
	}
	if p.Advance(env.DT()) {
		env.Keep()
	}
	return p.Progress()
}

// drawRotatedIcon draws icon at pt rotated by angleRad around its center.
func drawRotatedIcon(env *Env, gtx *layout.Context, pt image.Point, name IconName, size unit.Dp, ink color.NRGBA, angleRad float32) error {
	px := gtx.Dp(size)
	center := f32.Pt(float32(pt.X)+float32(px)/2, float32(pt.Y)+float32(px)/2)
	rot := op.Affine(f32.AffineId().Rotate(center, angleRad)).Push(gtx.Ops)
	err := drawIconAt(env, gtx, pt, name, size, ink)
	rot.Pop()
	return err
}

// drawRunAt draws single-line text at pt within maxW and records its run bounds.
func drawRunAt(env *Env, gtx *layout.Context, pt image.Point, maxW, h int, t theme.Type, col color.NRGBA, txt string, ellipsize bool) {
	at(gtx, pt, func(gtx *layout.Context) {
		g := *gtx
		if maxW > 0 {
			g.Constraints = layout.Constraints{Max: image.Pt(maxW, h)}
		}
		dims := drawText(env, &g, t, col, txt, ellipsize)
		recordRun(env, image.Rectangle{Max: dims.Size}.Add(pt))
	})
}
