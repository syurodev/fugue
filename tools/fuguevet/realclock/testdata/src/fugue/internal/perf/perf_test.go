package perf

import (
	"testing"
	"time"
)

// The perf package is the one place a test may measure.
func TestBudget(t *testing.T) {
	start := time.Now()
	if time.Since(start) > time.Hour {
		t.Fatal("over budget")
	}
}
