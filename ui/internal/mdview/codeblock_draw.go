package mdview

import (
	"fmt"
	"image"
	"image/color"
	"io"
	"log/slog"
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

func logMDViewErr(err error) {
	if err != nil {
		slog.Default().Warn("mdview layout error", "err", err)
	}
}

func (v *view) measureCommandBlock(gtx *layout.Context, st *CodeBlockState, bodyMaxW int) ([]*flow, int) {
	if len(st.cachedCmdLn) == 0 {
		return nil, 0
	}
	fs := flowStyle{px: fixed.I(gtx.Sp(theme.TypeCode().Size)), mono: true, whole: false}
	lineH := max(gtx.Sp(theme.TypeCode().Line), fs.lineHeight())
	flows := make([]*flow, len(st.cachedCmdLn))
	totalH := 0
	for i, spans := range st.cachedCmdLn {
		f := v.flowFor(spans, max(bodyMaxW, 1), fs)
		flows[i] = f
		totalH += max(f.height, lineH)
	}
	return flows, totalH
}

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
			spans = diffLineSpans(&diffLines[i])
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

func diffLineSpans(dl *DiffLine) []markdown.Span {
	if len(dl.Spans) > 0 {
		return dl.Spans
	}
	if dl.Text == "" {
		return nil
	}
	return []markdown.Span{{Text: dl.Text}}
}

func resolvedHeader(p *CodeBlockProps) (icon kit.IconName, title, fmtLabel string) {
	switch {
	case p.Command != "":
		icon = kit.IconTerminal
		if p.Icon != "" {
			icon = p.Icon
		}
		return icon, "shell", ""
	case p.IsDiff:
		title = p.Title
		if title == "" {
			title = "diff"
		}
		icon = p.Icon
		if icon == "" {
			icon = FileIcon(p.Title)
			if p.Title == "" {
				icon = kit.IconPencil
			}
		}
		return icon, title, ""
	case p.IsOutputCard:
		icon = p.Icon
		if icon == "" {
			icon = kit.IconFileCode
		}
		return icon, "output", p.Format
	default:
		title = p.Format
		if title == "" {
			title = "code"
		}
		return kit.IconFileCode, title, ""
	}
}

func (v *view) drawBlockHeader(
	env *kit.Env,
	gtx *layout.Context,
	st *CodeBlockState,
	p *CodeBlockProps,
	w, pad int,
	showActions bool,
	res *CodeBlockResult,
) int {
	c := v.th.Chrome()
	icon, title, fmtLabel := resolvedHeader(p)
	rowH := gtx.Dp(theme.ControlSM) + 2*gtx.Dp(theme.SpaceXS)
	off := op.Offset(image.Pt(pad, gtx.Dp(theme.SpaceXS))).Push(gtx.Ops)
	inner := *gtx
	inner.Constraints = layout.Constraints{Min: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM)), Max: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM))}
	layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(inner,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			d, err := env.Icon(&local, icon, theme.IconSM, c.IconMuted)
			logMDViewErr(err)
			return d
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Dp(theme.SpaceSM), 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return kit.Label(env, &local, theme.TypeLabel(), c.Fg, title)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if fmtLabel == "" {
				return layout.Dimensions{}
			}
			return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				local := gtx
				return kit.Label(env, &local, theme.TypeSmall(), c.FgSubtle, fmtLabel)
			})
		}),
		// The spacer must take its share, or the status sits glued to the title.
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, 0)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return v.drawHeaderStatus(env, &local, p)
		}),
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

func (v *view) drawHeaderStatus(env *kit.Env, gtx *layout.Context, p *CodeBlockProps) layout.Dimensions {
	if p.ExitCode == nil && p.Duration == "" {
		return layout.Dimensions{}
	}
	c := v.th.Chrome()
	return layout.Inset{Right: theme.SpaceSM}.Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if p.ExitCode == nil {
					return layout.Dimensions{}
				}
				col := v.th.Status().Success
				if *p.ExitCode != 0 {
					col = v.th.Status().Danger
				}
				l := gtx
				return kit.Label(env, &l, theme.TypeSmall(), col, fmt.Sprintf("exit %d", *p.ExitCode))
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				if p.Duration == "" {
					return layout.Dimensions{}
				}
				return layout.Inset{Left: theme.SpaceSM}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					l := gtx
					return kit.Label(env, &l, theme.TypeSmall(), c.FgSubtle, p.Duration)
				})
			}),
		)
	})
}

