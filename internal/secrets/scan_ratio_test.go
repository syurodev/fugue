//go:build !race

package secrets

import "testing"

// CI runs every test with -race on shared runners, where the detector's
// overhead and scheduler noise make timing ratios swing between 4x and over
// 20x. The linear-time ratio is therefore enforced by running this test
// without -race; the allocation and masking checks run in both modes.
func enforceScanRatio(t *testing.T, shortMedianNs, longMedianNs int64) {
	t.Helper()
	if longMedianNs > shortMedianNs*6 {
		t.Fatalf("1 MiB median scan took %d ns, more than 6x 256 KiB median %d ns", longMedianNs, shortMedianNs)
	}
}
