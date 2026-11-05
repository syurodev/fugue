package kernel

import (
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	cutRef0 = "cut-0"
	cutRef1 = "cut-1"
)

// kinds spells an expected batch by event type, so the tests name no event-name literals.
func kinds(events ...Event) []string { return eventNames(events) }

func stepCompleted(t *testing.T, events []Event) *StepCompletedEvent {
	t.Helper()
	e, ok := findEvent[*StepCompletedEvent](events)
	if !ok {
		t.Fatalf("no StepCompleted in %v", eventNames(events))
	}
	return e
}

func assertRejected(t *testing.T, r *runDriver, events []Event, err error, before *State) {
	t.Helper()
	var rejection Rejection
	if !errors.As(err, &rejection) || len(events) != 0 {
		t.Fatalf("expected a rejection, got %v and %d events", err, len(events))
	}
	if diff := cmp.Diff(before, r.s); diff != "" {
		t.Fatal(diff)
	}
}

func TestLengthAutoContinuesOnce(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	first := r.model(1, FinishReasonLength, nil, nil)
	want := kinds(new(ModelResponseRecordedEvent), new(BudgetObservedEvent), new(StepCompletedEvent))
	if diff := cmp.Diff(want, eventNames(first)); diff != "" {
		t.Fatal(diff)
	}
	if got := stepCompleted(t, first); got.Reason != ContinuationReasonTruncatedContinue || got.Continuation != StepContinuationContinue {
		t.Fatalf("first length answer: %+v", got)
	}
	if !r.s.AutoContinued || r.s.Finished {
		t.Fatal("run did not record its one automatic continuation")
	}
	second, err := r.advance(2, func(f *DecisionFacts) { f.ContinuationPromptRef = drvPrompt })
	if err != nil {
		t.Fatal(err)
	}
	want = kinds(new(StepStartedEvent), new(ContextInjectedEvent), new(ModelRequestPlannedEvent))
	if diff := cmp.Diff(want, eventNames(second)); diff != "" {
		t.Fatal(diff)
	}
	injected, _ := findEvent[*ContextInjectedEvent](second)
	if injected.Source != string(ContinuationReasonTruncatedContinue) || injected.PayloadRef != drvPrompt {
		t.Fatalf("continuation injection: %+v", injected)
	}
	assertTruncatedEnd(t, r, r.model(2, FinishReasonLength, nil, nil))
}

func assertTruncatedEnd(t *testing.T, r *runDriver, events []Event) {
	t.Helper()
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonTruncatedEnd || got.Continuation != StepContinuationEnd {
		t.Fatalf("second length answer: %+v", got)
	}
	finished, ok := findEvent[*RunFinishedEvent](events)
	if !ok || !finished.Truncated || finished.Outcome != RunOutcomeCompleted || finished.FailureCode != "" {
		t.Fatalf("second length answer finished as %+v", finished)
	}
	if !r.s.Finished || r.s.StepsUsed != 2 {
		t.Fatalf("finished=%v steps=%d: the automatic step must count as a step", r.s.Finished, r.s.StepsUsed)
	}
}

func TestTruncatedContinueRequiresPromptRef(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	r.model(1, FinishReasonLength, nil, nil)
	before := cloneState(r.s)
	events, err := r.advance(2, nil)
	var rejection Rejection
	if !errors.As(err, &rejection) || rejection.Code != RejectionCodeCommandRejected {
		t.Fatalf("advance without a prompt ref: %v", err)
	}
	assertRejected(t, r, events, err, &before)
}

func TestTruncatedToolCallsNeverDispatch(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	tools := []*ToolCallPlannedEvent{driverToolPlan(1, 0), driverToolPlan(1, 1)}
	events := r.model(1, FinishReasonLength, tools, func(f *DecisionFacts) { f.SyntheticResultRefs = []string{cutRef0, cutRef1} })
	want := kinds(
		new(ModelResponseRecordedEvent), new(BudgetObservedEvent), new(ToolCallPlannedEvent), new(ToolCallPlannedEvent),
		new(ToolResultRecordedEvent), new(ToolResultRecordedEvent), new(StepCompletedEvent),
	)
	if diff := cmp.Diff(want, eventNames(events)); diff != "" {
		t.Fatal(diff)
	}
	assertSyntheticResults(t, events[4:6])
	done := stepCompleted(t, events)
	if done.Reason != ContinuationReasonTruncatedArgs || done.Continuation != StepContinuationContinue ||
		!cmp.Equal(done.OrderedResultRefs, []string{cutRef0, cutRef1}) {
		t.Fatalf("step completion: %+v", done)
	}
	if _, ok := findEvent[*ToolDispatchStartedEvent](r.history); ok || r.s.ToolCallsDispatched != 0 {
		t.Fatal("a truncated call was dispatched")
	}
	if r.s.AutoContinued {
		t.Fatal("truncated tool calls must not use the automatic text continuation")
	}
	if _, err := r.advance(2, nil); err != nil {
		t.Fatalf("the model cannot retry after truncated calls: %v", err)
	}
}

func assertSyntheticResults(t *testing.T, events []Event) {
	t.Helper()
	for i, e := range events {
		result, ok := e.(*ToolResultRecordedEvent)
		if !ok || result.Outcome != ToolResultOutcomeRefused || result.Code != string(ContinuationReasonTruncatedArgs) ||
			result.SourcePosition != i || result.ResultRef != []string{cutRef0, cutRef1}[i] {
			t.Fatalf("synthetic result %d: %+v", i, e)
		}
	}
}

