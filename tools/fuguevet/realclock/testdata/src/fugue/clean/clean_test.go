package clean

import (
	"testing"
	"time"
)

// Durations and the time types are fine; only reading or measuring is not.
func TestNoClockReads(t *testing.T) {
	const budget = 2 * time.Second
	at := time.Unix(100, 0).Add(budget)
	if at.Sub(time.Unix(100, 0)) != budget {
		t.Fatal("arithmetic on fixed instants is deterministic")
	}
}
