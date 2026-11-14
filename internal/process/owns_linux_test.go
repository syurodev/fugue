//go:build linux

package process

import "testing"

// TestVendorDescendantCannotActAsLocalClient is the Linux half of roadmap
// Q-41 A: the Host API asks OwnsPID whether a connecting peer belongs to a
// vendor session. With a systemd scope the answer is the scope's cgroup
// membership; without one it is the tracked process tree. Both fail closed.
// The macOS half, end to end over the socket, is in internal/hostapi.
func TestVendorDescendantCannotActAsLocalClient(t *testing.T) {
	t.Parallel()
	t.Run("cgroup scope membership", func(t *testing.T) {
		t.Parallel()
		testScopeOwnership(t)
	})
	t.Run("process tree without a scope", func(t *testing.T) {
		t.Parallel()
		testTrackerOwnership(t)
	})
}
