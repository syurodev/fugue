//nolint:gocritic // Gio passes layout.Context by value by design; hugeParam flags it.
package main

import (
	"context"
	"fmt"
	"log/slog"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// contentWidth caps the reading column.
const contentWidth unit.Dp = 780

// preview is the window's state: the document, its view state, the scroll
// list and the theme toggle.
type preview struct {
	doc    *markdown.Doc
	state  mdview.State
	list   widget.List
	toggle widget.Clickable
	light  theme.Theme
	dark   theme.Theme
	isDark bool
}

// run opens the preview window and runs its event loop until it is closed.
func run(ctx context.Context, doc *markdown.Doc) error {
	shaper, err := fonts.Shaper()
	if err != nil {
		return fmt.Errorf("build the text shaper: %w", err)
	}
	p := &preview{doc: doc, light: theme.Light(shaper), dark: theme.Dark(shaper)}
	p.list.Axis = layout.Vertical
	w := new(app.Window)
	w.Option(app.Title("Fugue markdown preview"), app.Size(unit.Dp(900), unit.Dp(800)))
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			p.frame(ctx, gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// frame lays the window out and hands queued open requests to the opener.
func (p *preview) frame(ctx context.Context, gtx layout.Context) {
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModCtrl, Name: key.Name("D")})
		if !ok {
			break
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			p.isDark = !p.isDark
		}
	}
	for p.toggle.Clicked(gtx) {
		p.isDark = !p.isDark
	}
	th := &p.light
	if p.isDark {
		th = &p.dark
	}
	paint.Fill(gtx.Ops, th.Chrome().CanvasBg)
	layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.UniformInset(8).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return material.Button(th.Material(), &p.toggle, "Light / dark (Ctrl+D)").Layout(gtx)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			list := material.List(th.Material(), &p.list)
			return list.Layout(gtx, 1, func(gtx layout.Context, _ int) layout.Dimensions {
				gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-gtx.Dp(48), gtx.Dp(contentWidth))
				return layout.UniformInset(24).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					return mdview.Layout(gtx, th, p.doc, &p.state)
				})
			})
		}),
	)
	for {
		req, ok := p.state.PollOpen()
		if !ok {
			return
		}
		if err := openURL(ctx, req.URL); err != nil {
			slog.Warn("mdpreview: open link", "err", err)
		}
	}
}
