//go:build race

package mdview

import "testing"

// TestLayoutP95StaysWithinTheFrameBudget under the race detector checks only
// that the 20 KB document lays out within its width and logs the timings: the
// race detector slows layout by an order of magnitude on shared runners, so
// the 8 ms budget is asserted by the build without -race (budget_norace_test.go).
func TestLayoutP95StaysWithinTheFrameBudget(t *testing.T) {
	m := measureFrames(t)
	t.Parallel()
	m.logTimings(t)
	if m.size.Size.X != 800 || m.size.Size.Y <= 0 {
		t.Errorf("document size = %v, want the 800 px width and a positive height", m.size.Size)
	}
}
