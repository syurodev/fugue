package mdview

import (
	"image"
	"strconv"

	"gioui.org/f32"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

const (
	bodySize     unit.Sp = 15
	blockGap     unit.Dp = 10
	itemGap      unit.Dp = 4
	listIndent   unit.Dp = 26
	quoteIndent  unit.Dp = 14
	quoteBar     unit.Dp = 3
	codePad      unit.Dp = 10
	cellPadX     unit.Dp = 8
	cellPadY     unit.Dp = 4
	minColumn    unit.Dp = 48
	checkboxSize unit.Dp = 14
)

// headingSize is the text size of a heading level, 1 to 6.
func headingSize(level int) unit.Sp {
	return [...]unit.Sp{28, 22, 18, 16, 15, 14}[min(max(level, 1), 6)-1]
}

// Layout draws doc at the width of gtx.Constraints.Max.X and returns the size
// it took; the caller scrolls it. State holds everything that outlives a
// frame: laid-out text, popover and scroll state, and the queue of open
// requests (State.PollOpen).
func Layout(gtx *layout.Context, th *theme.Theme, doc *markdown.Doc, st *State) layout.Dimensions {
	v := &view{th: th, st: st, shaper: th.Shaper(), locale: gtx.Locale}
	st.sync(doc, v.shaper, gtx.Metric, th.Chrome())
	dims := v.blocks(gtx, doc.Blocks)
	if !doc.Truncated {
		return dims
	}
	off := op.Offset(image.Pt(0, dims.Size.Y+gtx.Dp(blockGap))).Push(gtx.Ops)
	note := material.Label(th.Material(), 13, "Output truncated")
	note.Color = th.Chrome().FgMuted
	n := note.Layout(*gtx)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(dims.Size.X, dims.Size.Y+gtx.Dp(blockGap)+n.Size.Y)}
}

// blocks stacks blocks with the block gap between them.
func (v *view) blocks(gtx *layout.Context, bs []markdown.Block) layout.Dimensions {
	y := 0
	for i := range bs {
		if i > 0 {
			y += gtx.Dp(blockGap)
		}
		off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		d := v.block(gtx, &bs[i])
		off.Pop()
		y += d.Size.Y
	}
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
}

// block draws one block.
func (v *view) block(gtx *layout.Context, b *markdown.Block) layout.Dimensions {
	body := flowStyle{px: fixed.I(gtx.Sp(bodySize))}
	switch b.Kind {
	case markdown.BlockParagraph:
		return v.text(gtx, b.Spans, body)
	case markdown.BlockHeading:
		return v.text(gtx, b.Spans, flowStyle{px: fixed.I(gtx.Sp(headingSize(b.Level))), bold: true})
	case markdown.BlockHTML:
		body.mono = true
		return v.text(gtx, b.Spans, body)
	case markdown.BlockThematicBreak:
		pad := gtx.Dp(blockGap) / 2
		paint.FillShape(gtx.Ops, v.th.Chrome().Border, clip.Rect(image.Rect(0, pad, gtx.Constraints.Max.X, pad+max(gtx.Dp(1), 1))).Op())
		return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 2*pad+max(gtx.Dp(1), 1))}
	case markdown.BlockCode:
		return v.code(gtx, b)
	case markdown.BlockQuote:
		return v.quote(gtx, b)
	case markdown.BlockList:
		return v.list(gtx, &b.List)
	case markdown.BlockTable:
		return v.table(gtx, &b.Table)
	}
	return layout.Dimensions{}
}

// text lays out and draws one run of spans at the full width.
func (v *view) text(gtx *layout.Context, spans []markdown.Span, fs flowStyle) layout.Dimensions {
	w := gtx.Constraints.Max.X
	f := v.flowFor(spans, w, fs)
	// A word wider than the block overflows its line; the block clips it.
	clipped := clip.Rect(image.Rect(0, 0, w, f.height)).Push(gtx.Ops)
	v.drawFlow(gtx, f, markdown.AlignNone, w)
	clipped.Pop()
	return layout.Dimensions{Size: image.Pt(w, f.height)}
}

