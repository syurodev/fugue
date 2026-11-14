//go:build race

package transcriptview

import "testing"

// TestTranscriptLayoutBudget under the race detector checks only that the
// 2,000-item pane lays out within its viewport and logs the timings: the race
// detector slows layout by an order of magnitude on shared runners, so the 8
// ms budget is asserted by the build without -race (budget_norace_test.go).
func TestTranscriptLayoutBudget(t *testing.T) {
	warmP95, frames := measureBudget(t)
	t.Parallel()
	logBudget(t, warmP95, frames)
}
