//go:build darwin

package hostapi

import "testing"

// TestVendorDescendantCannotActAsLocalClient is the macOS half of roadmap
// Q-41 A, end to end: the peer PID comes from LOCAL_PEERPID and membership
// from the session's process tree. The Linux half, cgroup membership, is in
// internal/process.
func TestVendorDescendantCannotActAsLocalClient(t *testing.T) {
	t.Parallel()
	t.Run("a process of the vendor tree is denied the local hello", func(t *testing.T) {
		t.Parallel()
		assertVendorChildHelloDenied(t)
	})
	t.Run("unknowns fail closed", func(t *testing.T) {
		t.Parallel()
		assertLocalHelloFailsClosed(t)
	})
}
