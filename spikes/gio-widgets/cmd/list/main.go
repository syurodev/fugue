// list: spike 4. 10,000 items of different heights; 30 stream ticks per
// second into the last item; follow the bottom while at the bottom, hold the
// position once the user scrolled up; tool_call/reasoning fold without the
// scroll position jumping.
//
//	go run ./cmd/list [-bench|-auto]
package main

import (
	"flag"
	"fmt"
	"image"
	"log"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/perf"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/view"
)

func main() {
	n := flag.Int("n", 10000, "items")
	flag.Parse()
	sh, err := fonts.Shaper()
	if err != nil {
		log.Fatal(err)
	}
	pal := view.Dark()
	v := view.New(view.NewTheme(sh, pal), flow.NewEnv(sh, pal, 15), pal, gen.Entries(*n, 2))
	v.List.List.ScrollToEnd = true // follow the bottom until the user scrolls up
	draw := func(gtx layout.Context) { view.Background(gtx, pal.Bg); v.Layout(gtx) }
	var maxJump int
	st := &view.Streamer{V: v}
	perf.Run(perf.Spec{
		Title: "spike: list", Size: image.Pt(900, 800), Draw: draw,
		Phases:      func() []perf.Phase { return phases(v, &maxJump) },
		Interactive: st.Interactive,
		Done:        func() { fmt.Printf("\nlargest jump of a held item: %d px\n", maxJump) },
	})
}

func phases(v *view.View, maxJump *int) []perf.Phase {
	var last time.Time
	stream := func() {
		if now := time.Now(); now.Sub(last) > 33*time.Millisecond {
			last = now
			e := v.Entries[len(v.Entries)-1]
			b := e.Body
			if len(b) == 0 {
				b = []flow.Block{{}}
			}
			b[0].Runs = append(b[0].Runs, gen.Inline("delta **ghép** `chữ` ")...)
			e.SetBody(b)
		}
	}
	// held reports how far an item that should stand still moved.
	var ref = map[int]int{}
	check := func() {
		for _, s := range v.Sel.Visible() {
			if t, ok := ref[s.Idx]; ok {
				*maxJump = max(*maxJump, abs(t-s.Top))
			}
			ref[s.Idx] = s.Top
		}
	}
	return []perf.Phase{
		{Name: "follow bottom + stream", Dur: 5 * time.Second, Tick: func(int) { v.Entries[len(v.Entries)-1].Kind = "assistant"; stream() }},
		{Name: "scrolled up + stream", Dur: 5 * time.Second, Tick: func(n int) {
			if n == 1 {
				v.List.List.ScrollBy(-30) // user leaves the bottom
				ref = map[int]int{}
			}
			stream()
			check()
		}},
		{Name: "fold toggles + stream", Dur: 5 * time.Second, Tick: func(n int) {
			stream()
			if n%20 == 0 { // toggle a fold that is on screen, not the first one
				for _, s := range v.Sel.Visible()[1:] {
					if v.Entries[s.Idx].Collapsible() {
						v.ToggleAt(s.Idx)
						break
					}
				}
			}
		}},
		{Name: "idle", Dur: 9 * time.Second, Idle: true},
	}
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}
