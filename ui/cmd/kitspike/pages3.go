package main

import (
	"fmt"
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kitspike"
)

// vietnameseInk is the §3.3 stress string: stacked double diacritics are the
// tallest ink Inter produces.
const vietnameseInk = "Ễ Ặ Ỡ Ậ ỷ ộ"

// typ is the type specimen: every §3.3 token with a Vietnamese and an
// English line (Q4's eyeball page).
type typ struct{}

func (typ) layout(env *kitspike.Env, gtx *layout.Context) {
	c := env.Th.Chrome()
	tokens := []struct {
		name string
		t    kitspike.Type
	}{
		{"caption 11/16", kitspike.TypeCaption}, {"small 12/16", kitspike.TypeSmall},
		{"body 13/18", kitspike.TypeBody}, {"label 13/18 500", kitspike.TypeLabel},
		{"prose 14/22", kitspike.TypeProse}, {"title 14/20 500", kitspike.TypeTitle},
		{"heading 16/24 500", kitspike.TypeHeading}, {"display 20/28 500", kitspike.TypeDisplay},
		{"code 13/20 mono", kitspike.TypeCode}, {"codeSmall 12/16 mono", kitspike.TypeCodeSmall},
	}
	y := 0
	for _, tk := range tokens {
		h := gtx.Sp(tk.t.Line) + gtx.Dp(4)
		at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
			kitspike.DrawText(gtx, env.Sh, kitspike.TypeCodeSmall, 130, tk.name, c.FgSubtle)
			at(gtx, image.Pt(150, 0), func(gtx *layout.Context) layout.Dimensions {
				kitspike.DrawText(gtx, env.Sh, tk.t, gtx.Constraints.Max.X-150, vietnameseInk+"  — The quick brown fox jumps over the lazy dog. 0123456789 → ←", c.Fg)
				return layout.Dimensions{}
			})
			return layout.Dimensions{}
		})
		y += h + gtx.Dp(14)
	}
	at(gtx, image.Pt(0, y+gtx.Dp(8)), func(gtx *layout.Context) layout.Dimensions {
		return kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X,
			"Two lines of prose at the token's absolute line height — check that stacked diacritics touch neither the line above nor clip:", c.FgMuted)
	})
	y += gtx.Dp(8) + gtx.Sp(16) + gtx.Dp(8)
	at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
		return kitspike.DrawText(gtx, env.Sh, kitspike.TypeProse, gtx.Constraints.Max.X,
			"Ễ Ặ Ỡ Ậ ẵng iertas ỷ ộ — Ễ Ặ Ỡ Ậ ỷ ộ — The second line must clear the stacked marks of the first.", c.Fg)
	})
}

// iconPage compares the two icon render paths (Q5) at the §3.4 sizes.
// dense switches the bench's 10×10 grid: 1 = fresh per frame, 2 = cached.
type iconPage struct {
	dense int
}

var iconSizes = []struct {
	name string
	size kitspike.Size
}{
	{"12", 12}, {"14", 14}, {"16", 16}, {"20", 20},
}

func (p iconPage) layout(env *kitspike.Env, gtx *layout.Context) {
	c := env.Th.Chrome()
	if p.dense > 0 {
		// The bench grid: 100 icons at 16 dp, one render path each.
		x, y := 0, 0
		for _, name := range kitspike.IconNames() {
			for row := 0; row < 10; row++ {
				at(gtx, image.Pt(x, y), func(gtx *layout.Context) layout.Dimensions {
					if p.dense == 1 {
						_ = kitspike.DrawIcon(gtx, name, 16, c.Fg)
					} else {
						_ = kitspike.DrawIconCached(gtx, name, 16, c.Fg)
					}
					return layout.Dimensions{Size: image.Pt(gtx.Dp(16), gtx.Dp(16)), Baseline: 0}
				})
				y += gtx.Dp(20)
			}
			x += gtx.Dp(24)
			y = 0
		}
		return
	}
	y := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X,
		"Lucide on the 24 grid, stroke 1.25 dp at 12/14 and 1.5 dp at 16/20. Top row: path parsed and stroked per frame (a). Bottom row: stroked once, transformed per frame (b).", c.FgMuted).Size.Y + gtx.Dp(16)
	for _, name := range kitspike.IconNames() {
		at(gtx, image.Pt(0, y), func(gtx *layout.Context) layout.Dimensions {
			kitspike.DrawText(gtx, env.Sh, kitspike.TypeCodeSmall, 130, name, c.FgSubtle)
			return layout.Dimensions{}
		})
		for row, mode := range []string{"fresh", "cached"} {
			x := 150
			for _, s := range iconSizes {
				at(gtx, image.Pt(x, y+row*gtx.Dp(28)), func(gtx *layout.Context) layout.Dimensions {
					if mode == "fresh" {
						_ = kitspike.DrawIcon(gtx, name, s.size, c.Fg)
					} else {
						_ = kitspike.DrawIconCached(gtx, name, s.size, c.Fg)
					}
					return layout.Dimensions{Size: image.Pt(gtx.Dp(s.size), gtx.Dp(s.size)), Baseline: 0}
				})
				x += 44
			}
		}
		y += gtx.Dp(28)*2 + gtx.Dp(6)
	}
}

// shad is the shadow comparison page (Q6). only selects one card for the
// bench (-1 draws all three).
type shad struct {
	only int
}

func (s *shad) layout(env *kitspike.Env, gtx *layout.Context) {
	c := env.Th.Chrome()
	cards := []struct {
		name string
		draw func(gtx *layout.Context, r image.Rectangle)
	}{
		{"no shadow", func(gtx *layout.Context, r image.Rectangle) {}},
		{"layered rects", func(gtx *layout.Context, r image.Rectangle) {
			kitspike.LayeredShadowTex(gtx, r, 12, 1)
		}},
		{"pre-blurred texture", func(gtx *layout.Context, r image.Rectangle) {
			kitspike.BlurShadowTex(gtx, r)
		}},
	}
	y := kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Constraints.Max.X,
		"§3.6: the default is no shadow; the bench measures what each candidate costs per frame.", c.FgMuted).Size.Y + gtx.Dp(12)
	x := 0
	for i, card := range cards {
		if s.only >= 0 && s.only != i {
			continue
		}
		w, h := gtx.Dp(180), gtx.Dp(120)
		at(gtx, image.Pt(x, y+gtx.Dp(40)), func(gtx *layout.Context) layout.Dimensions {
			r := image.Rect(gtx.Dp(16), 0, gtx.Dp(16)+w, h)
			card.draw(gtx, r)
			kitspike.FillRRect(gtx, r, 12, c.SurfaceRaised)
			kitspike.StrokeRRect(gtx, r, 12, float32(gtx.Dp(1)), c.Border)
			return layout.Dimensions{Size: image.Pt(w+gtx.Dp(32), h), Baseline: 0}
		})
		at(gtx, image.Pt(x, y+gtx.Dp(40)+h+gtx.Dp(8)), func(gtx *layout.Context) layout.Dimensions {
			return kitspike.DrawText(gtx, env.Sh, kitspike.TypeSmall, gtx.Dp(200), fmt.Sprintf("%d: %s", i, card.name), c.FgSubtle)
		})
		x += gtx.Dp(230)
	}
}
