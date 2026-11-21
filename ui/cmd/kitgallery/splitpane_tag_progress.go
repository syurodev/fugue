package main

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// The Tag, Progress & Divider, and SplitPane gallery pages (UK-09, F10.12).

const (
	numTagStatuses = 4
	numTagHues     = 8
	numTagEnvs     = 4
	numProgTones   = 7
	numSplitStates = 4
)

type f1012Pages struct {
	tags         []*kit.TagState
	progDet      [numProgTones]*kit.ProgressState
	outerSplit   *kit.SplitPaneState
	innerSplit   *kit.SplitPaneState
	outerRatio   float32
	innerRatio   float32
	splitSamples [2][numSplitStates]*kit.SplitPaneState
}

func newF1012Pages() f1012Pages {
	fp := f1012Pages{
		outerSplit: kit.NewSplitPaneState(),
		innerSplit: kit.NewSplitPaneState(),
		outerRatio: 0.40,
		innerRatio: 0.50,
	}
	for range 2 + numTagStatuses + numTagHues + numTagEnvs {
		fp.tags = append(fp.tags, kit.NewTagState())
	}
	for i := range fp.progDet {
		fp.progDet[i] = kit.NewProgressState()
	}
	for o := range fp.splitSamples {
		for s := range fp.splitSamples[o] {
			fp.splitSamples[o][s] = kit.NewSplitPaneState()
		}
	}
	return fp
}

type tagSpecimen struct {
	group string
	props kit.TagProps
}

const tagGroupNeutral = "neutral"

func tagSpecimens() []tagSpecimen {
	statuses := []struct {
		s    theme.StatusKind
		name string
	}{
		{theme.StatusInfo, "Info"},
		{theme.StatusSuccess, "Success"},
		{theme.StatusWarning, "Warning"},
		{theme.StatusDanger, "Danger"},
	}
	hueNames := [numTagHues]string{"Red", "Orange", "Yellow", "Green", "Teal", "Blue", "Violet", "Pink"}
	envs := []string{"prod", "stg", "dev", "preview"}
	out := make([]tagSpecimen, 0, 2+len(statuses)+len(hueNames)+len(envs))
	out = append(out,
		tagSpecimen{tagGroupNeutral, kit.TagProps{Variant: kit.TagNeutral, Text: "Neutral"}},
		tagSpecimen{tagGroupNeutral, kit.TagProps{Variant: kit.TagNeutral, Icon: kit.IconCheck, Text: "With icon"}},
	)
	for _, st := range statuses {
		out = append(out, tagSpecimen{"status", kit.TagProps{Variant: kit.TagStatus, Status: st.s, Text: st.name}})
	}
	for i, name := range hueNames {
		out = append(out, tagSpecimen{"hue", kit.TagProps{Variant: kit.TagHue, Hue: theme.Hue(i), Text: name}})
	}
	for _, env := range envs {
		out = append(out, tagSpecimen{"env", kit.TagProps{Variant: kit.TagEnv, Env: env, Text: env}})
	}
	return out
}

func (p *pages) layoutTags(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X,
		"Tag (§6.4): non-interactive pill (TypeSmall, radius.full, flat tinted fill, no border). Text always carries the meaning.") +
		gtx.Dp(theme.SpaceXL)

	specs := tagSpecimens()
	groups := []struct {
		key   string
		title string
	}{
		{tagGroupNeutral, "neutral (FillSoft)"},
		{"status", "status tokens"},
		{"hue", "palette hues"},
		{"env", "env tags (prod=danger, dev=neutral, stg/unknown=warning)"},
	}

	blocks := make([]block, 0, len(groups))
	for gi, grp := range groups {
		blocks = append(blocks, func(g *layout.Context, dry bool) image.Point {
			return p.tagGroupBlock(g, gi, grp.title, grp.key, specs, dry)
		})
	}
	p.flow(gtx, y, blocks)
}

