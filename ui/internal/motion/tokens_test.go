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
		{"Snappy", Snappy(), Spec{Response: 220 * time.Millisecond, DampingFraction: 0.9}},
		{"Smooth", Smooth(), Spec{Response: 300 * time.Millisecond, DampingFraction: 1}},
		{"Bouncy", Bouncy(), Spec{Response: 320 * time.Millisecond, DampingFraction: 0.72}},
		{"Exit", Exit(), Spec{Response: 150 * time.Millisecond, DampingFraction: 1}},
	} {
		if tc.spec.Response != tc.want.Response {
			t.Errorf("%s response is %v, want %v", tc.name, tc.spec.Response, tc.want.Response)
		}
		if tc.spec.DampingFraction != tc.want.DampingFraction {
			t.Errorf("%s damping fraction is %v, want %v", tc.name, tc.spec.DampingFraction, tc.want.DampingFraction)
		}
	}

	// The roles the kit assigns assume this ordering: leaving is the
	// quickest, Snappy fades/toggles faster than Smooth moves, Bouncy has the
	// longest response so its bounce stays visible without slowing down
	// Smooth, and only Snappy and Bouncy bounce — Smooth and Exit settle
	// without overshoot.
	if Exit().Response >= Snappy().Response || Snappy().Response >= Smooth().Response || Smooth().Response >= Bouncy().Response {
		t.Errorf("token responses are out of order: Exit %v, Snappy %v, Smooth %v, Bouncy %v",
			Exit().Response, Snappy().Response, Smooth().Response, Bouncy().Response)
	}
	if min(Snappy().DampingFraction, Bouncy().DampingFraction) >= 1 {
		t.Error("Snappy and Bouncy are the bouncing tokens; both sit below 1")
	}
	if Smooth().DampingFraction != 1 || Exit().DampingFraction != 1 {
		t.Error("Smooth and Exit must settle without overshoot: their damping fraction is 1")
	}
}

type tokenSettlementCase struct {
	name           string
	spec           Spec
	want90         time.Duration
	want99         time.Duration
	allowOvershoot bool
}

// TestMotionTokenSettlementTimes pins the 90%-reached and 99%-settled timings
// for each macOS-tuned token within ±10 ms using a fake 1 ms clock step.
func TestMotionTokenSettlementTimes(t *testing.T) {
	t.Parallel()
	for _, tc := range []tokenSettlementCase{
		{"Smooth", Smooth(), 186 * time.Millisecond, 319 * time.Millisecond, false},
		{"Snappy", Snappy(), 120 * time.Millisecond, 182 * time.Millisecond, true},
		{"Exit", Exit(), 93 * time.Millisecond, 161 * time.Millisecond, false},
		{"Bouncy", Bouncy(), 137 * time.Millisecond, 169 * time.Millisecond, true},
	} {
		verifyTokenSettlement(t, tc)
	}
}

func measureSettlement(spec Spec) (got90, got99 time.Duration, peak float32, spring Spring) {
	spring = NewSpringSpec(0, spec)
	spring.Target = 1
	for ms := 1; ms <= 2000; ms++ {
		spring.Advance(time.Millisecond)
		peak = max(peak, spring.Value)
		elapsed := time.Duration(ms) * time.Millisecond
		if got90 == 0 && spring.Value >= 0.90 {
			got90 = elapsed
		}
		if got99 == 0 && spring.Value >= 0.99 {
			got99 = elapsed
		}
	}
	return got90, got99, peak, spring
}

func verifyTokenSettlement(t *testing.T, tc tokenSettlementCase) {
	t.Helper()
	const tolerance = 10 * time.Millisecond
	got90, got99, peak, s := measureSettlement(tc.spec)
	if got90 == 0 || absDur(got90-tc.want90) > tolerance {
		t.Errorf("%s 90%% time = %v, want %v (±%v)", tc.name, got90, tc.want90, tolerance)
	}
	if got99 == 0 || absDur(got99-tc.want99) > tolerance {
		t.Errorf("%s 99%% time = %v, want %v (±%v)", tc.name, got99, tc.want99, tolerance)
	}
	if !tc.allowOvershoot && peak > 1+restDistance {
		t.Errorf("%s overshot to %v", tc.name, peak)
	}
	if !s.AtRest() || s.Value != 1 {
		t.Errorf("%s did not settle at rest: %+v", tc.name, s)
	}
}

func absDur(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
