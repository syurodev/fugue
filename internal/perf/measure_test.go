//go:build perf

package perf

import (
	"slices"
	"testing"
	"time"
)

// measurements is how many times each budget is measured; the median of them
// is compared to the budget so one noisy run neither fails nor hides a
// regression.
const measurements = 7

// median returns the median of values.
func median[T int64 | time.Duration](values []T) T {
	sorted := slices.Sorted(slices.Values(values))
	return sorted[len(sorted)/2]
}

// nsPerOp measures fn with the testing package's own timer and returns the
// nanoseconds per call.
func nsPerOp(fn func()) int64 {
	return testing.Benchmark(func(b *testing.B) {
		for b.Loop() {
			fn()
		}
	}).NsPerOp()
}

// medianRatio measures how much slower large is than small, measurements
// times, logs every ratio and returns the median.
func medianRatio(t *testing.T, name string, small, large func()) float64 {
	t.Helper()
	ratios := make([]int64, 0, measurements)
	for range measurements {
		s, l := nsPerOp(small), nsPerOp(large)
		t.Logf("%s: small %d ns, large %d ns, ratio %.2fx", name, s, l, float64(l)/float64(max(s, 1)))
		ratios = append(ratios, l*1000/max(s, 1))
	}
	return float64(median(ratios)) / 1000
}
