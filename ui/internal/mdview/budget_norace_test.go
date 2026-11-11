//go:build !race

package mdview

import (
	"testing"
	"time"
)

// TestLayoutP95StaysWithinTheFrameBudget holds a steady-state frame of a
// 20 KB document with highlighted code to the 8 ms p95 of DA-03. The absolute
// budget is a release number measured without instrumentation, so it is
// asserted only in builds without the race detector. CI runs the ui tests with
// -race, where the same measurement is only logged (the race build of this
// test); enforce the budget with
// `go test ./internal/mdview -run TestLayoutP95StaysWithinTheFrameBudget`.
// DA-05 release evidence stays the owner-machine measurement (DA-E01, F10.4).
func TestLayoutP95StaysWithinTheFrameBudget(t *testing.T) {
	m := measureFrames(t)
	t.Parallel()
	m.logTimings(t)
	if m.warmP95 > 8*time.Millisecond {
		t.Errorf("layout p95 = %v, want at most 8 ms", m.warmP95)
	}
}
