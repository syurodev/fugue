package sealing

import "testing"

func TestExceedsLimitHandlesOverflow(t *testing.T) {
	t.Parallel()
	if ExceedsLimit(5, 5, 10) {
		t.Fatal("equal limit rejected")
	}
	if !ExceedsLimit(5, 6, 10) {
		t.Fatal("excess accepted")
	}
	if !ExceedsLimit(1<<63-1, 1, 1<<63-1) {
		t.Fatal("overflow accepted")
	}
}