func TestTruncatedToolCallsNeedSyntheticRefs(t *testing.T) {
	t.Parallel()
	for name, refs := range map[string][]string{"none": nil, "short": {cutRef0}, "empty": {cutRef0, ""}, "long": {cutRef0, cutRef1, "cut-2"}} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRunDriver(t, DefaultRunBudgets())
			r.startModel(1)
			before := cloneState(r.s)
			tools := []*ToolCallPlannedEvent{driverToolPlan(1, 0), driverToolPlan(1, 1)}
			events, err := r.respond(1, FinishReasonLength, tools, func(f *DecisionFacts) { f.SyntheticResultRefs = refs })
			assertRejected(t, r, events, err, &before)
		})
	}
}

func TestResponseFinishReasonMustMatchToolCalls(t *testing.T) {
	t.Parallel()
	one := func() []*ToolCallPlannedEvent { return []*ToolCallPlannedEvent{driverToolPlan(1, 0)} }
	cases := map[string]struct {
		finish FinishReason
		tools  []*ToolCallPlannedEvent
	}{
		"empty":             {"", nil},
		"unknown":           {"end_turn", nil},
		"tool use no tools": {FinishReasonToolUse, nil},
		"stop with tools":   {FinishReasonStop, one()},
		"refusal with tool": {FinishReasonRefusal, one()},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRunDriver(t, DefaultRunBudgets())
			r.startModel(1)
			before := cloneState(r.s)
			events, err := r.respond(1, tc.finish, tc.tools, nil)
			assertRejected(t, r, events, err, &before)
		})
	}
}

func TestRefusalFinishesFailedWithRefusedCode(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	events := r.model(1, FinishReasonRefusal, nil, nil)
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonRefused || got.Continuation != StepContinuationEnd {
		t.Fatalf("refusal step: %+v", got)
	}
	finished, ok := findEvent[*RunFinishedEvent](events)
	if !ok || finished.Outcome != RunOutcomeFailed || finished.FailureCode != failureCodeRefused || finished.Truncated {
		t.Fatalf("refusal finished as %+v", finished)
	}
	if r.s.Outcome != RunOutcomeFailed || DeriveLifecycle(r.s) != LifecycleFinished {
		t.Fatal("run is not finished as failed")
	}
}

func TestToolTerminateEndsAfterStep(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	terminating := driverToolPlan(1, 0)
	terminating.Terminates = true
	r.model(1, FinishReasonToolUse, []*ToolCallPlannedEvent{terminating}, nil)
	events := r.tool(1, 0)
	want := kinds(new(ToolResultRecordedEvent), new(StepCompletedEvent), new(RunFinishedEvent))
	if diff := cmp.Diff(want, eventNames(events)); diff != "" {
		t.Fatal(diff)
	}
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonToolTerminate || got.Continuation != StepContinuationEnd {
		t.Fatalf("terminate step: %+v", got)
	}
	if finished, _ := findEvent[*RunFinishedEvent](events); finished.Outcome != RunOutcomeCompleted {
		t.Fatalf("terminate finished as %+v", finished)
	}
}

func TestToolTerminateNeedsEveryTool(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	terminating := driverToolPlan(1, 0)
	terminating.Terminates = true
	r.model(1, FinishReasonToolUse, []*ToolCallPlannedEvent{terminating, driverToolPlan(1, 1)}, nil)
	r.tool(1, 0)
	events := r.tool(1, 1)
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonToolCalls || r.s.Finished {
		t.Fatalf("a single non-terminating tool must keep the run going: %+v", got)
	}
}

func TestSteerPendingRunsOneMoreStep(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	events := r.model(1, FinishReasonStop, nil, func(f *DecisionFacts) { f.SteerPending = true })
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonSteerPending || got.Continuation != StepContinuationContinue {
		t.Fatalf("steer step: %+v", got)
	}
	if _, ok := findEvent[*RunFinishedEvent](events); ok {
		t.Fatal("run finished with a steer pending")
	}
	if _, err := r.advance(2, nil); err != nil {
		t.Fatal(err)
	}
	end := r.model(2, FinishReasonStop, nil, nil)
	if got := stepCompleted(t, end); got.Reason != ContinuationReasonNaturalEnd || !r.s.Finished {
		t.Fatalf("second step: %+v", got)
	}
}

func TestStepOutcomeTerminatesOnlyWhenEveryBatchPlanDoes(t *testing.T) {
	t.Parallel()
	plain, stop := driverToolPlan(1, 0), driverToolPlan(1, 1)
	stop.Terminates = true
	// The state is empty on purpose: a response batch has not been folded into it yet.
	d := &decision{state: NewState(), facts: &DecisionFacts{}, next: Envelope{}}
	cases := map[string]struct {
		plans []*ToolCallPlannedEvent
		want  bool
	}{
		"no plans":       {nil, false},
		"none terminate": {[]*ToolCallPlannedEvent{plain, driverToolPlan(1, 2)}, false},
		"one terminates": {[]*ToolCallPlannedEvent{plain, stop}, false},
		"all terminate":  {[]*ToolCallPlannedEvent{stop, stop}, true},
		"nil plan":       {[]*ToolCallPlannedEvent{stop, nil}, false},
	}
	for name, tc := range cases {
		got := d.stepOutcome(FinishReasonLength, tc.plans)
		if got.AllTerminate != tc.want || got.ToolCalls != len(tc.plans) {
			t.Errorf("%s: AllTerminate=%v ToolCalls=%d", name, got.AllTerminate, got.ToolCalls)
		}
	}
}