func (v *view) drawActionButtons(env *kit.Env, gtx *layout.Context, st *CodeBlockState, p *CodeBlockProps, res *CodeBlockResult) layout.Dimensions {
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
			clicked, err := st.wrapBtn.Layout(env, &local, &ip)
			logMDViewErr(err)
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
			clicked, err := st.copyBtn.Layout(env, &local, bp)
			logMDViewErr(err)
			if clicked {
				txt := PlainCopyText(p.Raw, p.IsDiff)
				if txt == "" && p.Command != "" {
					txt = p.Command
				}
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
	p *CodeBlockProps,
	flows []*flow,
	diffLines []DiffLine,
	visCount, pad, bodyTop, codeAvailW, viewH, gutterW, markerW, lineH int,
) {
	bodyW := gutterW + markerW + codeAvailW
	bodyRect := image.Rect(pad, bodyTop, pad+bodyW, bodyTop+viewH)
	clipped := clip.Rect(bodyRect).Push(gtx.Ops)
	y := bodyTop - st.ScrollY
	codeX := pad + gutterW + markerW

	for i := range visCount {
		rowH := max(flows[i].height, lineH)
		if y+rowH < bodyTop || y > bodyTop+viewH {
			y += rowH
			continue
		}
		v.drawCodeRowChrome(gtx, p, diffLines, i, pad, y, bodyW, gutterW, markerW, rowH)
		codeClip := clip.Rect(image.Rect(codeX, y, codeX+codeAvailW, y+rowH)).Push(gtx.Ops)
		off := op.Offset(image.Pt(codeX-st.ScrollX, y)).Push(gtx.Ops)
		v.drawFlow(gtx, flows[i], markdown.AlignNone, codeAvailW)
		off.Pop()
		codeClip.Pop()
		y += rowH
	}

	v.drawHorizontalScrollbar(gtx, st, codeX, bodyTop, codeAvailW, viewH)
	clipped.Pop()
}

func (v *view) drawCodeRowChrome(
	gtx *layout.Context, p *CodeBlockProps, diffLines []DiffLine, idx, pad, y, bodyW, gutterW, markerW, rowH int,
) {
	c := v.th.Chrome()
	monoSmall := font.Font{Typeface: fonts.Mono}
	pxSmall := fixed.I(gtx.Sp(theme.TypeCodeSmall().Size))
	if !p.IsDiff || idx >= len(diffLines) {
		if p.ShowLineNumbers && gutterW > 0 {
			v.drawMonoGlyph(gtx, strconv.Itoa(idx+1), monoSmall, pxSmall, c.FgSubtle, pad+gtx.Dp(theme.SpaceSM), y, rowH)
		}
		return
	}
	dl := diffLines[idx]
	rowBox := image.Rect(pad, y, pad+bodyW, y+rowH)
	switch dl.Kind {
	case DiffAdd:
		paint.FillShape(gtx.Ops, v.th.StatusTone(theme.StatusSuccess).Fill, clip.Rect(rowBox).Op())
	case DiffDel:
		paint.FillShape(gtx.Ops, v.th.StatusTone(theme.StatusDanger).Fill, clip.Rect(rowBox).Op())
	case DiffContext, DiffMeta:
	}
	if dl.LineNo > 0 && gutterW > 0 {
		v.drawMonoGlyph(gtx, strconv.Itoa(dl.LineNo), monoSmall, pxSmall, c.FgSubtle, pad+gtx.Dp(theme.SpaceSM), y, rowH)
	}
	if dl.Marker != "" && markerW > 0 {
		// Centered in its own column, with room on both sides, so number,
		// marker and code read as three separate columns.
		glyphW := v.shape(dl.Marker, monoSmall, pxSmall).w
		v.drawMonoGlyph(gtx, dl.Marker, monoSmall, pxSmall, diffMarkerColor(v.th, dl.Kind), pad+gutterW+(markerW-glyphW)/2, y, rowH)
	}
}

func diffMarkerColor(th *theme.Theme, kind DiffKind) color.NRGBA {
	switch kind {
	case DiffAdd:
		return th.Status().Success
	case DiffDel:
		return th.Status().Danger
	case DiffContext, DiffMeta:
		return th.Chrome().FgSubtle
	}
	return th.Chrome().FgSubtle
}

func (v *view) drawHorizontalScrollbar(gtx *layout.Context, st *CodeBlockState, codeX, bodyTop, codeAvailW, viewH int) {
	if st.maxScrollX <= 0 || codeAvailW <= 0 {
		return
	}
	thick := max(gtx.Dp(theme.SpaceXS), 2)
	thumbW := max(gtx.Dp(theme.SpaceXXL), codeAvailW*codeAvailW/(codeAvailW+st.maxScrollX))
	thumbW = min(thumbW, codeAvailW)
	tx := codeX + (codeAvailW-thumbW)*st.ScrollX/max(st.maxScrollX, 1)
	bar := image.Rect(tx, bodyTop+viewH-thick, tx+thumbW, bodyTop+viewH)
	paint.FillShape(gtx.Ops, v.th.Chrome().FgSubtle, clip.UniformRRect(bar, thick/2).Op(gtx.Ops))
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

func (v *view) drawCommandHeader(env *kit.Env, gtx *layout.Context, flows []*flow, w, pad, topY, lineH int, hasOutput bool) int {
	y := topY + pad
	cmdAvailW := max(w-2*pad, 1)
	for _, f := range flows {
		rowH := max(f.height, lineH)
		off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
		v.drawFlow(gtx, f, markdown.AlignNone, cmdAvailW)
		off.Pop()
		y += rowH
	}
	y += gtx.Dp(theme.SpaceSM)
	if hasOutput {
		y += v.drawDividerAt(env, gtx, w, y)
	}
	return y
}

func (v *view) drawExpandAction(env *kit.Env, gtx *layout.Context, st *CodeBlockState, w, pad, y int, res *CodeBlockResult, truncated bool) int {
	off := op.Offset(image.Pt(pad, y)).Push(gtx.Ops)
	local := *gtx
	local.Constraints = layout.Constraints{Max: image.Pt(max(w-2*pad, 0), gtx.Dp(theme.ControlSM))}
	bp := kit.ButtonProps{
		Text:    "Show full output",
		Variant: kit.Ghost,
		Size:    kit.SizeSM,
	}
	clicked, err := st.expandBtn.Layout(env, &local, bp)
	logMDViewErr(err)
	dims := st.expandBtn.Measure(env, &local, bp)
	off.Pop()
	if clicked {
		st.Expanded = true
		res.RequestFull = truncated
	}
	return dims.Size.Y + gtx.Dp(theme.SpaceSM)
}
