//go:build perf

package vendorstore

import "testing"

// The nightly perf job runs the 10,000-append variant without the race
// detector; the PR run covers the same property with manyPayloads.
func TestAppend10000PayloadsKeepsTotalConsistent(t *testing.T) {
	t.Parallel()
	appendPayloadsAndCheckTotal(t, 10000)
}
