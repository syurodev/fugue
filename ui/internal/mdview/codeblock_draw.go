package mdview

import (
	"image"
	"image/color"
	"io"
	"strconv"
	"strings"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/theme"
)

func (v *view) measureCodeRows(
	lines [][]markdown.Span,
	diffLines []DiffLine,
	visCount, codeAvailW int,
	fs flowStyle,
	isDiff, wrapped bool,
	lineH int,
) ([]*flow, int, int) {
	flows := make([]*flow, visCount)
	wrapW := unbounded
	if wrapped && codeAvailW > 0 {
		wrapW = codeAvailW
	}
	widest, totalH := 0, 0
	for i := range visCount {
		var spans []markdown.Span
		if isDiff {
			if diffLines[i].Text != "" {
				spans = []markdown.Span{{Text: diffLines[i].Text}}
			}
		} else if i < len(lines) {
			spans = lines[i]
		}
		f := v.flowFor(spans, wrapW, fs)
		flows[i] = f
		widest = max(widest, f.width)
		totalH += max(f.height, lineH)
	}
	return flows, widest, totalH
}

func (v *view) drawBlockHeader(
	env *kit.Env,
	gtx *layout.Context,
	st *CodeBlockState,
	p CodeBlockProps,
	w, pad int,
	showActions bool,
	res *CodeBlockResult,
) int {
	c := v.th.Chrome()
	title := p.Title
	if title == "" {
		title = "Output"
	}
	rowH := gtx.Dp(theme.ControlSM) + 2*gtx.Dp(theme.SpaceXS)
	off := op.Offset(image.Pt(pad, gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	inner := *gtx
	inner.Constraints = layout.Constraints{Min: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM)), Max: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM))}
	layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(inner,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if p.Icon == "" {
				return layout.Dimensions{}
			}
			local := gtx
			d, _ := env.Icon(&local, p.Icon, theme.IconSM, c.FgMuted)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if p.Icon == "" {
				return layout.Dimensions{}
			}
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return kit.Label(env, &local, theme.TypeLabel(), c.Fg, title)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if p.Format == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return kit.Label(env, &local, theme.TypeSmall(), c.FgSubtle, p.Format)
			})
		}),
		layout.Flexed(1, func(layout.Context) layout.Dimensions { return layout.Dimensions{} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !showActions {
				return layout.Dimensions{}
			}
			local := gtx
			return v.drawActionButtons(env, &local, st, p, res)
		}),
	)
	off.Pop()
	return rowH
}

func (v *view) drawFloatingActions(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p CodeBlockProps, w, pad int, res *CodeBlockResult) {
	rec := op.Record(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Constraints{Max: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM))}
	dims := v.drawActionButtons(env, &local, st, p, res)
	call := rec.Stop()
	x := max(w-pad-dims.Size.X, pad)
	y := gtx.Dp(theme.SpaceXS)
	box := image.Rect(x, y, x+dims.Size.X, y+dims.Size.Y)
	rad := gtx.Dp(theme.RadiusSM)
	paint.FillShape(gtx.Ops, v.th.Chrome().SurfaceRaised, clip.UniformRRect(box, rad).Op(gtx.Ops))
	paint.FillShape(gtx.Ops, v.th.Chrome().BorderSubtle, clip.Stroke{
		Path: clip.UniformRRect(box, rad).Path(gtx.Ops), Width: float32(max(gtx.Dp(theme.Hairline), 1)),
	}.Op())
	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	off.Pop()
}

func (v *view) drawActionButtons(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p CodeBlockProps, res *CodeBlockResult) layout.Dimensions {
	copied := !st.CopiedUntil.IsZero() && env.Now().Before(st.CopiedUntil)
	copyLabel, copyIcon := "Copy", kit.IconCopy
	if copied {
		copyLabel, copyIcon = "Copied", kit.IconCheck
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			ip := kit.IconButtonProps{
				Icon:    kit.IconWrapText,
				Label:   "Toggle line wrap",
				Variant: kit.IconButtonGhost,
				Size:    kit.SizeSM,
				Toggled: st.Wrapped,
			}
			clicked, _ := st.wrapBtn.Layout(env, &local, &ip)
			if clicked {
				st.Wrapped = !st.Wrapped
				res.WrapToggled = true
			}
			return st.wrapBtn.Measure(env, &local, &ip)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceXXS), 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			bp := kit.ButtonProps{Text: copyLabel, Icon: copyIcon, Variant: kit.Ghost, Size: kit.SizeSM}
			clicked, _ := st.copyBtn.Layout(env, &local, bp)
			if clicked {
				txt := PlainCopyText(p.Raw, p.IsDiff)
				gtx.Execute(clipboard.WriteCmd{Type: clipboardMIME, Data: io.NopCloser(strings.NewReader(txt))})
				st.CopiedUntil = env.Now().Add(copiedFlashDuration)
				env.Driver.WakeAt(st.CopiedUntil)
				res.Copied = true
			}
			return st.copyBtn.Measure(env, &local, bp)
		}),
	)
}

