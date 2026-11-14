//go:build linux

package hostapi

import "testing"

// TestVendorProcessIsDeniedTheLocalHello runs the Q-41 A end-to-end check on
// Linux, where the peer PID comes from SO_PEERCRED: with a systemd user scope
// the session answers from its cgroup, without one from its process tree.
// The evidence test of the cgroup rule itself is in internal/process.
func TestVendorProcessIsDeniedTheLocalHello(t *testing.T) {
	t.Parallel()
	assertVendorChildHelloDenied(t)
}