func (p *pages) tagGroupBlock(gtx *layout.Context, gi int, title, groupKey string, specs []tagSpecimen, dry bool) image.Point {
	c := p.env.Th.Chrome()
	gap, wrap := gtx.Dp(theme.SpaceSM), gtx.Dp(kbdWrap)
	if !dry {
		p.caption(gtx, image.Point{}, title, c.FgMuted)
	}
	w := p.captionWidth(gtx, title)
	y := gtx.Sp(theme.TypeSmall().Line) + gtx.Dp(theme.SpaceSM)
	x, rowH := 0, 0
	for i, sp := range specs {
		if sp.group != groupKey {
			continue
		}
		size := p.measureTagSpecimen(gtx, i, sp.props)
		if x > 0 && x+size.X > wrap {
			x = 0
			y += rowH + gap
			rowH = 0
		}
		if !dry {
			p.drawTagSpecimen(gtx, gi, i, image.Pt(x, y), size, sp.props)
		}
		x += size.X + gap
		rowH = max(rowH, size.Y)
		w = max(w, x-gap)
	}
	return image.Pt(w, y+rowH)
}

func (p *pages) measureTagSpecimen(gtx *layout.Context, i int, props kit.TagProps) image.Point {
	dims, err := p.f1012.tags[i].Measure(p.env, gtx, props)
	if err != nil && p.err == nil {
		p.err = err
	}
	return dims.Size
}

func (p *pages) drawTagSpecimen(gtx *layout.Context, gi, i int, pos, size image.Point, props kit.TagProps) {
	at(gtx, pos, func(cg *layout.Context) {
		if _, err := p.f1012.tags[i].Layout(p.env, cg, props); err != nil && p.err == nil {
			p.err = err
		}
	})
	p.runs = append(p.runs, image.Rectangle{Min: pos, Max: pos.Add(size)}.Add(p.base))
	p.cells[cell{compTag, gi, i, props.Text}] = true
}

type progressGallerySpec struct {
	name      string
	tone      kit.ProgressTone
	value     float32
	secondary float32
	size      kit.LineSize
}

func progressSpecs() []progressGallerySpec {
	return []progressGallerySpec{
		{"one-tier · level < 70% (success)", kit.ProgressDefault, 0.35, 0, kit.LineMD},
		{"one-tier · info", kit.ProgressInfo, 0.55, 0, kit.LineMD},
		{"two-tier · 42% / gate 85% (success)", kit.ProgressDefault, 0.42, 0.85, kit.LineMD},
		{"two-tier · 78% / gate 85% (warning)", kit.ProgressDefault, 0.78, 0.85, kit.LineMD},
		{"over gate · 88% / gate 85% (danger)", kit.ProgressDefault, 0.88, 0.85, kit.LineMD},
		{"raised floor · 42% raised to warning", kit.ProgressWarning, 0.42, 0.85, kit.LineMD},
		{"compact sm (10 dp) · 60% / gate 85%", kit.ProgressDefault, 0.60, 0.85, kit.LineSM},
	}
}

func (p *pages) layoutProgressAndDivider(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X,
		"LineRow (§3.9), Progress (§6.5) and Divider (§6.6): rounded vertical lines anchored left with level tones, optional second tier, and hairline rules.") +
		gtx.Dp(theme.SpaceXL)

	barW := min(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(320)))
	gap := gtx.Dp(theme.SpaceMD)
	y = p.layoutLineRowAndProgress(gtx, y, barW, gap)
	p.layoutDividerSamples(gtx, y+gtx.Dp(theme.SpaceSM), barW, gap)
}

