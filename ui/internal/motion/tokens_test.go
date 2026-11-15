package motion

import (
	"testing"
	"time"
)

// TestMotionTokensMatchTheKitSpec pins the four §3.7 motion tokens against
// the tuning they were accepted with: Snappy for direct responses to
// input, Smooth for content moving or resizing, Bouncy for small playful
// elements, Exit for things leaving the screen. A retune changes this test
// on purpose, so the whole app's feel never drifts one widget at a time.
func TestMotionTokensMatchTheKitSpec(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		spec Spec
		want Spec
	}{
		{"Snappy", Snappy(), Spec{Response: 300 * time.Millisecond, DampingFraction: 0.86}},
		{"Smooth", Smooth(), Spec{Response: 450 * time.Millisecond, DampingFraction: 1}},
		{"Bouncy", Bouncy(), Spec{Response: 400 * time.Millisecond, DampingFraction: 0.68}},
		{"Exit", Exit(), Spec{Response: 200 * time.Millisecond, DampingFraction: 1}},
	} {
		if tc.spec.Response != tc.want.Response {
			t.Errorf("%s response is %v, want %v", tc.name, tc.spec.Response, tc.want.Response)
		}
		if tc.spec.DampingFraction != tc.want.DampingFraction {
			t.Errorf("%s damping fraction is %v, want %v", tc.name, tc.spec.DampingFraction, tc.want.DampingFraction)
		}
	}

	// The roles the kit assigns assume this ordering: leaving is the
	// quickest, content movement the slowest, and only Snappy and Bouncy
	// bounce — Smooth and Exit settle without overshoot.
	if Exit().Response >= Snappy().Response || Snappy().Response >= Bouncy().Response || Bouncy().Response >= Smooth().Response {
		t.Errorf("token responses are out of order: Exit %v, Snappy %v, Bouncy %v, Smooth %v",
			Exit().Response, Snappy().Response, Bouncy().Response, Smooth().Response)
	}
	if min(Snappy().DampingFraction, Bouncy().DampingFraction) >= 1 {
		t.Error("Snappy and Bouncy are the bouncing tokens; both sit below 1")
	}
	if Smooth().DampingFraction != 1 || Exit().DampingFraction != 1 {
		t.Error("Smooth and Exit must settle without overshoot: their damping fraction is 1")
	}
}
