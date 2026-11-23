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
	// diffKeyShare is the least percent of the panel's inner width a
	// capped key column keeps, so long values stack instead of the key
	// being cut.
	diffKeyShare = 45
)

// diffRow is one entry's text, measured at the panel's metrics.
type diffRow struct {
	key, old, new string
	oldW, arrowW  int
	newW, lineH   int
	inline        bool
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
	// Keys keep their full width while the values still fit one line beside
	// them; past that the key column is capped and long values stack.
	if keyW+colGap+valW > innerW {
		keyW = min(keyW, max(innerW*diffKeyShare/100, innerW-colGap-valW))
	}
	valX := keyW + colGap
	valW = innerW - valX

	rec := op.Record(gtx.Ops)
	y := 0
	for i, e := range diff {
		if i > 0 {
			y += rowGap
		}
		r := measureDiffRow(env, gtx, e, valW, innerGap, line)
		y += drawDiffRow(env, gtx, r, image.Pt(0, y), keyW, valX, valW, innerGap, chrome)
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
	r := diffRow{key: e.Key, old: diffText(e.OldValue, diffUnset), new: diffText(e.NewValue, diffRemoved), lineH: line}
	r.oldW = kit.MeasureLabel(env, gtx, theme.TypeSmall(), r.old).X
	r.arrowW = kit.MeasureLabel(env, gtx, theme.TypeSmall(), diffArrow).X
	r.newW = kit.MeasureLabel(env, gtx, mediumSmall(), r.new).X
	r.inline = r.oldW+gap+r.arrowW+gap+r.newW <= valW
	return r
}

// drawDiffRow draws one row at at and returns its height.
func drawDiffRow(env *kit.Env, gtx *layout.Context, r diffRow, at image.Point, keyW, valX, valW, gap int, chrome theme.Chrome) int {
	place := func(p image.Point, draw func(g *layout.Context) layout.Dimensions) layout.Dimensions {
		defer op.Offset(at.Add(p)).Push(gtx.Ops).Pop()
		g := *gtx
		g.Constraints = layout.Constraints{Max: image.Pt(1<<20, 1<<20)}
		return draw(&g)
	}
	place(image.Point{}, func(g *layout.Context) layout.Dimensions {
		g.Constraints.Max.X = keyW
		return kit.MiddleEllipsisLabel(env, g, theme.TypeCodeSmall(), chrome.FgMuted, r.key)
	})
	if r.inline {
		place(image.Pt(valX, 0), func(g *layout.Context) layout.Dimensions {
			dims := kit.Label(env, g, theme.TypeSmall(), chrome.FgMuted, r.old)
			strikeThrough(g, dims.Size, chrome)
			return dims
		})
		x := valX + r.oldW + gap
		place(image.Pt(x, 0), func(g *layout.Context) layout.Dimensions {
			return kit.Label(env, g, theme.TypeSmall(), chrome.FgSubtle, diffArrow)
		})
		place(image.Pt(x+r.arrowW+gap, 0), func(g *layout.Context) layout.Dimensions {
			return kit.Label(env, g, mediumSmall(), chrome.Fg, r.new)
		})
		return r.lineH
	}
	oldH := place(image.Pt(valX, 0), func(g *layout.Context) layout.Dimensions {
		g.Constraints.Max.X = valW
		return kit.Paragraph(env, g, theme.TypeSmall(), chrome.FgMuted, r.old)
	}).Size.Y
	if oldH <= r.lineH {
		place(image.Pt(valX, 0), func(g *layout.Context) layout.Dimensions {
			strikeThrough(g, image.Pt(r.oldW, oldH), chrome)
			return layout.Dimensions{}
		})
	}
	place(image.Pt(valX, oldH), func(g *layout.Context) layout.Dimensions {
		return kit.Label(env, g, theme.TypeSmall(), chrome.FgSubtle, diffArrow)
	})
	newH := place(image.Pt(valX+r.arrowW+gap, oldH), func(g *layout.Context) layout.Dimensions {
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
