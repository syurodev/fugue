package mdview

import "testing"

// TestLayoutFitsTheBudgetDocument checks that the 20 KB document lays out
// within its width and logs the timings. The 8 ms p95 budget of DA-03 is a
// speed assertion: it runs only in the nightly perf job
// (budget_perf_test.go, `task perf`), never in a PR run.
func TestLayoutFitsTheBudgetDocument(t *testing.T) {
	m := measureFrames(t)
	t.Parallel()
	m.logTimings(t)
	if m.size.Size.X != 800 || m.size.Size.Y <= 0 {
		t.Errorf("document size = %v, want the 800 px width and a positive height", m.size.Size)
	}
}
