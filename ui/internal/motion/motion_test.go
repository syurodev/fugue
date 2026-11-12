package motion

import (
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// epochTime is a fixed instant; the package never reads the wall clock.
func epochTime() time.Time { return time.Unix(1_700_000_000, 0) }

func TestCriticalSpringSettlesWithoutOvershoot(t *testing.T) {
	t.Parallel()
	s := NewSpringSpec(0, Smooth())
	s.Target = 1
	for range 240 {
		s.Advance(time.Second / 120)
		if s.Value > 1+restDistance {
			t.Fatalf("critically damped spring overshot to %v", s.Value)
		}
	}
	if !s.AtRest() || s.Value != 1 {
		t.Fatalf("spring not at rest on target: %+v", s)
	}
}

func TestUnderdampedSpringBouncesThenSettles(t *testing.T) {
	t.Parallel()
	s := NewSpringSpec(0, Spec{Response: 400 * time.Millisecond, DampingFraction: 0.4})
	s.Target = 1
	peak := float32(0)
	for range 600 {
		s.Advance(time.Second / 120)
		peak = max(peak, s.Value)
	}
	if peak <= 1 {
		t.Fatalf("underdamped spring never overshot, peak %v", peak)
	}
	if !s.AtRest() {
		t.Fatalf("underdamped spring still moving after 5 s: %+v", s)
	}
}

func TestSpringIsStableOverOneLongFrame(t *testing.T) {
	t.Parallel()
	s := NewSpringSpec(0, Spec{Response: 50 * time.Millisecond, DampingFraction: 1})
	s.Target = 1
	s.Advance(2 * time.Second)
	if s.Value != 1 {
		t.Fatalf("stiff spring after one long frame = %v, want settled on 1", s.Value)
	}
}

func TestDriverIsIdleWhenNothingMoves(t *testing.T) {
	t.Parallel()
	var d Driver
	if dt := d.Begin(epochTime()); dt != 0 {
		t.Fatalf("first frame dt = %v, want 0", dt)
	}
	if _, ok := d.Next(); ok {
		t.Fatal("idle driver asked for a frame")
	}
	d.Begin(epochTime().Add(time.Second))
	d.Keep()
	if at, ok := d.Next(); !ok || !at.IsZero() {
		t.Fatalf("moving driver Next = %v %v, want immediate", at, ok)
	}
}

func TestDriverCapsLongFramesAndKeepsEarliestWake(t *testing.T) {
	t.Parallel()
	var d Driver
	d.Begin(epochTime())
	if dt := d.Begin(epochTime().Add(10 * time.Second)); dt != maxFrameStep {
		t.Fatalf("dt after a stall = %v, want %v", dt, maxFrameStep)
	}
	if dt := d.Begin(epochTime()); dt != 0 {
		t.Fatalf("dt for a clock going backwards = %v, want 0", dt)
	}
	d.WakeAt(epochTime().Add(3 * time.Second))
	d.WakeAt(epochTime().Add(2 * time.Second))
	d.WakeAt(epochTime().Add(4 * time.Second))
	if at, ok := d.Next(); !ok || !at.Equal(epochTime().Add(2*time.Second)) {
		t.Fatalf("Next = %v %v, want the earliest wake", at, ok)
	}
}

func TestSpecMatchesItsResponse(t *testing.T) {
	t.Parallel()
	// A critically damped spring covers about 99% of the way within one
	// response, and never overshoots.
	s := NewSpringSpec(0, Smooth())
	s.Target = 1
	steps := int(Smooth().Response / (time.Second / 120))
	for range steps {
		s.Advance(time.Second / 120)
	}
	if s.Value < 0.97 || s.Value > 1 {
		t.Fatalf("after one response the spring is at %v, want about 1 without overshoot", s.Value)
	}
}

func TestPresenceReversesFromWhereItIs(t *testing.T) {
	t.Parallel()
	p := NewPresence(Snappy(), Exit())
	if p.Visible() {
		t.Fatal("new presence is visible")
	}
	p.Show(true)
	for range 6 {
		p.Advance(time.Second / 60)
	}
	mid := p.Progress()
	if mid <= 0 || mid >= 1 {
		t.Fatalf("progress half way = %v", mid)
	}
	p.Show(false)
	p.Advance(time.Second / 60)
	if got := p.Progress(); got < mid-0.2 || got > mid+0.2 {
		t.Fatalf("reversing jumped from %v to %v", mid, got)
	}
	for p.Advance(time.Second / 60) {
	}
	if p.Visible() || p.Progress() != 0 {
		t.Fatalf("left presence still visible at %v", p.Progress())
	}
}

func TestFollowerSnapsFirstThenSprings(t *testing.T) {
	t.Parallel()
	f := NewFollower(Smooth())
	if got := f.Follow(40, time.Second/60); got != 40 || f.Moving() {
		t.Fatalf("first Follow = %v moving=%v, want 40 at rest", got, f.Moving())
	}
	got := f.Follow(100, time.Second/60)
	if got <= 40 || got >= 100 || !f.Moving() {
		t.Fatalf("second Follow = %v moving=%v, want between 40 and 100", got, f.Moving())
	}
}

func TestSlotsSlideMovedItemsAndForgetRemovedOnes(t *testing.T) {
	t.Parallel()
	s := NewSlots[string](Smooth())
	s.Begin()
	s.Place("a", 0, 0)
	s.Place("b", 50, 0)
	if s.End() {
		t.Fatal("first frame reported motion")
	}
	// An item is inserted above: a and b are laid out 40 lower.
	s.Begin()
	newItem := s.Place("new", 0, time.Second/60)
	drawnA := s.Place("a", 40, time.Second/60)
	if newItem != 0 || drawnA > 10 {
		t.Fatalf("new item at %v, a drawn at %v; want the new item in place and a near its old place", newItem, drawnA)
	}
	s.Place("b", 90, time.Second/60)
	if !s.End() {
		t.Fatal("moved items did not report motion")
	}
	// b is removed: it must be forgotten.
	for range 120 {
		s.Begin()
		s.Place("new", 0, time.Second/60)
		drawnA = s.Place("a", 40, time.Second/60)
		s.End()
	}
	if drawnA != 40 || len(s.items) != 2 {
		t.Fatalf("a drawn at %v with %d slots, want settled at 40 with b forgotten", drawnA, len(s.items))
	}
}

func TestSpringFollowingALargeValueComesToRest(t *testing.T) {
	t.Parallel()
	f := NewFollower(Smooth())
	f.Follow(14_691, time.Second/60)
	for range 600 {
		f.Follow(16_002, time.Second/60)
	}
	if f.Moving() || f.Value() != 16_002 {
		t.Fatalf("follower of a large value = %v moving=%v, want at rest on 16002", f.Value(), f.Moving())
	}
}
