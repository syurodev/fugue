package main

import (
	"image"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

type compareViewPage struct {
	view *kit.CompareViewState
}

func newCompareViewPage() compareViewPage {
	return compareViewPage{view: kit.NewCompareViewState()}
}

func (p *pages) layoutCompareViews(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X, "CompareView: two-column field comparison with changed rows highlighted and long values wrapped.") + gtx.Dp(theme.SpaceXL)
	p.flow(gtx, y, []block{p.compareViewSpecBlock})
}

func (p *pages) compareViewSpecBlock(gtx *layout.Context, dry bool) image.Point {
	c := p.env.Th.Chrome()
	line := gtx.Sp(theme.TypeSmall().Line)
	gap := gtx.Dp(theme.SpaceLG)
	w := min(gtx.Constraints.Max.X, gtx.Dp(theme.MainSlotMinWidth))

	props := kit.CompareViewProps{
		LeftTitle:  "On this machine",
		RightTitle: "In the file",
		Rows: []kit.CompareRow{
			{Name: "Kind", Left: "decision", Right: "decision", Changed: false},
			{Name: "Summary", Left: "Use Argon2id for vault key derivation", Right: "Use Argon2id (64 MiB) for vault key derivation", Changed: true},
			{Name: "Body", Left: "Vault uses Argon2id with default host parameters and XChaCha20-Poly1305.", Right: "Vault uses Argon2id (64 MiB, 3 iterations) and XChaCha20-Poly1305 for payload sealing.", Changed: true},
		},
	}

	sz := measure(gtx, func(gtx *layout.Context) image.Point {
		g := *gtx
		g.Constraints.Max.X = w
		d, err := p.cv.view.Layout(p.env, &g, props)
		if err != nil && p.err == nil {
			p.err = err
		}
		return d.Size
	})

	y := line + gap
	if !dry {
		p.caption(gtx, image.Point{}, "CompareView (changed + unchanged rows)", c.FgMuted)
		cd := cellDraw{
			key: cell{compCompareView, 0, 0, stRest},
			box: sz,
			lay: func(gtx *layout.Context) {
				g := *gtx
				g.Constraints.Max.X = w
				if _, err := p.cv.view.Layout(p.env, &g, props); err != nil && p.err == nil {
					p.err = err
				}
			},
		}
		p.place(gtx, image.Pt(0, y), &cd)
	}
	return image.Pt(w, y+sz.Y)
}
