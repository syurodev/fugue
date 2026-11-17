package testutil

import (
	"runtime"
	"testing"
)

// The helpers themselves spin and sleep under a hang deadline.
func TestHelperSpins(t *testing.T) {
	for {
		runtime.Gosched()
		return
	}
}
