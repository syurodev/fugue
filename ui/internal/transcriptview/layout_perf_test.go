//go:build perf

package transcriptview

import (
	"slices"
	"testing"
	"time"
)

// perfRuns is how many times the budget is measured; the median is compared.
const perfRuns = 5

// TestTranscriptLayoutBudget holds a scripted scroll and stream over 2,000
// mixed items to the layout p95 of desktop-app section 9, as the median of
// several measurements. It runs only in the nightly perf job, without the race
// detector; DA-E01 stays the owner-machine measurement (F10.4a).
func TestTranscriptLayoutBudget(t *testing.T) {
	p95s := make([]time.Duration, 0, perfRuns)
	for range perfRuns {
		warmP95, frames := measureBudget(t)
		logBudget(t, warmP95, frames)
		p95s = append(p95s, warmP95)
	}
	slices.Sort(p95s)
	if got := p95s[len(p95s)/2]; got > 8*time.Millisecond {
		t.Errorf("median layout p95 = %v over %v, want at most 8 ms", got, p95s)
	}
}
