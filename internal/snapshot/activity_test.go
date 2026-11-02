package snapshot

import (
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
)

func TestOverlapFindsOnlyConcurrentSessions(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(base)
	activity := NewActivity(fakeClock)

	earlier := activity.Begin("/proj", "s-earlier", ActivityTurn)
	fakeClock.Advance(time.Minute)
	earlier.End()

	fakeClock.Advance(time.Minute)
	during := activity.Begin("/proj", "s-during", ActivityTurn)

	other := activity.Begin("/other", "s-other", ActivityTurn)
	defer other.End()

	windowStart := base.Add(3 * time.Minute)
	fakeClock.Advance(2 * time.Minute)
	windowEnd := fakeClock.Now()

	got := activity.Overlap("/proj", windowStart, windowEnd, "s-self")
	if len(got) != 1 || got[0].Session != "s-during" {
		t.Fatalf("overlap = %+v, want only s-during", got)
	}

	during.End()
	if got := activity.Overlap("/proj", windowStart, windowEnd); len(got) != 1 {
		t.Fatalf("overlap after End = %+v, want the span still remembered", got)
	}
	if got := activity.Overlap("/proj", windowStart, windowEnd, "s-during"); len(got) != 0 {
		t.Fatalf("overlap = %+v, want the excluded session left out", got)
	}
}

func TestRunningInIgnoresFinishedSpans(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(base)
	activity := NewActivity(fakeClock)

	done := activity.Begin("/proj", "s-done", ActivityTurn)
	live := activity.Begin("/proj", "s-live", ActivityTurn)
	fakeClock.Advance(time.Minute)
	done.End()

	got := activity.RunningIn("/proj")
	if len(got) != 1 || got[0].Session != "s-live" || got[0].Kind != ActivityTurn {
		t.Fatalf("running = %+v, want only s-live", got)
	}
	if got := activity.RunningIn("/proj", "s-live"); len(got) != 0 {
		t.Fatalf("running = %+v, want the caller's own span excluded", got)
	}
	live.End()
	if got := activity.RunningIn("/proj"); len(got) != 0 {
		t.Fatalf("running = %+v, want none", got)
	}
}

func TestTrimKeepsRunningSpansAndDropsOldOnes(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(base)
	activity := NewActivity(fakeClock)

	stale := activity.Begin("/proj", "s-stale", ActivityTurn)
	longRunning := activity.Begin("/proj", "s-long", ActivityTurn)
	fakeClock.Advance(time.Minute)
	stale.End()

	fakeClock.Advance(spanHistory + time.Hour)
	activity.Begin("/proj", "s-new", ActivityTurn).End()

	sessions := make(map[string]bool)
	spans := activity.Overlap("/proj", base, fakeClock.Now())
	for i := range spans {
		sessions[spans[i].Session] = true
	}
	if sessions["s-stale"] {
		t.Error("a span that ended beyond the history window was kept")
	}
	if !sessions["s-long"] {
		t.Error("a still-running span was trimmed")
	}
	longRunning.End()
}

func TestEndIsSafeAndIdempotent(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	fakeClock := clock.NewFake(base)
	activity := NewActivity(fakeClock)

	var absent *Handle
	absent.End()

	handle := activity.Begin("/proj", "s", ActivityRestore)
	fakeClock.Advance(time.Minute)
	handle.End()
	first := handle.Span().End
	fakeClock.Advance(time.Hour)
	handle.End()
	if handle.Span().End != first {
		t.Fatalf("End moved the timestamp: %v then %v", first, handle.Span().End)
	}
}