func (p *pages) layoutLineRowAndProgress(gtx *layout.Context, y, barW, gap int) int {
	c := p.env.Th.Chrome()
	y += p.caption(gtx, image.Pt(0, y), "LineRow — custom colors (no track)", c.FgMuted).Dy() + gtx.Dp(theme.SpaceXS)
	at(gtx, image.Pt(0, y), func(g *layout.Context) {
		cg := *g
		cg.Constraints = layout.Exact(image.Pt(barW, gtx.Dp(theme.LineHeight)))
		dims := kit.LineRow(p.env, &cg, kit.LineRowProps{
			Size:     kit.LineMD,
			Fallback: c.LineRest,
			ColorAt: func(i, _ int) color.NRGBA {
				if i%2 == 0 {
					return p.env.Th.Status().Info
				}
				return c.LineRest
			},
		})
		y += dims.Size.Y + gap
	})
	p.cells[cell{compLineRow, 0, 0, stRest}] = true

	y += p.caption(gtx, image.Pt(0, y), "Progress — one-tier & two-tier line rows", c.FgMuted).Dy() + gap
	for i, ps := range progressSpecs() {
		y += p.caption(gtx, image.Pt(0, y), ps.name, c.FgSubtle).Dy() + gtx.Dp(theme.SpaceXS)
		at(gtx, image.Pt(0, y), func(g *layout.Context) {
			cg := *g
			h := theme.LineHeight
			if ps.size == kit.LineSM {
				h = theme.LineHeightSM
			}
			cg.Constraints = layout.Exact(image.Pt(barW, gtx.Dp(h)))
			dims := p.f1012.progDet[i].Layout(p.env, &cg, kit.ProgressProps{
				Value: ps.value, Secondary: ps.secondary, Tone: ps.tone, Size: ps.size,
			})
			y += dims.Size.Y + gap
		})
		p.cells[cell{compProgress, i, 0, "determinate"}] = true
	}
	return y
}

func (p *pages) layoutDividerSamples(gtx *layout.Context, y, barW, gap int) {
	c := p.env.Th.Chrome()
	y += p.caption(gtx, image.Pt(0, y), "Divider — Horizontal and Vertical (Border)", c.FgMuted).Dy() + gap
	at(gtx, image.Pt(0, y), func(g *layout.Context) {
		cg := *g
		cg.Constraints = layout.Exact(image.Pt(barW, gtx.Dp(theme.Hairline)))
		dims := kit.Divider(p.env, &cg, kit.DividerProps{Orientation: kit.Horizontal})
		y += dims.Size.Y + gap
	})
	p.cells[cell{compDivider, 0, 0, "horizontal"}] = true

	at(gtx, image.Pt(0, y), func(g *layout.Context) {
		cg := *g
		cg.Constraints = layout.Exact(image.Pt(gtx.Dp(theme.Hairline), gtx.Dp(theme.ControlLG)))
		dims := kit.Divider(p.env, &cg, kit.DividerProps{Orientation: kit.Vertical})
		y += dims.Size.Y
	})
	p.cells[cell{compDivider, 1, 0, "vertical"}] = true
}

func splitStateNames() [numSplitStates]string {
	return [numSplitStates]string{stRest, stHover, "dragging", stFocus}
}

func (p *pages) layoutSplitPane(gtx *layout.Context) {
	p.base = p.origin
	y := p.paragraph(gtx, gtx.Constraints.Max.X,
		"SplitPane (§6.27, desktop-app §5.3): live nested Horizontal + Vertical splits and handle states (rest, hover, dragging, keyboard focus).") +
		gtx.Dp(theme.SpaceXL)

	c := p.env.Th.Chrome()
	status := fmt.Sprintf("Live nested — Outer H: %d%% | Inner V: %d%%",
		int(math.Round(float64(p.f1012.outerRatio)*100)),
		int(math.Round(float64(p.f1012.innerRatio)*100)))
	y += p.caption(gtx, image.Pt(0, y), status, c.FgMuted).Dy() + gtx.Dp(theme.SpaceMD)

	boxW := min(gtx.Constraints.Max.X, gtx.Dp(unit.Dp(520)))
	boxH := gtx.Dp(unit.Dp(200))
	p.layoutLiveNestedSplit(gtx, y, boxW, boxH)

	y += boxH + gtx.Dp(theme.SpaceXL)
	orients := []struct {
		name   string
		orient kit.Orientation
	}{
		{"Horizontal", kit.Horizontal},
		{"Vertical", kit.Vertical},
	}
	blocks := make([]block, 0, len(orients)*numSplitStates)
	for oi, o := range orients {
		for si, st := range splitStateNames() {
			blocks = append(blocks, func(g *layout.Context, dry bool) image.Point {
				return p.splitStateSpecimen(g, oi, o.name, o.orient, si, st, dry)
			})
		}
	}
	p.flow(gtx, y, blocks)
}

