//go:build !unix

package hostapi

import "testing"

// assertDataDirLockReleased has no probe on platforms without flock; the CI
// matrix (linux and darwin) always runs the real probe.
func assertDataDirLockReleased(t *testing.T, _ string) {
	t.Helper()
}
