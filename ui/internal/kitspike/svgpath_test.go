package kitspike

import (
	"image"
	"math"
	"testing"
	"time"

	"gioui.org/f32"
)

// The harness's fake clock: one frame per tick.
var clockNow = time.Unix(1_700_000_000, 0)

func frameClock() time.Time { return clockNow }

func advanceClock() { clockNow = clockNow.Add(time.Second / 60) }

func imagePt(x, y int) image.Point { return image.Pt(x, y) }

func imageF32Pt(x, y float32) f32.Point { return f32.Pt(x, y) }

// TestIconPathsParse checks that all ten packaged Lucide paths parse and
// produce sane geometry on the 24 grid.
func TestIconPathsParse(t *testing.T) {
	if err := loadIcons(); err != nil {
		t.Fatal(err)
	}
	if len(icons) != len(iconNames) {
		t.Fatalf("loaded %d icons, want %d", len(icons), len(iconNames))
	}
	for name, spec := range icons {
		if len(spec.subpaths) == 0 {
			t.Fatalf("%s: no subpaths", name)
		}
		for _, sp := range spec.subpaths {
			for _, seg := range sp.Segments {
				checkCoord := func(v float32) {
					if v < -1 || v > iconGrid+1 {
						t.Fatalf("%s: segment coordinate %v outside the 24 grid", name, v)
					}
				}
				checkCoord(sp.Move.X)
				checkCoord(sp.Move.Y)
				if seg.IsCubic {
					checkCoord(seg.C0.X)
					checkCoord(seg.C1.Y)
					checkCoord(seg.End.X)
				} else {
					checkCoord(seg.To.X)
					checkCoord(seg.To.Y)
				}
			}
		}
	}
}

// TestParseXIconHasTwoCrossingStrokes checks the x icon: two separate SVG
// path elements, so the relative m of the second must not continue the
// first's endpoint.
func TestParseXIconHasTwoCrossingStrokes(t *testing.T) {
	if err := loadIcons(); err != nil {
		t.Fatal(err)
	}
	spec := icons["x"]
	if len(spec.subpaths) != 2 {
		t.Fatalf("x: %d subpaths, want 2 separate strokes", len(spec.subpaths))
	}
	// Stroke 1: M18 6 -> 6 18. Stroke 2: m6 6 -> 18 18.
	if got := spec.subpaths[1].Move; got != (f32Point{6, 6}) {
		t.Fatalf("x: second stroke starts at %v, want (6,6)", got)
	}
	if got := spec.subpaths[1].Segments[0].To; got != (f32Point{18, 18}) {
		t.Fatalf("x: second stroke ends at %v, want (18,18)", got)
	}
}

// TestArcToCubicsStaysOnRadius feeds a half circle and checks the
// conversion: endpoints and curve midpoints sit on the radius, control
// points ride the tangents outside it by design.
func TestArcToCubicsStaysOnRadius(t *testing.T) {
	// Half circle from (2,12) over the top to (22,12), r=10 (two quarter
	// arcs), the shape of the search pupil built from two arcs.
	segs, err := arcToCubics(f32Point{2, 12}, 10, 10, 0, false, true, f32Point{22, 12})
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("half circle: %d segments, want 2", len(segs))
	}
	center := f32Point{12, 12}
	prev := f32Point{2, 12}
	for i, s := range segs {
		if !s.IsCubic {
			t.Fatal("arc converted to a line")
		}
		// Cubic at t=0.5 is (P0 + 3·C0 + 3·C1 + P1)/8.
		mid := f32Point{
			(prev.X + 3*s.C0.X + 3*s.C1.X + s.End.X) / 8,
			(prev.Y + 3*s.C0.Y + 3*s.C1.Y + s.End.Y) / 8,
		}
		prev = s.End
		for _, p := range []f32Point{s.End, mid} {
			d := p.minus(center).len()
			if d < 9.9 || d > 10.1 {
				t.Fatalf("segment %d: point %v at distance %v from the center, want ~10", i, p, d)
			}
		}
	}
}

// minus and len keep the test arithmetic readable.
func (p f32Point) minus(q f32Point) f32Point { return f32Point{p.X - q.X, p.Y - q.Y} }

func (p f32Point) len() float32 {
	return float32(math.Sqrt(float64(p.X*p.X + p.Y*p.Y)))
}
