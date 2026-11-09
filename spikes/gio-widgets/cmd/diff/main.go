// diff: spike 3. A 5,000-line unified diff with Go syntax colouring from
// chroma, line-number gutter, add/delete backgrounds, scrolling, selection
// and copy, on the same list and selection engine as the transcript.
//
//	go run ./cmd/diff [-bench|-auto|-shot a.png]
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/diff"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/perf"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/shot"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/view"
)

func main() {
	n := flag.Int("n", 5000, "diff lines")
	png := flag.String("shot", "", "render one frame to this PNG and exit")
	flag.Parse()
	sh, err := fonts.Shaper()
	if err != nil {
		log.Fatal(err)
	}
	a := diff.Source()
	lines, b := diff.Make(a, *n, 1)
	es, st, err := diff.Entries(lines, a, b)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("%d diff lines from %d source lines; chroma tokenised both sides in %v, built items in %v\n", len(es), len(a), st.Lex, st.Build)

	pal := view.Dark()
	env := flow.NewEnv(sh, pal, 13)
	env.GutW = 90
	v := view.New(view.NewTheme(sh, pal), env, pal, es)
	v.Sel.PadY, v.Sel.Sep = 0, "\n"
	draw := func(gtx layout.Context) { view.Background(gtx, pal.Bg); v.Layout(gtx) }
	if *png != "" {
		v.Sel.Anchor, v.Sel.Focus, v.Sel.Has = flow.Pos{Item: 12, Off: 6}, flow.Pos{Item: 16, Off: 20}, true
		if err := shot.Render(*png, image.Pt(1000, 520), draw); err != nil {
			log.Fatal(err)
		}
		return
	}
	perf.Run(perf.Spec{Title: "spike: diff", Size: image.Pt(1000, 800), Draw: draw,
		Phases: func() []perf.Phase {
			return []perf.Phase{
				{Name: "scroll 5,000 lines", Dur: 8 * time.Second, Tick: func(n int) {
					v.List.List.Position.Offset += 30
					if v.List.List.Position.First > len(es)-60 {
						v.List.List.Position = layout.Position{}
					}
				}},
				{Name: "idle", Dur: 5 * time.Second, Idle: true},
			}
		}})
}
