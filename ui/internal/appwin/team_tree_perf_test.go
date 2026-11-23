//go:build perf

package appwin

import (
	"slices"
	"testing"
	"time"
)

// TestTeamTreeLayoutBudget measures the desktop-app §9 p95 release budget.
func TestTeamTreeLayoutBudget(t *testing.T) {
	const runs = 5
	durations := make([]time.Duration, 0, runs)
	for range runs {
		h := newTeamTreeHarness(t)
		durations = append(durations, measureTeamTreeFrames(t, h))
	}
	t.Logf("12 agents receiving continuous state changes, frame layout p95: %v", durations)
	slices.Sort(durations)
	if p95 := durations[len(durations)/2]; p95 > 8*time.Millisecond {
		t.Errorf("median frame layout p95 = %v, want at most 8 ms", p95)
	}
}

func measureTeamTreeFrames(t *testing.T, h *teamTreeHarness) time.Duration {
	t.Helper()
	samples := make([]time.Duration, 0, 100)
	testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for b.Loop() {
			start := b.Elapsed()
			h.update()
			h.render()
			samples = append(samples, b.Elapsed()-start)
		}
	})
	slices.Sort(samples)
	return samples[len(samples)*95/100]
}
