package appwin

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"

	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/theme"
	"github.com/syurodev/fugue/ui/internal/workspace"
)

// The policy card's diff panel: one soft panel holding one aligned row per
// changed key. The key is monospace and muted, the old value muted and
// struck through, the new value medium weight in Fg. The words carry the
// meaning ("unset", "removed" included); the muting and the strike only
// support them (ui-kit §6.1).

const (
	diffUnset   = "unset"
	diffRemoved = "removed"
	diffArrow   = "→"
	// diffKeyShare is the percent of the panel's inner width past which a
	// key stops sharing its row with the values and takes a line of its
	// own. A policy key is never truncated: the user must see which key a
	// change is about to touch.
	diffKeyShare = 45
)

// diffRow is one entry's text, measured at the panel's metrics.
type diffRow struct {
	key, old, new string
	oldW, arrowW  int
	newW, lineH   int
	inline        bool
	env           *kit.Env
}

// diffText returns the shown form of a value: an empty one is spoken, never
// drawn as a blank.
func diffText(v, empty string) string {
	if v == "" {
		return empty
	}
	return v
}

func mediumSmall() theme.Type {
	t := theme.TypeSmall()
	t.Weight = font.Medium
	return t
}

// layoutDiffPanel draws the panel at the constraints' width and returns its
// size.
func layoutDiffPanel(env *kit.Env, gtx *layout.Context, diff []workspace.DiffEntry) layout.Dimensions {
	pad := gtx.Dp(theme.SpaceLG)
	colGap := gtx.Dp(theme.SpaceMD)
	rowGap := gtx.Dp(theme.SpaceSM)
	w := gtx.Constraints.Max.X
	innerW := w - 2*pad
	chrome := env.Th.Chrome()

	innerGap := colGap / 2
	line := kit.MeasureLabel(env, gtx, theme.TypeSmall(), diffArrow).Y
	arrowW := kit.MeasureLabel(env, gtx, theme.TypeSmall(), diffArrow).X
	keyW, valW := 0, 0
	for _, e := range diff {
		keyW = max(keyW, kit.MeasureLabel(env, gtx, theme.TypeCodeSmall(), e.Key).X)
		r := measureDiffRow(env, gtx, e, 0, innerGap, line)
		valW = max(valW, r.oldW+innerGap+arrowW+innerGap+r.newW)
	}
	// Keys keep their full width beside the values while that fits or the
	// key is short enough to leave the values room; a longer key is stacked
	// above its values and wraps itself, so it is shown whole.
	stack := keyW+colGap+valW > innerW && keyW > innerW*diffKeyShare/100
	valX := keyW + colGap
	if stack {
		keyW, valX = innerW, 0
	}
	valW = innerW - valX

	rec := op.Record(gtx.Ops)
	y := 0
	for i, e := range diff {
		if i > 0 {
			y += rowGap
		}
		r := measureDiffRow(env, gtx, e, valW, innerGap, line)
		y += drawDiffRow(env, gtx, r, image.Pt(0, y), diffCols{keyW: keyW, valX: valX, valW: valW, stack: stack}, innerGap, chrome)
	}
	content := rec.Stop()

	size := image.Pt(w, y+2*pad)
	shape := clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(theme.RadiusMD)).Op(gtx.Ops)
	paint.FillShape(gtx.Ops, chrome.FillSoft, shape)
	off := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	content.Add(gtx.Ops)
	off.Pop()
	return layout.Dimensions{Size: size}
}

// measureDiffRow decides whether old, arrow and new share one line of the
// value column or stack: a long value wraps instead of being cut, because a
// policy decision must show the whole value.
func measureDiffRow(env *kit.Env, gtx *layout.Context, e workspace.DiffEntry, valW, gap, line int) diffRow {
	r := diffRow{key: e.Key, old: diffText(e.OldValue, diffUnset), new: diffText(e.NewValue, diffRemoved), lineH: line, env: env}
	r.oldW = kit.MeasureLabel(env, gtx, theme.TypeSmall(), r.old).X
	r.arrowW = kit.MeasureLabel(env, gtx, theme.TypeSmall(), diffArrow).X
	r.newW = kit.MeasureLabel(env, gtx, mediumSmall(), r.new).X
	r.inline = r.oldW+gap+r.arrowW+gap+r.newW <= valW
	return r
}