func (v *view) drawCodeBody(
	gtx *layout.Context,
	st *CodeBlockState,
	p CodeBlockProps,
	flows []*flow,
	diffLines []DiffLine,
	visCount, pad, bodyTop, codeAvailW, viewH, gutterW, markerW, lineH int,
) {
	c := v.th.Chrome()
	bodyW := gutterW + markerW + codeAvailW
	bodyRect := image.Rect(pad, bodyTop, pad+bodyW, bodyTop+viewH)
	clipped := clip.Rect(bodyRect).Push(gtx.Ops)
	y := bodyTop - st.ScrollY
	codeX := pad + gutterW + markerW
	monoSmall := font.Font{Typeface: fonts.Mono}
	pxSmall := fixed.I(gtx.Sp(theme.TypeCodeSmall().Size))

	for i := range visCount {
		f := flows[i]
		rowH := max(f.height, lineH)
		if y+rowH < bodyTop || y > bodyTop+viewH {
			y += rowH
			continue
		}
		if p.IsDiff && i < len(diffLines) {
			dl := diffLines[i]
			rowBox := image.Rect(pad, y, pad+bodyW, y+rowH)
			switch dl.Kind {
			case DiffAdd:
				paint.FillShape(gtx.Ops, v.th.StatusTone(theme.StatusSuccess).Fill, clip.Rect(rowBox).Op())
			case DiffDel:
				paint.FillShape(gtx.Ops, v.th.StatusTone(theme.StatusDanger).Fill, clip.Rect(rowBox).Op())
			}
			if dl.LineNo > 0 && gutterW > 0 {
				numStr := strconv.Itoa(dl.LineNo)
				v.drawMonoGlyph(gtx, numStr, monoSmall, pxSmall, c.FgSubtle, pad, y, rowH)
			}
			if dl.Marker != "" && markerW > 0 {
				mc := c.FgSubtle
				if dl.Kind == DiffAdd {
					mc = v.th.Status().Success
				} else if dl.Kind == DiffDel {
					mc = v.th.Status().Danger
				}
				v.drawMonoGlyph(gtx, dl.Marker, monoSmall, pxSmall, mc, pad+gutterW, y, rowH)
			}
		} else if p.ShowLineNumbers && gutterW > 0 {
			v.drawMonoGlyph(gtx, strconv.Itoa(i+1), monoSmall, pxSmall, c.FgSubtle, pad, y, rowH)
		}

		codeClip := clip.Rect(image.Rect(codeX, y, codeX+codeAvailW, y+rowH)).Push(gtx.Ops)
		off := op.Offset(image.Pt(codeX-st.ScrollX, y)).Push(gtx.Ops)
		v.drawFlow(gtx, f, markdown.AlignNone, codeAvailW)
		off.Pop()
		codeClip.Pop()
		y += rowH
	}

	if st.maxScrollX > 0 && codeAvailW > 0 {
		thick := max(gtx.Dp(theme.SpaceXS), 2)
		thumbW := max(gtx.Dp(theme.SpaceXXL), codeAvailW*codeAvailW/(codeAvailW+st.maxScrollX))
		thumbW = min(thumbW, codeAvailW)
		tx := codeX + (codeAvailW-thumbW)*st.ScrollX/max(st.maxScrollX, 1)
		bar := image.Rect(tx, bodyTop+viewH-thick, tx+thumbW, bodyTop+viewH)
		paint.FillShape(gtx.Ops, c.FgSubtle, clip.UniformRRect(bar, thick/2).Op(gtx.Ops))
	}
	clipped.Pop()
}

func (v *view) drawMonoGlyph(gtx *layout.Context, txt string, f font.Font, px fixed.Int26_6, col color.NRGBA, x, y, rowH int) {
	a := v.shape(txt, f, px)
	if len(a.glyphs) == 0 {
		return
	}
	base := y + a.asc + (rowH-a.asc-a.desc)/2
	off := op.Offset(image.Pt(x, base)).Push(gtx.Ops)
	outline := clip.Outline{Path: v.shaper.Shape(a.glyphs)}.Op().Push(gtx.Ops)
	paint.ColorOp{Color: col}.Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	outline.Pop()
	off.Pop()
}

func (v *view) drawDividerAt(env *kit.Env, gtx *layout.Context, w, y int) int {
	off := op.Offset(image.Pt(0, y)).Push(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, gtx.Dp(theme.Hairline))}
	d := kit.Divider(env, &local, kit.DividerProps{Orientation: kit.Horizontal})
	off.Pop()
	return d.Size.Y
}

func (v *view) drawExpandFooter(env *kit.Env, gtx *layout.Context, st *CodeBlockState, w, pad, y int, res *CodeBlockResult, truncated bool) int {
	off := op.Offset(image.Pt(pad, y+gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Constraints{Max: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM))}
	bp := kit.ButtonProps{
		Text:    "Show full output",
		Variant: kit.Ghost,
		Size:    kit.SizeSM,
	}
	clicked, _ := st.expandBtn.Layout(env, &local, bp)
	dims := st.expandBtn.Measure(env, &local, bp)
	off.Pop()
	if clicked {
		st.Expanded = true
		res.RequestFull = truncated
	}
	return dims.Size.Y + 2*gtx.Dp(theme.SpaceXS)
}
