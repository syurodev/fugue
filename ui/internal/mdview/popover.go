package mdview

import (
	"image"
	"io"
	"strings"
	"unicode/utf8"

	"gioui.org/font"
	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	"golang.org/x/image/math/fixed"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
)

// linkState is the popover state of one link: whether the pointer is over
// the link or over its popover, and the popover's two buttons. Pinning lives
// in State, so only one popover is pinned at a time.
type linkState struct {
	hover    bool
	popHover bool
	popTag   byte
	open     widget.Clickable
	copy     widget.Clickable
}

const (
	popoverWidth unit.Dp = 340
	popoverPad   unit.Dp = 10
	// popoverSkirt is the strip between link and popover that still counts as
	// the popover, so the pointer can travel from one to the other.
	popoverSkirt  unit.Dp = 6
	clipboardMIME         = "application/text"
)

// middleTruncate shortens url to about cols runes by cutting the middle of
// what follows the host: the scheme and the host (host, as the URL spells it)
// always stay whole, even past cols. A URL without the host, such as mailto,
// is cut in the middle as a whole.
func middleTruncate(url, host string, cols int) string {
	if utf8.RuneCountInString(url) <= cols {
		return url
	}
	head, tail := "", url
	if i := strings.Index(url, host); host != "" && i >= 0 {
		head, tail = url[:i+len(host)], url[i+len(host):]
	}
	room := cols - utf8.RuneCountInString(head)
	if room < 3 {
		return head + "…"
	}
	r := []rune(tail)
	if len(r) <= room {
		return url
	}
	keepEnd := (room - 1) / 2
	keepStart := room - 1 - keepEnd
	return head + string(r[:keepStart]) + "…" + string(r[len(r)-keepEnd:])
}

// visible reports whether a link's popover shows: the pointer is over the
// link or the popover, keyboard focus is on the link or in the popover, or
// the popover is pinned. A pointer over one of the buttons counts as over
// the popover: the buttons sit above the popover's own hover area.
func (v *view) visible(gtx *layout.Context, lk *markdown.Link, ls *linkState) bool {
	return ls.hover || ls.popHover || gtx.Focused(ls) || gtx.Focused(&ls.open) || gtx.Focused(&ls.copy) ||
		ls.open.Hovered() || ls.copy.Hovered() || v.st.pinned == lk
}

// popover handles the popover's buttons and, while it is visible, draws it
// deferred above the link, or below it when the document has no room above.
func (v *view) popover(gtx *layout.Context, an *linkAnchor, avail int) {
	ls := v.st.link(an.link)
	for ls.open.Clicked(*gtx) {
		v.st.opens = append(v.st.opens, OpenLink{URL: an.link.URL})
	}
	for ls.copy.Clicked(*gtx) {
		gtx.Execute(clipboard.WriteCmd{Type: clipboardMIME, Data: io.NopCloser(strings.NewReader(an.link.URL))})
	}
	if !v.visible(gtx, an.link, ls) {
		return
	}
	sized := *gtx
	sized.Constraints.Max.X = min(avail, gtx.Dp(popoverWidth))
	card := op.Record(gtx.Ops)
	dims := v.card(&sized, an.link, ls)
	call := card.Stop()
	skirt := gtx.Dp(popoverSkirt)
	box := an.boxes[0]
	x := max(0, min(box.Min.X, avail-dims.Size.X))
	y, hit := box.Min.Y-skirt-dims.Size.Y, image.Rect(0, 0, dims.Size.X, dims.Size.Y+skirt)
	if y < 0 {
		y, hit = box.Max.Y+skirt, image.Rect(0, -skirt, dims.Size.X, dims.Size.Y)
	}
	placed := op.Record(gtx.Ops)
	off := op.Offset(image.Pt(x, y)).Push(gtx.Ops)
	area := clip.Rect(hit).Push(gtx.Ops)
	event.Op(gtx.Ops, &ls.popTag)
	area.Pop()
	call.Add(gtx.Ops)
	off.Pop()
	op.Defer(gtx.Ops, placed.Stop())
}