func (p *pages) layoutLiveNestedSplit(gtx *layout.Context, y, boxW, boxH int) {
	c := p.env.Th.Chrome()
	baseTop := p.origin.Add(image.Pt(0, y))
	at(gtx, image.Pt(0, y), func(g *layout.Context) {
		cg := *g
		cg.Constraints = layout.Exact(image.Pt(boxW, boxH))
		paint.FillShape(cg.Ops, c.SurfaceBase, clip.Rect{Max: image.Pt(boxW, boxH)}.Op())
		if r, ok, _ := p.f1012.outerSplit.Layout(p.env, &cg, kit.SplitPaneProps{
			Orientation:  kit.Horizontal,
			Ratio:        p.f1012.outerRatio,
			DefaultRatio: 0.40,
			MinFirst:     theme.SpaceXXXL * 2,
			MinSecond:    theme.SpaceXXXL * 3,
		},
			func(fg layout.Context) layout.Dimensions {
				p.base = baseTop
				return p.splitPaneBox(&fg, "Left pane (H)", c.SurfaceTinted)
			},
			func(sg layout.Context) layout.Dimensions {
				rightX := boxW - sg.Constraints.Max.X
				if ir, iok, _ := p.f1012.innerSplit.Layout(p.env, &sg, kit.SplitPaneProps{
					Orientation:  kit.Vertical,
					Ratio:        p.f1012.innerRatio,
					DefaultRatio: 0.50,
					MinFirst:     theme.SpaceXXXL * 2,
					MinSecond:    theme.SpaceXXXL * 2,
				},
					func(tg layout.Context) layout.Dimensions {
						p.base = baseTop.Add(image.Pt(rightX, 0))
						return p.splitPaneBox(&tg, "Top-right pane (V)", c.SurfaceBase)
					},
					func(bg layout.Context) layout.Dimensions {
						botY := boxH - bg.Constraints.Max.Y
						p.base = baseTop.Add(image.Pt(rightX, botY))
						return p.splitPaneBox(&bg, "Bottom-right pane (V)", c.SurfaceRaised)
					},
				); iok {
					p.f1012.innerRatio = ir
				}
				return layout.Dimensions{Size: sg.Constraints.Max}
			},
		); ok {
			p.f1012.outerRatio = r
		}
	})
}

func (p *pages) splitStateSpecimen(gtx *layout.Context, oi int, orientName string, orient kit.Orientation, si int, stateName string, dry bool) image.Point {
	c := p.env.Th.Chrome()
	title := orientName + " · " + stateName
	boxW := max(gtx.Dp(unit.Dp(120)), p.captionWidth(gtx, title))
	boxH := gtx.Dp(unit.Dp(64))
	capH := gtx.Sp(theme.TypeSmall().Line) + gtx.Dp(theme.SpaceXS)
	if dry {
		return image.Pt(boxW, capH+boxH)
	}
	p.caption(gtx, image.Point{}, title, c.FgMuted)
	sp := p.f1012.splitSamples[oi][si]
	saved := p.env.Preview
	switch stateName {
	case stHover:
		p.env.Preview = kit.Preview{Hover: sp.Tag()}
	case "dragging":
		p.env.Preview = kit.Preview{Press: sp.Tag()}
	case stFocus:
		p.env.Preview = kit.Preview{Focus: sp.Tag()}
	}
	at(gtx, image.Pt(0, capH), func(g *layout.Context) {
		cg := *g
		cg.Constraints = layout.Exact(image.Pt(boxW, boxH))
		paint.FillShape(cg.Ops, c.SurfaceTinted, clip.Rect{Max: image.Pt(boxW, boxH)}.Op())
		sp.Layout(p.env, &cg, kit.SplitPaneProps{Orientation: orient, Ratio: 0.5}, nil, nil)
	})
	p.env.Preview = saved
	p.cells[cell{compSplitPane, oi, 0, stateName}] = true
	return image.Pt(boxW, capH+boxH)
}

func (p *pages) splitPaneBox(gtx *layout.Context, title string, bg color.NRGBA) layout.Dimensions {
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, bg, clip.Rect{Max: size}.Op())
	pad := gtx.Dp(theme.SpaceMD)
	p.caption(gtx, image.Pt(pad, pad), title, p.env.Th.Chrome().Fg)
	return layout.Dimensions{Size: size}
}
