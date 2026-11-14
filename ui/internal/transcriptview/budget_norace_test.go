//go:build !race

package transcriptview

import (
	"testing"
	"time"
)

// TestTranscriptLayoutBudget holds a scripted scroll and stream over 2,000
// mixed items to the layout p95 of desktop-app §9. The absolute budget is a
// release number measured without instrumentation, so it is asserted only in
// builds without the race detector; CI runs the ui tests with -race, where
// the same measurement is only logged (the race build of this test). DA-E01
// stays the owner-machine measurement (F10.4a).
func TestTranscriptLayoutBudget(t *testing.T) {
	warmP95, frames := measureBudget(t)
	t.Parallel()
	logBudget(t, warmP95, frames)
	if warmP95 > 8*time.Millisecond {
		t.Errorf("layout p95 = %v, want at most 8 ms", warmP95)
	}
}