// quote draws nested blocks behind a bar on the left.
func (v *view) quote(gtx *layout.Context, b *markdown.Block) layout.Dimensions {
	indent := gtx.Dp(quoteIndent)
	inner := *gtx
	inner.Constraints.Max.X = max(gtx.Constraints.Max.X-indent, 0)
	rec := op.Record(gtx.Ops)
	d := v.blocks(&inner, b.Blocks)
	call := rec.Stop()
	bar := image.Rect(0, 0, gtx.Dp(quoteBar), d.Size.Y)
	paint.FillShape(gtx.Ops, v.th.Chrome().Border, clip.Rect(bar).Op())
	off := op.Offset(image.Pt(indent, 0)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, d.Size.Y)}
}

// list draws items beside their markers: a bullet, a number or a read-only
// checkbox.
func (v *view) list(gtx *layout.Context, l *markdown.List) layout.Dimensions {
	indent := gtx.Dp(listIndent)
	inner := *gtx
	inner.Constraints.Max.X = max(gtx.Constraints.Max.X-indent, 0)
	y := 0
	for i := range l.Items {
		item := &l.Items[i]
		if i > 0 {
			y += gtx.Dp(itemGap)
		}
		off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		v.marker(gtx, l, i, indent)
		body := op.Offset(image.Pt(indent, 0)).Push(gtx.Ops)
		d := v.blocks(&inner, item.Blocks)
		body.Pop()
		off.Pop()
		y += max(d.Size.Y, gtx.Sp(bodySize)*3/2)
	}
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
}

// marker draws item i's marker in the indent column, level with the item's
// first line.
func (v *view) marker(gtx *layout.Context, l *markdown.List, i, indent int) {
	item := &l.Items[i]
	line := gtx.Sp(bodySize) * 3 / 2
	if item.Task {
		v.checkbox(gtx, item.Checked, line)
		return
	}
	label := "•"
	if l.Ordered {
		label = strconv.Itoa(l.Start+i) + "."
	}
	w := indent - gtx.Dp(6)
	spans := v.st.marker(item, label)
	f := v.flowFor(spans, w, flowStyle{px: fixed.I(gtx.Sp(bodySize))})
	cell := *gtx
	cell.Constraints.Max.X = w
	v.drawFlow(&cell, f, markdown.AlignRight, w)
}

// checkbox draws a read-only task box; a checked one is filled with a tick.
func (v *view) checkbox(gtx *layout.Context, checked bool, line int) {
	c := v.th.Chrome()
	size := gtx.Dp(checkboxSize)
	box := image.Rect(0, (line-size)/2, size, (line+size)/2)
	if !checked {
		v.outline(gtx, box, c.FgMuted)
		return
	}
	paint.FillShape(gtx.Ops, c.Action, clip.UniformRRect(box, gtx.Dp(3)).Op(gtx.Ops))
	var p clip.Path
	p.Begin(gtx.Ops)
	at := func(fx, fy float32) { p.LineTo(fPt(box, fx, fy)) }
	p.MoveTo(fPt(box, 0.25, 0.52))
	at(0.43, 0.7)
	at(0.76, 0.32)
	paint.FillShape(gtx.Ops, c.ActionFg, clip.Stroke{Path: p.End(), Width: float32(max(gtx.Dp(2), 1))}.Op())
}

