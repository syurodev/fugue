package main

import (
	"fmt"
	"log/slog"
	"runtime"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/unit"

	"github.com/syurodev/fugue/ui/internal/fonts"
	"github.com/syurodev/fugue/ui/internal/kit"
	"github.com/syurodev/fugue/ui/internal/markdown"
	"github.com/syurodev/fugue/ui/internal/mdview"
	"github.com/syurodev/fugue/ui/internal/theme"
)

// preview is the window's state: the document, its view state, the scroll
// area, the theme toggle and the mode the theme is in.
type preview struct {
	env    *kit.Env
	doc    *markdown.Doc
	state  mdview.State
	scroll kit.ScrollAreaState
	toggle *kit.ButtonState
	light  theme.Theme
	dark   theme.Theme
}

// run opens the preview window and runs its event loop until it is closed.
func run(doc *markdown.Doc) error {
	shaper, err := fonts.Shaper()
	if err != nil {
		return fmt.Errorf("build the text shaper: %w", err)
	}
	light, dark := theme.Light(shaper), theme.Dark(shaper)
	env := &kit.Env{Th: &light, Sh: shaper}
	if runtime.GOOS == "darwin" {
		env.Platform = kit.PlatformMac
	}
	p := &preview{env: env, doc: doc, scroll: *kit.NewScrollAreaState(), toggle: kit.NewButtonState(), light: light, dark: dark}
	w := new(app.Window)
	w.Option(app.Title("Fugue markdown preview"), app.Size(unit.Dp(900), unit.Dp(800)))
	env.Invalidate = w.Invalidate
	var ops op.Ops
	for {
		switch e := w.Event().(type) {
		case app.DestroyEvent:
			return e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			gtx.Metric = env.Metric(gtx.Metric)
			env.Begin(gtx.Now)
			env.TrackInput(&gtx)
			p.frame(&gtx)
			env.FrameEnd(&gtx)
			e.Frame(gtx.Ops)
		}
	}
}

// frame lays the window out and hands queued open requests to the opener.
func (p *preview) frame(gtx *layout.Context) {
	p.toggleKeys(gtx)
	paint.Fill(gtx.Ops, p.env.Th.Chrome().CanvasBg)
	layout.Flex{Axis: layout.Vertical}.Layout(*gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return layout.UniformInset(theme.SpaceMD).Layout(local, func(gtx layout.Context) layout.Dimensions {
				l := gtx
				return p.toggleButton(&l)
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			local := gtx
			return p.scroll.Layout(p.env, &local, kit.ScrollAreaProps{Axis: layout.Vertical}, p.document)
		}),
	)
	p.openLinks()
}

// toggleKeys drains the window's theme-toggle chord.
func (p *preview) toggleKeys(gtx *layout.Context) {
	for {
		ke, ok := gtx.Event(key.Filter{Required: key.ModCtrl, Name: key.Name("D")})
		if !ok {
			break
		}
		if k, isKey := ke.(key.Event); isKey && k.State == key.Press {
			p.toggleTheme()
		}
	}
}

// toggleButton draws the theme toggle and acts on its click.
func (p *preview) toggleButton(gtx *layout.Context) layout.Dimensions {
	clicked, err := p.toggle.Layout(p.env, gtx, kit.ButtonProps{Text: "Light / dark (Ctrl+D)", Variant: kit.Secondary})
	if err != nil {
		slog.Warn("mdpreview: toggle", "err", err)
	}
	if clicked {
		p.toggleTheme()
	}
	return layout.Dimensions{Size: gtx.Constraints.Max}
}

// document lays the reading column out inside the scroll area: capped at the
// prose measure with a gutter inside whatever frame remains.
func (p *preview) document(gtx *layout.Context) layout.Dimensions {
	gtx.Constraints.Max.X = min(gtx.Constraints.Max.X-2*gtx.Dp(theme.SpaceXXL), gtx.Dp(theme.ProseMeasure))
	return layout.UniformInset(theme.SpaceXXL).Layout(*gtx, func(gtx layout.Context) layout.Dimensions {
		l := gtx
		return mdview.Layout(&l, p.env.Th, p.doc, &p.state)
	})
}

// openLinks hands queued open requests to the opener.
func (p *preview) openLinks() {
	for {
		req, ok := p.state.PollOpen()
		if !ok {
			return
		}
		if err := openURL(req.URL); err != nil {
			slog.Warn("mdpreview: open link", "err", err)
		}
	}
}

// toggleTheme swaps light for dark over the same shaper.
func (p *preview) toggleTheme() {
	if p.env.Th == &p.light {
		p.env.Th = &p.dark
	} else {
		p.env.Th = &p.light
	}
}
