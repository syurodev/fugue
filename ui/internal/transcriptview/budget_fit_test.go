package transcriptview

import "testing"

// TestTranscriptLayoutFitsTheViewport checks that the 2,000-item pane lays out
// and logs the timings. The 8 ms p95 budget is a speed assertion: it runs only
// in the nightly perf job (budget_perf_test.go, `task perf`), never in a PR run.
func TestTranscriptLayoutFitsTheViewport(t *testing.T) {
	warmP95, frames := measureBudget(t)
	t.Parallel()
	logBudget(t, warmP95, frames)
	if frames == 0 {
		t.Error("the scripted scroll laid out no frames")
	}
}
