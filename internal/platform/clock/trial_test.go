package clock_test

import "testing"

func TestTrialFails(t *testing.T) {
	t.Parallel()
	t.Fatal("trial failure")
}