// code draws a code block on its own rounded surface; long lines scroll
// sideways and never wrap, so no token is ever broken. A block that fits
// draws without the scroll widget.
func (v *view) code(gtx *layout.Context, b *markdown.Block) layout.Dimensions {
	pad := gtx.Dp(codePad)
	fs := flowStyle{px: fixed.I(gtx.Sp(bodySize)), mono: true, whole: true}
	inner := *gtx
	inner.Constraints.Max.X = max(gtx.Constraints.Max.X-2*pad, 0)
	inner.Constraints.Min = image.Point{X: inner.Constraints.Max.X}
	flows, widest := make([]*flow, len(b.Lines)), 0
	for i, spans := range b.Lines {
		flows[i] = v.flowFor(spans, unbounded, fs)
		widest = max(widest, flows[i].width)
	}
	rec := op.Record(gtx.Ops)
	var d layout.Dimensions
	if widest <= inner.Constraints.Max.X {
		d = v.codeLines(&inner, flows)
	} else {
		list := material.List(v.th.Material(), v.st.codeList(b))
		d = list.Layout(inner, 1, func(gtx layout.Context, _ int) layout.Dimensions {
			return v.codeLines(&gtx, flows)
		})
	}
	call := rec.Stop()
	size := image.Pt(gtx.Constraints.Max.X, d.Size.Y+2*pad)
	paint.FillShape(gtx.Ops, v.th.CodeSurface(), clip.UniformRRect(image.Rectangle{Max: size}, gtx.Dp(6)).Op(gtx.Ops))
	off := op.Offset(image.Pt(pad, pad)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
	return layout.Dimensions{Size: size}
}

// codeLines draws a code block's lines, one flow each, top to bottom.
func (v *view) codeLines(gtx *layout.Context, flows []*flow) layout.Dimensions {
	y, w := 0, 0
	for _, f := range flows {
		off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
		v.drawFlow(gtx, f, markdown.AlignNone, unbounded)
		off.Pop()
		y += f.height
		w = max(w, f.width)
	}
	return layout.Dimensions{Size: image.Pt(w, y)}
}

// table draws a GFM table: columns as wide as their widest cell, shrunk
// proportionally when the table is wider than the block, the header on the
// tinted surface and a hairline under every row.
func (v *view) table(gtx *layout.Context, t *markdown.Table) layout.Dimensions {
	rows := append([][]markdown.Cell{t.Header}, t.Rows...)
	px := fixed.I(gtx.Sp(bodySize))
	widths := v.columnWidths(gtx, rows, len(t.Aligns), px)
	padX, padY := gtx.Dp(cellPadX), gtx.Dp(cellPadY)
	total := 0
	for _, w := range widths {
		total += w
	}
	y := 0
	for r, row := range rows {
		fs := flowStyle{px: px, bold: r == 0}
		h := 0
		for c := range min(len(row), len(widths)) {
			h = max(h, v.flowFor(row[c].Spans, widths[c]-2*padX, fs).height+2*padY)
		}
		if r == 0 {
			paint.FillShape(gtx.Ops, v.th.Chrome().SurfaceTinted, clip.Rect(image.Rect(0, y, total, y+h)).Op())
		}
		x := 0
		for c := range min(len(row), len(widths)) {
			inner := *gtx
			inner.Constraints.Max.X = widths[c] - 2*padX
			off := op.Offset(image.Pt(x+padX, y+padY)).Push(gtx.Ops)
			v.drawFlow(&inner, v.flowFor(row[c].Spans, widths[c]-2*padX, fs), t.Aligns[c], inner.Constraints.Max.X)
			off.Pop()
			x += widths[c]
		}
		y += h
		paint.FillShape(gtx.Ops, v.th.Chrome().Border, clip.Rect(image.Rect(0, y-1, total, y)).Op())
	}
	return layout.Dimensions{Size: image.Pt(total, y)}
}

// columnWidths is each column's natural width, shrunk to fit the block.
func (v *view) columnWidths(gtx *layout.Context, rows [][]markdown.Cell, cols int, px fixed.Int26_6) []int {
	widths := make([]int, cols)
	sum := 0
	for r, row := range rows {
		for c := range min(len(row), cols) {
			f := v.flowFor(row[c].Spans, unbounded, flowStyle{px: px, bold: r == 0})
			widths[c] = max(widths[c], f.width+2*gtx.Dp(cellPadX))
		}
	}
	for _, w := range widths {
		sum += w
	}
	if limit := gtx.Constraints.Max.X; sum > limit && sum > 0 {
		for c := range widths {
			widths[c] = max(widths[c]*limit/sum, min(widths[c], gtx.Dp(minColumn)))
		}
	}
	return widths
}

// fPt is the point at fractions (fx, fy) of box.
func fPt(box image.Rectangle, fx, fy float32) f32.Point {
	return f32.Pt(float32(box.Min.X)+fx*float32(box.Dx()), float32(box.Min.Y)+fy*float32(box.Dy()))
}
