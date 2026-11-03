package kernel

func checkRetryBatches(events []Event, bad map[InvariantID]bool) {
	for _, event := range events {
		retry, ok := event.(*ModelRetryScheduledEvent)
		if !ok || retry == nil {
			continue
		}
		planned, timer := retryPairPresent(retry, events)
		if !planned || !timer {
			bad[SM08] = true
		}
	}
}

func retryPlanMatches(r *ModelRetryScheduledEvent, p *ModelAttemptPlannedEvent) bool {
	return p != nil && p.InvocationID == r.InvocationID && p.AttemptID == r.NewAttemptID && p.PreviousAttemptID == r.PreviousAttemptID
}

func retryPairPresent(retry *ModelRetryScheduledEvent, events []Event) (bool, bool) {
	planned, timer := false, false
	for _, e := range events {
		switch ev := e.(type) {
		case *ModelAttemptPlannedEvent:
			if retryPlanMatches(retry, ev) {
				planned = true
			}
		case *TimerScheduledEvent:
			if ev != nil && ev.TimerID == retry.TimerID && ev.Purpose == TimerPurposeModelRetry {
				timer = true
			}
		}
	}
	return planned, timer
}
