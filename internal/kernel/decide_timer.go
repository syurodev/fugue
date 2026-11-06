package kernel

// timerFired decides whether a TimerFiredCommand is valid to elapse its timer.
// Matching generation and observed time at or after deadline emits TimerElapsed.
// Stale or cancelled generation returns a nil batch (no-op).
// An early observation is rejected with early_timer.
func (d *decision) timerFired(c *TimerFiredCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeCommandRejected, "nil command")
	}
	e := d.next
	if c.CommandID != e.CommandID || c.RunID != e.RunID || c.Origin != e.Origin {
		return nil, Reject(RejectionCodeCommandRejected, "command metadata mismatch")
	}
	if c.Origin != "timer" {
		return nil, Reject(RejectionCodeCommandRejected, "timer origin required")
	}
	if c.ObservedTime.IsZero() {
		return nil, Reject(RejectionCodeCommandRejected, "zero observed time")
	}

	active, exists := d.state.ActiveTimers[c.TimerID]
	if !exists || active == nil || active.Generation != c.Generation {
		// A stale, missing, or already elapsed timer generation produces an empty batch (no-op receipt).
		return []Event{}, nil
	}

	if c.ObservedTime.Before(active.Deadline) {
		return nil, Reject(RejectionCodeEarlyTimer, "timer observed before deadline")
	}

	return []Event{
		&TimerElapsedEvent{
			Envelope:     d.envelope(),
			TimerID:      c.TimerID,
			Generation:   c.Generation,
			ObservedTime: c.ObservedTime,
		},
	}, nil
}
