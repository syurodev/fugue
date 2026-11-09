// composer: spike 2. Multi-line prompt box on widget.Editor with a slash menu.
//
//	go run ./cmd/composer            interactive (type, Enter, Shift+Enter, "/")
//	go run ./cmd/composer -bench     typing cost at 1 KB, 20 KB and 100 KB of text
//
// IME (Telex through IBus) can only be checked by hand: see REPORT.md.
package main

import (
	"flag"
	"image"
	"log"
	"strings"
	"time"

	"gioui.org/layout"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/composer"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/perf"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/view"
)

func main() {
	flag.Parse()
	sh, err := fonts.Shaper()
	if err != nil {
		log.Fatal(err)
	}
	pal := view.Dark()
	th := view.NewTheme(sh, pal)
	c := composer.New(th)
	draw := func(gtx layout.Context) {
		view.Background(gtx, pal.Bg)
		layout.UniformInset(unit.Dp(16)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					var kids []layout.FlexChild
					for _, s := range c.Sent {
						kids = append(kids, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Inset{Bottom: 8}.Layout(gtx, material.Body1(th, s).Layout)
						}))
					}
					return layout.Flex{Axis: layout.Vertical, Spacing: layout.SpaceEnd}.Layout(gtx, kids...)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return c.Layout(gtx, 8) }),
			)
		})
	}
	perf.Run(perf.Spec{Title: "spike: composer", Size: image.Pt(800, 600), Draw: draw,
		Phases: func() []perf.Phase {
			typing := func(name, text string) perf.Phase {
				return perf.Phase{Name: name, Dur: 3 * time.Second, Tick: func(n int) {
					if n == 1 {
						c.Ed.SetText(text)
						c.Ed.SetCaret(len([]rune(text)), len([]rune(text)))
					}
					c.Ed.Insert("x") // one keystroke per frame
				}}
			}
			line := "func (m *Manager) Run(ctx context.Context) error { // log line\n"
			return []perf.Phase{
				typing("typing in 1 KB", strings.Repeat(line, 16)),
				typing("typing in 20 KB", strings.Repeat(line, 320)),
				typing("typing in 100 KB", strings.Repeat(line, 1600)),
				typing("typing in 100 KB, no newline", strings.Repeat("word ", 20000)),
			}
		}})
}
