package theme_test

import (
	"testing"

	"github.com/syurodev/fugue/ui/internal/theme"
)

// TestElevationFloatIsTheMeasuredStack pins the one shadow token of §3.6:
// four layers, each 3 dp wider and 1 dp further down than the last,
// fading from 10% to 2%. The spike measured this stack at a p50 of
// 0.64 ms per frame; a change here re-costs every floating surface.
func TestElevationFloatIsTheMeasuredStack(t *testing.T) {
	t.Parallel()
	layers := theme.ElevationFloat()
	want := []theme.ElevationLayer{
		{Grow: 2, Sag: 1, Alpha: 0.10},
		{Grow: 5, Sag: 2, Alpha: 0.06},
		{Grow: 8, Sag: 3, Alpha: 0.035},
		{Grow: 11, Sag: 4, Alpha: 0.02},
	}
	if len(layers) != len(want) {
		t.Fatalf("elevation.float has %d layers, want %d", len(layers), len(want))
	}
	for i, l := range layers {
		if l != want[i] {
			t.Errorf("layer %d = %+v, want %+v", i, l, want[i])
		}
	}
}
