package kernel

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestRetryPreservesAttemptEvidence(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 4)
	c := cases[4]
	bindDecision(&c, s, "failure")
	failure := new(ModelRequestFailedEvent)
	failure.Envelope = c.facts.Envelope
	failure.InvocationID = "m1"
	failure.AttemptID = "a1"
	failure.PartialRef = "partial-ref"
	failure.Stage = "dispatch"
	failure.FailureClass = "retryable"
	if ids := CheckBatch(s, []Event{failure}, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	s = Evolve(s, failure)
	old := *s.ModelInvocations["m1"].Attempts["a1"]
	events := retryFixture(s, &c)
	if ids := CheckBatch(s, events, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	if diff := cmp.Diff(old, *s.ModelInvocations["m1"].Attempts["a1"]); diff != "" {
		t.Fatal(diff)
	}
	if s.ModelInvocations["m1"].Attempts["retry-attempt"].Status != ModelAttemptStatusPlanned {
		t.Fatal("retry not planned")
	}
	if ids := CheckBatch(replayBeforeRetry(t), events[:1], &c.facts); !slices.Contains(ids, SM08) {
		t.Fatal("non-atomic retry accepted")
	}
}

func replayBeforeRetry(t *testing.T) *State {
	t.Helper()
	s, _ := traceState(t, 4)
	e := new(ModelRequestFailedEvent)
	e.InvocationID = "m1"
	e.AttemptID = "a1"
	return Evolve(s, e)
}

func retryFixture(s *State, c *decisionCase) []Event {
	bindDecision(c, s, "retry")
	r := new(ModelRetryScheduledEvent)
	r.Envelope = c.facts.Envelope
	r.InvocationID = "m1"
	r.PreviousAttemptID = "a1"
	r.NewAttemptID = "retry-attempt"
	r.TimerID = "retry-timer"
	r.RetryNumber = 1
	r.PolicyVersion = "v1"
	p := new(ModelAttemptPlannedEvent)
	p.Envelope = r.Envelope
	p.Envelope.Sequence++
	p.InvocationID = r.InvocationID
	p.AttemptID = r.NewAttemptID
	p.PreviousAttemptID = r.PreviousAttemptID
	timer := new(TimerScheduledEvent)
	timer.Envelope = p.Envelope
	timer.Envelope.Sequence++
	timer.TimerID = r.TimerID
	timer.Purpose = TimerPurposeModelRetry
	timer.Generation = 1
	return []Event{r, p, timer}
}

func TestUnknownControlClosurePreservesExposure(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 8)
	c := cases[8]
	bindDecision(&c, s, "unknown")
	unknown := new(ToolOutcomeUnknownEvent)
	unknown.Envelope = c.facts.Envelope
	unknown.ToolCallID = "t1"
	unknown.AttemptID = decToolAttempt
	interrupted := new(RunInterruptedEvent)
	interrupted.Envelope = unknown.Envelope
	interrupted.Envelope.Sequence++
	interrupted.CauseID = "incident"
	interrupted.Cause = testUnknownCause
	interrupted.AttemptRef = decToolAttempt
	first := []Event{unknown, interrupted}
	if ids := CheckBatch(s, first, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	for _, e := range first {
		s = Evolve(s, e)
	}
	events := unknownCloseFixture(s, &c)
	if ids := CheckBatch(s, events, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	if !s.ToolInvocations["t1"].Attempts[decToolAttempt].ExposureUnknown || s.Outcome != RunOutcomeAbandonedUnknown {
		t.Fatal("closure erased unknown exposure")
	}
}

func unknownCloseFixture(s *State, c *decisionCase) []Event {
	bindDecision(c, s, "close")
	accepted := new(UnknownAcceptedEvent)
	accepted.Envelope = c.facts.Envelope
	accepted.TargetAttempts = []string{decToolAttempt}
	accepted.Disposition = ControlDispositionClose
	result := new(ToolResultRecordedEvent)
	result.Envelope = accepted.Envelope
	result.Envelope.Sequence++
	result.ToolCallID = "t1"
	result.AttemptID = decToolAttempt
	result.Outcome = ToolResultOutcomeAbandonedUnknown
	result.ResultRef = "unknown-result"
	cleared := new(RunRecoveryClearedEvent)
	cleared.Envelope = result.Envelope
	cleared.Envelope.Sequence++
	cleared.CauseIDs = []string{"incident"}
	cleared.Disposition = RecoveryDispositionAcceptedUnknown
	cleared.NextAction = NextActionClose
	aborted := new(StepAbortedEvent)
	aborted.Envelope = cleared.Envelope
	aborted.Envelope.Sequence++
	aborted.StepIndex = 1
	aborted.Reason = "unknown_close"
	finished := new(RunFinishedEvent)
	finished.Envelope = aborted.Envelope
	finished.Envelope.Sequence++
	finished.Outcome = RunOutcomeAbandonedUnknown
	return []Event{accepted, result, cleared, aborted, finished}
}

func TestToolReconciliationNeedsOwnerEvidence(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 8)
	c := cases[8]
	bindDecision(&c, s, "classified")
	u := new(ToolOutcomeUnknownEvent)
	u.Envelope = c.facts.Envelope
	u.ToolCallID = "t1"
	u.AttemptID = decToolAttempt
	cause := new(RunInterruptedEvent)
	cause.Envelope = u.Envelope
	cause.Envelope.Sequence++
	cause.CauseID = "reconciliation"
	cause.Cause = testUnknownCause
	s = Evolve(Evolve(s, u), cause)
	bindDecision(&c, s, "reconciled")
	r := new(ToolReconciledEvent)
	r.Envelope = c.facts.Envelope
	r.ToolCallID = "t1"
	r.AttemptID = decToolAttempt
	r.OperationReceipt = "owner-operation-proof"
	r.ObservedOutcome = ToolOutcomeUnknownObservedDisconnected
	result := new(ToolResultRecordedEvent)
	result.Envelope = r.Envelope
	result.Envelope.Sequence++
	result.ToolCallID = "t1"
	result.AttemptID = decToolAttempt
	result.Outcome = ToolResultOutcomeSucceeded
	result.ResultRef = "reconciled-result"
	clearCause := new(RunRecoveryClearedEvent)
	clearCause.Envelope = result.Envelope
	clearCause.Envelope.Sequence++
	clearCause.CauseIDs = []string{cause.CauseID}
	clearCause.Disposition = RecoveryDispositionReconciled
	events := []Event{r, result, clearCause}
	if ids := CheckBatch(s, events, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	r.OperationReceipt = ""
	if ids := CheckBatch(s, events, &c.facts); !slices.Contains(ids, SM09) || !slices.Contains(ids, SM11) {
		t.Fatal(ids)
	}
}