// card lays out the popover card: a generic link icon, the host (and its
// punycode form when that differs), the middle-truncated URL and the open and
// copy buttons. Nothing here loads anything: no favicon, no network.
func (v *view) card(gtx *layout.Context, lk *markdown.Link, ls *linkState) layout.Dimensions {
	c := v.th.Chrome()
	mt := v.th.Material()
	pad := gtx.Dp(popoverPad)
	inner := *gtx
	inner.Constraints.Min = image.Point{}
	body := op.Record(gtx.Ops)
	dims := layout.UniformInset(popoverPad).Layout(inner, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return v.hostRow(&gtx, lk)
			}),
			layout.Rigid(layout.Spacer{Height: 6}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				mono := fixed.I(gtx.Sp(12))
				cols := max(16, gtx.Constraints.Max.X/max(v.shape(" ", font.Font{Typeface: fonts.Mono}, mono).w, 1))
				lbl := material.Label(mt, 12, middleTruncate(lk.URL, lk.HostPunycode, cols))
				lbl.Font.Typeface, lbl.Color = fonts.Mono, c.FgMuted
				return lbl.Layout(gtx)
			}),
			layout.Rigid(layout.Spacer{Height: 8}.Layout),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
					layout.Rigid(v.button(&ls.open, "Open", true)),
					layout.Rigid(layout.Spacer{Width: 8}.Layout),
					layout.Rigid(v.button(&ls.copy, "Copy", false)),
				)
			}),
		)
	})
	call := body.Stop()
	box := image.Rectangle{Max: dims.Size}
	paint.FillShape(gtx.Ops, c.SurfaceRaised, clip.UniformRRect(box, pad/2).Op(gtx.Ops))
	v.outline(gtx, box, c.Border)
	call.Add(gtx.Ops)
	return dims
}

// hostLines are the popover's host lines: the display host, and the punycode
// form beneath it only when it differs. A link without a host (mailto) says
// what it is instead.
func hostLines(lk *markdown.Link) (host, punycode string) {
	host = lk.Host
	if host == "" {
		host = "Email address"
	}
	if lk.HostPunycode != lk.Host {
		punycode = lk.HostPunycode
	}
	return host, punycode
}

// hostRow is the icon with the host whole: the display form, and under it the
// punycode form when the display host is an IDN.
func (v *view) hostRow(gtx *layout.Context, lk *markdown.Link) layout.Dimensions {
	c, mt := v.th.Chrome(), v.th.Material()
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Start}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: 8, Top: 2}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return v.linkIcon(&gtx)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			host, puny := hostLines(lk)
			name := material.Label(mt, 14, host)
			name.Font.Weight, name.Color = font.Bold, c.Fg
			rows := []layout.FlexChild{layout.Rigid(name.Layout)}
			if puny != "" {
				lbl := material.Label(mt, 12, puny)
				lbl.Font.Typeface, lbl.Color = fonts.Mono, c.FgMuted
				rows = append(rows, layout.Rigid(lbl.Layout))
			}
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx, rows...)
		}),
	)
}

// button is one popover button: Open takes the action fill, Copy the quiet
// tinted one.
func (v *view) button(c *widget.Clickable, label string, primary bool) layout.Widget {
	return func(gtx layout.Context) layout.Dimensions {
		b := material.Button(v.th.Material(), c, label)
		b.TextSize = 13
		b.Inset = layout.Inset{Top: 4, Bottom: 4, Left: 12, Right: 12}
		if !primary {
			b.Background, b.Color = v.th.Chrome().SurfaceTinted, v.th.Chrome().Fg
		}
		return b.Layout(gtx)
	}
}

// linkIcon is the generic link glyph, two interlocked pills; it is drawn, not
// fetched.
func (v *view) linkIcon(gtx *layout.Context) layout.Dimensions {
	size := gtx.Dp(16)
	h := size * 3 / 8
	w := float32(max(gtx.Dp(unit.Dp(1.5)), 1))
	for _, x := range []int{0, size * 6 / 16} {
		box := image.Rect(x, (size-h)/2, x+size*10/16, (size+h)/2)
		paint.FillShape(gtx.Ops, v.th.Chrome().IconMuted, clip.Stroke{Path: clip.UniformRRect(box, h/2).Path(gtx.Ops), Width: w}.Op())
	}
	return layout.Dimensions{Size: image.Pt(size, size)}
}
