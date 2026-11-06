package kernel

import (
	"errors"
	"testing"
	"time"
)

const (
	testTimerID = "timer-1"
)

func newTestTimerState(deadline time.Time) *State {
	s := NewState()
	s.RunID = "run-timer-1"
	s.LastSequence = 5
	s.Admitted = true
	s.AdmissionToken = 1
	s.OwnerIncarnation = 1
	s.ActiveTimers[testTimerID] = &ActiveTimer{
		TimerID:    testTimerID,
		Generation: 1,
		Purpose:    TimerPurposeModelRetry,
		Deadline:   deadline,
	}
	return s
}

func newTestTimerFacts(runID, cmdID string, seq uint64, ts time.Time) *DecisionFacts {
	return &DecisionFacts{
		Envelope: Envelope{
			RunID:       runID,
			CommandID:   cmdID,
			Origin:      drvOriginTimer,
			CausationID: cmdID,
			Sequence:    seq,
			Timestamp:   ts,
			Token:       1,
		},
		OwnerIncarnation: 1,
	}
}

func TestDecideTimerFiredElapsesMatchingGeneration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(10 * time.Second)
	s := newTestTimerState(deadline)

	cmd := &TimerFiredCommand{
		CommandID:    "cmd-timer-fire-1",
		RunID:        s.RunID,
		Origin:       drvOriginTimer,
		TimerID:      testTimerID,
		Generation:   1,
		ObservedTime: deadline,
	}
	facts := newTestTimerFacts(s.RunID, cmd.CommandID, s.LastSequence+1, now)

	events, err := Decide(s, cmd, facts)
	if err != nil {
		t.Fatalf("Decide TimerFired failed: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	elapsed, ok := events[0].(*TimerElapsedEvent)
	if !ok {
		t.Fatalf("expected *TimerElapsedEvent, got %T", events[0])
	}
	if elapsed.TimerID != testTimerID || elapsed.Generation != 1 {
		t.Fatalf("unexpected TimerElapsedEvent fields: %+v", elapsed)
	}
	if !elapsed.ObservedTime.Equal(deadline) {
		t.Fatalf("observed time mismatch: got %v, want %v", elapsed.ObservedTime, deadline)
	}

	evolved := Evolve(s, elapsed)
	if _, exists := evolved.ActiveTimers[testTimerID]; exists {
		t.Fatal("timer-1 must be removed from ActiveTimers after TimerElapsed")
	}
}

func TestDecideTimerFiredRejectsEarlyObservation(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(10 * time.Second)
	s := newTestTimerState(deadline)

	cmd := &TimerFiredCommand{
		CommandID:    "cmd-timer-early",
		RunID:        s.RunID,
		Origin:       drvOriginTimer,
		TimerID:      testTimerID,
		Generation:   1,
		ObservedTime: deadline.Add(-time.Millisecond),
	}
	facts := newTestTimerFacts(s.RunID, cmd.CommandID, s.LastSequence+1, now)

	_, err := Decide(s, cmd, facts)
	if err == nil {
		t.Fatal("expected rejection for early timer observation, got nil")
	}

	var rej Rejection
	if !errors.As(err, &rej) || rej.Code != RejectionCodeEarlyTimer {
		t.Fatalf("expected RejectionCodeEarlyTimer, got %v", err)
	}
}

func TestDecideTimerFiredIgnoresStaleGeneration(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(10 * time.Second)

	s := newTestTimerState(deadline)
	s.ActiveTimers[testTimerID].Generation = 2

	// Stale generation 1
	cmdStale := &TimerFiredCommand{
		CommandID:    "cmd-timer-stale",
		RunID:        s.RunID,
		Origin:       drvOriginTimer,
		TimerID:      testTimerID,
		Generation:   1,
		ObservedTime: deadline.Add(time.Second),
	}
	factsStale := newTestTimerFacts(s.RunID, cmdStale.CommandID, s.LastSequence+1, now)

	events, err := Decide(s, cmdStale, factsStale)
	if err != nil {
		t.Fatalf("stale generation must be a no-op, got error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected nil or empty batch for stale timer, got %d events", len(events))
	}

	// Missing timer ID
	cmdMissing := &TimerFiredCommand{
		CommandID:    "cmd-timer-missing",
		RunID:        s.RunID,
		Origin:       drvOriginTimer,
		TimerID:      "nonexistent-timer",
		Generation:   1,
		ObservedTime: deadline.Add(time.Second),
	}
	factsMissing := newTestTimerFacts(s.RunID, cmdMissing.CommandID, s.LastSequence+1, now)

	events, err = Decide(s, cmdMissing, factsMissing)
	if err != nil {
		t.Fatalf("missing timer must be a no-op, got error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("expected nil or empty batch for missing timer, got %d events", len(events))
	}
}
