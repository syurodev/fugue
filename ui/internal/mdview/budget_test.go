package mdview

import (
	"slices"
	"testing"
	"time"

	"gioui.org/layout"

	"github.com/syurodev/fugue/ui/internal/markdown"
)

// frameTimings are the measurements of the 20 KB document.
type frameTimings struct {
	bytes      int
	size       layout.Dimensions
	warmP95    time.Duration
	warmFrames int
	coldMedian time.Duration
	coldP95    time.Duration
	coldFrames int
}

// measureFrames lays out a 20 KB document with highlighted code and times
// steady-state frames and cold first frames. It reads the benchmark timer,
// not a clock, and must run before the caller's t.Parallel so no other test
// competes for the machine (as in the redaction cost test of PR 93).
func measureFrames(t *testing.T) frameTimings {
	t.Helper()
	src := sampleDoc(20 * 1024)
	h := newHarness(t, src, 800)
	if !slices.ContainsFunc(h.doc.Blocks, func(b markdown.Block) bool { return b.Highlighted }) {
		t.Fatal("the sample has no highlighted code")
	}
	out := frameTimings{bytes: len(src), size: h.frame()}
	// The best of several rounds: contention from other packages' tests only
	// ever adds time, so the lowest p95 is the closest to the true cost.
	out.warmP95 = time.Hour
	for range 4 {
		var samples []time.Duration
		testing.Benchmark(func(b *testing.B) {
			b.Helper()
			for b.Loop() {
				h.ops.Reset()
				start := b.Elapsed()
				Layout(h.context(), &h.th, h.doc, &h.st)
				samples = append(samples, b.Elapsed()-start)
			}
		})
		slices.Sort(samples)
		out.warmP95 = min(out.warmP95, samples[len(samples)*95/100])
		out.warmFrames += len(samples)
	}
	// A streamed item meets a cold frame: no laid-out text yet. The shaper's
	// own cache is warm here; the bundled fonts are parsed once per process.
	var cold []time.Duration
	testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			h.st = State{}
			h.ops.Reset()
			start := b.Elapsed()
			Layout(h.context(), &h.th, h.doc, &h.st)
			cold = append(cold, b.Elapsed()-start)
		}
	})
	slices.Sort(cold)
	out.coldMedian, out.coldP95, out.coldFrames = cold[len(cold)/2], cold[len(cold)*95/100], len(cold)
	return out
}

// logTimings prints the measurements.
func (m *frameTimings) logTimings(t *testing.T) {
	t.Helper()
	t.Logf("cold first frame (empty State, warm shaper): median %v, p95 %v over %d frames", m.coldMedian, m.coldP95, m.coldFrames)
	t.Logf("20 KB document (%d bytes, %d px tall): warm p95 %v over %d frames in 4 rounds", m.bytes, m.size.Size.Y, m.warmP95, m.warmFrames)
}
