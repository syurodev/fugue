package kit

import (
	"image"
	"image/color"

	"gioui.org/layout"
	"gioui.org/op"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// CompareView exists so any two-state inspection (such as import conflict diffs,
// setting overrides, or revision comparisons) renders aligned field rows side
// by side with changed fields highlighted and long values wrapped rather than
// reimplementing comparison tables per screen (UK-02, UK-03).
//
//	dims, err := cmpView.Layout(env, gtx, kit.CompareViewProps{
//	    LeftTitle:  "On this machine",
//	    RightTitle: "In the file",
//	    Rows:       rows,
//	})

// CompareRow describes one aligned field in a CompareView.
type CompareRow struct {
	Name    string
	Left    string
	Right   string
	Changed bool
}

// CompareViewProps configures a CompareView for one frame (UK-02).
type CompareViewProps struct {
	LeftTitle  string
	RightTitle string
	Rows       []CompareRow
}

// CompareViewState holds per-row TagStates for changed badges (UK-02).
type CompareViewState struct {
	tags []*TagState
}

// NewCompareViewState builds a CompareViewState.
func NewCompareViewState() *CompareViewState {
	return &CompareViewState{}
}

func (s *CompareViewState) tagFor(i int) *TagState {
	for len(s.tags) <= i {
		s.tags = append(s.tags, NewTagState())
	}
	return s.tags[i]
}

// Layout draws the two-column comparison panel and returns its dimensions.
func (s *CompareViewState) Layout(env *Env, gtx *layout.Context, p CompareViewProps) (layout.Dimensions, error) {
	w := gtx.Constraints.Max.X
	if w <= 0 {
		return layout.Dimensions{}, nil
	}
	pad := gtx.Dp(theme.SpaceMD)
	innerW := max(w-2*pad, 0)

	var firstErr error
	noteErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	rec := op.Record(gtx.Ops)
	y := 0
	y += s.drawHeader(env, gtx, innerW, pad, p)
	y += gtx.Dp(theme.SpaceXS)
	at(gtx, image.Pt(pad, pad+y), func(g *layout.Context) {
		dg := *g
		dg.Constraints = layout.Constraints{Max: image.Pt(innerW, g.Constraints.Max.Y)}
		Divider(env, &dg, DividerProps{})
	})
	y += max(gtx.Dp(theme.Hairline), 1) + gtx.Dp(theme.SpaceSM)

	for i := range p.Rows {
		if i > 0 {
			y += gtx.Dp(theme.SpaceSM)
		}
		h := s.drawRow(env, gtx, innerW, pad, pad+y, i, &p.Rows[i], noteErr)
		y += h
	}
	call := rec.Stop()

	boxH := pad + y + pad
	box := image.Rect(0, 0, w, boxH)
	c := env.Th.Chrome()
	rad := gtx.Dp(theme.RadiusLG)
	fillLayer(gtx, box, rad, c.SurfaceRaised, 1)
	strokeRRect(gtx.Ops, box, rad, float32(gtx.Dp(theme.Hairline)), c.BorderSubtle)
	call.Add(gtx.Ops)

	return layout.Dimensions{Size: image.Pt(w, boxH)}, firstErr
}

func (s *CompareViewState) drawHeader(env *Env, gtx *layout.Context, innerW, pad int, p CompareViewProps) int {
	colGap := gtx.Dp(theme.SpaceMD)
	colW := max((innerW-colGap)/2, 0)
	leftTitle := p.LeftTitle
	if leftTitle == "" {
		leftTitle = "Left"
	}
	rightTitle := p.RightTitle
	if rightTitle == "" {
		rightTitle = "Right"
	}
	c := env.Th.Chrome()
	var lh, rh int
	at(gtx, image.Pt(pad, pad), func(g *layout.Context) {
		lg := *g
		lg.Constraints = layout.Constraints{Max: image.Pt(colW, g.Constraints.Max.Y)}
		lh = Paragraph(env, &lg, theme.TypeLabel(), c.Fg, leftTitle).Size.Y
	})
	at(gtx, image.Pt(pad+colW+colGap, pad), func(g *layout.Context) {
		rg := *g
		rg.Constraints = layout.Constraints{Max: image.Pt(colW, g.Constraints.Max.Y)}
		rh = Paragraph(env, &rg, theme.TypeLabel(), c.Fg, rightTitle).Size.Y
	})
	return max(lh, rh)
}

func (s *CompareViewState) drawRow(env *Env, gtx *layout.Context, innerW, padX, topY, idx int, row *CompareRow, noteErr func(error)) int {
	rowPad := gtx.Dp(theme.SpaceSM)
	colInnerW := max(innerW-2*rowPad, 0)
	colGap := gtx.Dp(theme.SpaceMD)
	colW := max((colInnerW-colGap)/2, 0)

	c := env.Th.Chrome()
	warnTone := env.Th.StatusTone(theme.StatusWarning)

	rec := op.Record(gtx.Ops)
	ry := rowPad
	if row.Name != "" || row.Changed {
		headH := 0
		nameMaxW := colInnerW
		if row.Changed {
			tag := s.tagFor(idx)
			tagProps := TagProps{Variant: TagStatus, Status: theme.StatusWarning, Text: "changed"}
			tagDims, err := tag.Measure(env, gtx, tagProps)
			noteErr(err)
			tagX := max(padX+rowPad+colInnerW-tagDims.Size.X, padX+rowPad)
			nameMaxW = max(colInnerW-tagDims.Size.X-gtx.Dp(theme.SpaceSM), 0)
			at(gtx, image.Pt(tagX, topY+ry), func(g *layout.Context) {
				tg := *g
				tg.Constraints = layout.Constraints{Max: tagDims.Size}
				_, lErr := tag.Layout(env, &tg, tagProps)
				noteErr(lErr)
			})
			headH = max(headH, tagDims.Size.Y)
		}
		if row.Name != "" && nameMaxW > 0 {
			at(gtx, image.Pt(padX+rowPad, topY+ry), func(g *layout.Context) {
				ng := *g
				ng.Constraints = layout.Constraints{Max: image.Pt(nameMaxW, g.Constraints.Max.Y)}
				d := Paragraph(env, &ng, theme.TypeSmall(), c.FgMuted, row.Name)
				headH = max(headH, d.Size.Y)
			})
		}
		ry += headH + gtx.Dp(theme.SpaceXS)
	}

	leftText, leftColor := compareCellText(row.Left, c.Fg, c.FgSubtle)
	rightText, rightColor := compareCellText(row.Right, c.Fg, c.FgSubtle)
	var lh, rh int
	at(gtx, image.Pt(padX+rowPad, topY+ry), func(g *layout.Context) {
		lg := *g
		lg.Constraints = layout.Constraints{Max: image.Pt(colW, g.Constraints.Max.Y)}
		lh = Paragraph(env, &lg, theme.TypeBody(), leftColor, leftText).Size.Y
	})
	at(gtx, image.Pt(padX+rowPad+colW+colGap, topY+ry), func(g *layout.Context) {
		rg := *g
		rg.Constraints = layout.Constraints{Max: image.Pt(colW, g.Constraints.Max.Y)}
		rh = Paragraph(env, &rg, theme.TypeBody(), rightColor, rightText).Size.Y
	})
	colsH := max(lh, rh)
	if colsH > 0 {
		divX := padX + rowPad + colW + colGap/2
		at(gtx, image.Pt(divX, topY+ry), func(g *layout.Context) {
			dg := *g
			dg.Constraints = layout.Constraints{Max: image.Pt(max(gtx.Dp(theme.Hairline), 1), colsH)}
			Divider(env, &dg, DividerProps{Orientation: Vertical})
		})
	}
	ry += colsH + rowPad
	rowCall := rec.Stop()

	rowRect := image.Rect(padX, topY, padX+innerW, topY+ry)
	if row.Changed {
		fillLayer(gtx, rowRect, gtx.Dp(theme.RadiusMD), warnTone.Fill, 1)
	} else {
		fillLayer(gtx, rowRect, gtx.Dp(theme.RadiusMD), c.FillSoft, 1)
	}
	rowCall.Add(gtx.Ops)
	return ry
}

func compareCellText(val string, fg, subtle color.NRGBA) (string, color.NRGBA) {
	if val == "" {
		return "—", subtle
	}
	return val, fg
}
