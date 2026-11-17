//go:build perf

package mdview

import (
	"slices"
	"testing"
	"time"
)

// perfRuns is how many times the budget is measured; the median is compared.
const perfRuns = 5

// TestLayoutP95StaysWithinTheFrameBudget holds a steady-state frame of a 20 KB
// document with highlighted code to the 8 ms p95 of DA-03, as the median of
// several measurements. It runs only in the nightly perf job, without the race
// detector; DA-05 release evidence stays the owner-machine measurement (DA-E01).
func TestLayoutP95StaysWithinTheFrameBudget(t *testing.T) {
	p95s := make([]time.Duration, 0, perfRuns)
	for range perfRuns {
		m := measureFrames(t)
		m.logTimings(t)
		p95s = append(p95s, m.warmP95)
	}
	slices.Sort(p95s)
	if got := p95s[len(p95s)/2]; got > 8*time.Millisecond {
		t.Errorf("median layout p95 = %v over %v, want at most 8 ms", got, p95s)
	}
}
