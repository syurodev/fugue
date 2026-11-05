package kernel

import "strconv"

// loopReminderLevel maps a consecutive-call count to a reminder level
// (continuation §4): the 3rd, 5th and 8th identical call, nothing in between.
func loopReminderLevel(repeat int) (int, bool) {
	switch repeat {
	case 3, 5, 8:
		return repeat, true
	}
	return 0, false
}

// LoopReminderDue reports whether the result of a tool call must be followed by
// a loop reminder, and at which level. The count is the one recorded when the
// call was planned, so every call of a multi-call response is judged on its own
// position in the run.
func LoopReminderDue(s *State, toolCallID string) (level int, due bool) {
	t := s.ToolInvocations[toolCallID]
	if t == nil {
		return 0, false
	}
	return loopReminderLevel(t.Repeat)
}

// loopReminder builds the injection that follows a repeated call's result. The
// reminder never blocks or delays the call; it is a note for the next step. A
// due reminder without the host's payload ref is an error rather than a silent
// drop, because the model would then loop without the warning being recorded.
func (d *decision) loopReminder(toolCallID string) ([]Event, error) {
	level, due := LoopReminderDue(d.state, toolCallID)
	if !due {
		return nil, nil // no reminder: a nil batch, not an error
	}
	if d.facts.LoopReminderRef == "" {
		return nil, Reject(RejectionCodeCommandRejected, "loop reminder due without payload ref")
	}
	return []Event{&ContextInjectedEvent{
		Envelope: d.envelope(), Tag: "level_" + strconv.Itoa(level), Source: "loop_reminder",
		PayloadRef: d.facts.LoopReminderRef,
	}}, nil
}
