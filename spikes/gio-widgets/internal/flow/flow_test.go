package flow_test

import (
	"math/rand"
	"testing"

	"github.com/syurodev/fugue/spikes/gio-widgets/internal/flow"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/fonts"
	"github.com/syurodev/fugue/spikes/gio-widgets/internal/gen"
)

func TestHitCaretRoundTrip(t *testing.T) {
	sh, err := fonts.Shaper()
	if err != nil {
		t.Fatal(err)
	}
	pal := &flow.Palette{}
	env := flow.NewEnv(sh, pal, 15)
	r := rand.New(rand.NewSource(3))
	for n := 0; n < 40; n++ {
		it := &flow.Item{Blocks: gen.Message(r, 1+r.Intn(4))}
		for _, w := range []int{240, 520, 900} {
			l := env.Layout(it, w)
			total := len([]rune(it.Text()))
			bad := 0
			for o := 0; o <= total; o++ {
				x, y := l.Caret(o)
				if got := l.Hit(x, y+l.LineH()/2); got != o {
					bad++
				}
			}
			// A caret right after a wrapped line's trailing space is the same
			// pixel as the next line start; only a handful of such offsets exist.
			if bad > total/20 {
				t.Fatalf("item %d width %d: %d of %d offsets do not round-trip", n, w, bad, total)
			}
		}
	}
}