// diffCols is the panel's column geometry: the key's width, where the values
// start and how wide they are, and whether the key sits above them.
type diffCols struct {
	keyW, valX, valW int
	stack            bool
}

// drawDiffRow draws one row at at and returns its height.
func drawDiffRow(env *kit.Env, gtx *layout.Context, r diffRow, at image.Point, c diffCols, gap int, chrome theme.Chrome) int {
	place := func(p image.Point, draw func(g *layout.Context) layout.Dimensions) layout.Dimensions {
		defer op.Offset(at.Add(p)).Push(gtx.Ops).Pop()
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
		return draw(&g)
	}
	keyH := place(image.Point{}, func(g *layout.Context) layout.Dimensions {
		g.Constraints.Max.X = c.keyW
		return kit.Paragraph(env, g, theme.TypeCodeSmall(), chrome.FgMuted, r.key)
	}).Size.Y
	vy := 0
	if c.stack {
		vy = keyH
	}
	return max(keyH, vy+drawDiffValues(place, r, image.Pt(c.valX, vy), c.valW, gap, chrome))
}

// drawDiffValues draws a row's old and new values at p, side by side when
// they fit and stacked otherwise, and returns their height.
func drawDiffValues(place func(image.Point, func(*layout.Context) layout.Dimensions) layout.Dimensions, r diffRow, p image.Point, valW, gap int, chrome theme.Chrome) int {
	env := r.env
	if r.inline {
		place(p, func(g *layout.Context) layout.Dimensions {
			dims := kit.Label(env, g, theme.TypeSmall(), chrome.FgMuted, r.old)
			strikeThrough(g, dims.Size, chrome)
			return dims
		})
		x := p.X + r.oldW + gap
		place(image.Pt(x, p.Y), func(g *layout.Context) layout.Dimensions {
			return kit.Label(env, g, theme.TypeSmall(), chrome.FgSubtle, diffArrow)
		})
		place(image.Pt(x+r.arrowW+gap, p.Y), func(g *layout.Context) layout.Dimensions {
			return kit.Label(env, g, mediumSmall(), chrome.Fg, r.new)
		})
		return r.lineH
	}
	oldH := place(p, func(g *layout.Context) layout.Dimensions {
		g.Constraints.Max.X = valW
		return kit.Paragraph(env, g, theme.TypeSmall(), chrome.FgMuted, r.old)
	}).Size.Y
	if oldH <= r.lineH {
		place(p, func(g *layout.Context) layout.Dimensions {
			strikeThrough(g, image.Pt(r.oldW, oldH), chrome)
			return layout.Dimensions{}
		})
	}
	place(image.Pt(p.X, p.Y+oldH), func(g *layout.Context) layout.Dimensions {
		return kit.Label(env, g, theme.TypeSmall(), chrome.FgSubtle, diffArrow)
	})
	newH := place(image.Pt(p.X+r.arrowW+gap, p.Y+oldH), func(g *layout.Context) layout.Dimensions {
		g.Constraints.Max.X = valW - r.arrowW - gap
		return kit.Paragraph(env, g, mediumSmall(), chrome.Fg, r.new)
	}).Size.Y
	return oldH + newH
}

// strikeThrough draws a hairline through the middle of a text box of size.
func strikeThrough(gtx *layout.Context, size image.Point, chrome theme.Chrome) {
	h := max(gtx.Dp(theme.Hairline), 1)
	y := size.Y/2 - h/2
	paint.FillShape(gtx.Ops, chrome.FgMuted, clip.Rect(image.Rect(0, y, size.X, y+h)).Op())
}
