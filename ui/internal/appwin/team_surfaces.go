package appwin

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func teamPanel(gtx layout.Context, env *kit.Env, fill color.NRGBA, inset layout.Inset, body layout.Widget) layout.Dimensions {
	gtx.Constraints.Min.X = gtx.Constraints.Max.X
	return layout.Background{}.Layout(gtx, func(g layout.Context) layout.Dimensions {
		paintTeamSurface(&g, env, g.Constraints.Min, fill)
		return layout.Dimensions{Size: g.Constraints.Min}
	}, func(g layout.Context) layout.Dimensions {
		return inset.Layout(g, body)
	})
}

func paintTeamSurface(gtx *layout.Context, env *kit.Env, size image.Point, fill color.NRGBA) {
	radius := gtx.Dp(theme.RadiusXL)
	bounds := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, env.Th.Chrome().BorderSubtle, clip.UniformRRect(bounds, radius).Op(gtx.Ops))
	stroke := max(gtx.Dp(theme.Hairline), 1)
	if size.X > 2*stroke && size.Y > 2*stroke {
		shape := clip.UniformRRect(bounds.Inset(stroke), max(radius-stroke, 0)).Op(gtx.Ops)
		if fill.A < 255 {
			paint.FillShape(gtx.Ops, env.Th.Chrome().SurfaceBase, shape)
		}
		paint.FillShape(gtx.Ops, fill, shape)
	}
}

func teamAvatar(gtx *layout.Context, env *kit.Env, icon kit.IconName, tone theme.Tone) layout.Dimensions {
	return teamAvatarAtSize(gtx, env, icon, tone, theme.ControlLG)
}

func teamAvatarAtSize(gtx *layout.Context, env *kit.Env, icon kit.IconName, tone theme.Tone, size unit.Dp) layout.Dimensions {
	side := gtx.Dp(size)
	gtx.Constraints = layout.Exact(image.Pt(side, side))
	paint.FillShape(gtx.Ops, tone.Fill, clip.UniformRRect(image.Rectangle{Max: gtx.Constraints.Min}, side/2).Op(gtx.Ops))
	return layout.Center.Layout(*gtx, func(g layout.Context) layout.Dimensions {
		dims, err := env.Icon(&g, icon, theme.IconSM, tone.Fg)
		if err != nil {
			return layout.Dimensions{}
		}
		return dims
	})
}

func teamFillLabel(gtx *layout.Context, env *kit.Env, style theme.Type, ink color.NRGBA, text string) layout.Dimensions {
	dims := kit.EllipsisLabel(env, gtx, style, ink, text)
	dims.Size.X = gtx.Constraints.Max.X
	return dims
}

// paintTeamElbow shares the connector geometry between the tree and channel threads.
func paintTeamElbow(gtx *layout.Context, ink color.NRGBA, spine, center, edge int) {
	if center <= 0 || edge <= spine {
		return
	}
	width := max(1, gtx.Dp(theme.Hairline))
	radius := float32(min(gtx.Dp(theme.RadiusSM), center, edge-spine))
	x := float32(spine) + float32(width)/2
	y := float32(center) + float32(width)/2
	var path clip.Path
	path.Begin(gtx.Ops)
	path.MoveTo(f32.Pt(x, 0))
	path.LineTo(f32.Pt(x, y-radius))
	path.QuadTo(f32.Pt(x, y), f32.Pt(x+radius, y))
	path.LineTo(f32.Pt(float32(edge), y))
	paint.FillShape(gtx.Ops, ink, clip.Stroke{Path: path.End(), Width: float32(width)}.Op())
}
