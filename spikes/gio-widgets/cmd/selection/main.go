// selection: spike 1. A 2,000-item virtualised markdown transcript with a
// mouse selection that spans items; Ctrl+C copies plain text.
//
//	go run ./cmd/selection              interactive (Ctrl+S starts/stops streaming)
//	go run ./cmd/selection -auto        scripted measurement in a real window
//	go run ./cmd/selection -bench       same, headless (no display needed)
//	go run ./cmd/selection -shot a.png  one frame to a PNG
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"math/rand"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/perf"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/shot"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/view"
)

func main() {
	n := flag.Int("n", 2000, "items")
	png := flag.String("shot", "", "render one frame to this PNG and exit")
	flag.Parse()

	sh, err := fonts.Shaper()
	if err != nil {
		log.Fatal(err)
	}
	pal := view.Dark()
	env := flow.NewEnv(sh, pal, 15)
	v := view.New(view.NewTheme(sh, pal), env, pal, gen.Entries(*n, 1))
	// A selection from the middle of item A into item C, across a code block.
	a, f := flow.Pos{Item: 1000, Off: 30}, flow.Pos{Item: 1004, Off: 40}
	v.Sel.Anchor, v.Sel.Focus, v.Sel.Has = a, f, true
	v.List.List.Position.First = 1000 // start where the selection is
	draw := func(gtx layout.Context) { view.Background(gtx, pal.Bg); v.Layout(gtx) }

	if *png != "" {
		if err := shot.Render(*png, image.Pt(900, 700), draw); err != nil {
			log.Fatal(err)
		}
		return
	}
	perf.Run(perf.Spec{
		Title: "spike: selection", Size: image.Pt(900, 800), Draw: draw,
		Phases:      func() []perf.Phase { return phases(v) },
		Interactive: (&view.Streamer{V: v, NewEvery: 2 * time.Second}).Interactive,
		Done: func() {
			fmt.Printf("\nselection kept: %v (%v..%v), items now %d\n", v.Sel.Anchor == a && v.Sel.Focus == f, v.Sel.Anchor, v.Sel.Focus, len(v.Entries))
		},
	})
}

// phases scrolls through the transcript, streams into the last item at 30 Hz
// (adding an item every 2 s), then idles. The selection must survive.
func phases(v *view.View) []perf.Phase {
	rng := rand.New(rand.NewSource(7))
	var lastTick, lastItem time.Time
	return []perf.Phase{
		{Name: "scroll", Dur: 6 * time.Second, Tick: func(n int) {
			if n == 1 {
				v.List.List.ScrollTo(900)
			}
			v.List.List.Position.Offset += 24
		}},
		{Name: "stream 600 ch/s", Dur: 6 * time.Second, Tick: func(n int) {
			if n == 1 {
				v.List.List.ScrollToEnd = true
				v.List.List.Position.BeforeEnd = false
			}
			now := time.Now()
			if now.Sub(lastItem) > 2*time.Second {
				lastItem = now
				v.Add(gen.Entry{Kind: "assistant", Head: "**Fugue** streaming", Body: []flow.Block{{}}})
			}
			if now.Sub(lastTick) > 33*time.Millisecond {
				lastTick = now
				e := v.Entries[len(v.Entries)-1]
				b := e.Body[0]
				b.Runs = append(b.Runs, gen.Inline(fmt.Sprintf("streamed văn bản %c ", 'a'+rng.Intn(26)))...)
				e.SetBody([]flow.Block{b})
			}
		}},
		{Name: "idle", Dur: 9 * time.Second, Idle: true},
	}
}
