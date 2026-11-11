//go:build race

package secrets

import "testing"

// Under -race the ratio is only logged: see scan_ratio_test.go for why the
// 6x bound is enforced by the non-race run.
func enforceScanRatio(t *testing.T, shortMedianNs, longMedianNs int64) {
	t.Helper()
	t.Logf("1 MiB median scan %d ns, 256 KiB median %d ns (ratio not asserted under -race)", longMedianNs, shortMedianNs)
}
