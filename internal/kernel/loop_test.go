package kernel

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func repeatedPlan(step int) *ToolCallPlannedEvent {
	plan := driverToolPlan(step, 0)
	plan.RepeatsPrevious = step > 1
	return plan
}

// repeatCalls makes the same call in n consecutive steps and returns, per step,
// the events the tool's four commands produced.
func repeatCalls(t *testing.T, r *runDriver, n int) [][]Event {
	t.Helper()
	perStep := make([][]Event, 0, n)
	for step := 1; step <= n; step++ {
		r.model(step, FinishReasonToolUse, []*ToolCallPlannedEvent{repeatedPlan(step)}, nil)
		modelEnd := len(r.history)
		r.tool(step, 0)
		perStep = append(perStep, r.history[modelEnd:])
	}
	return perStep
}

func reminderEvents(events []Event) []*ContextInjectedEvent {
	var reminders []*ContextInjectedEvent
	for _, e := range events {
		if c, ok := e.(*ContextInjectedEvent); ok {
			reminders = append(reminders, c)
		}
	}
	return reminders
}

func TestLoopRemindersAtThreeFiveEight(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	perStep := repeatCalls(t, r, 8)
	levels := map[int]string{3: "level_3", 5: "level_5", 8: "level_8"}
	for i, events := range perStep {
		step := i + 1
		reminders := reminderEvents(events)
		tag, due := levels[step]
		switch {
		case !due && len(reminders) != 0:
			t.Fatalf("call %d got a reminder", step)
		case due && (len(reminders) != 1 || reminders[0].Tag != tag || reminders[0].Source != "loop_reminder" || reminders[0].PayloadRef != drvReminder):
			t.Fatalf("call %d reminders: %+v", step, reminders)
		}
	}
	assertReminderFollowsResult(t, perStep[2])
}

// assertReminderFollowsResult checks the reminder sits directly after the repeated call's result.
func assertReminderFollowsResult(t *testing.T, events []Event) {
	t.Helper()
	for i, e := range events {
		if _, ok := e.(*ToolResultRecordedEvent); ok {
			if _, next := events[i+1].(*ContextInjectedEvent); !next {
				t.Fatalf("the reminder must follow the result, got %s", events[i+1].EventName())
			}
		}
	}
}

func TestLoopReminderNeverBlocksCall(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	perStep := repeatCalls(t, r, 3)
	tools := func(events []Event) []string {
		var names []string
		for _, e := range events {
			if e.EventName() != new(ContextInjectedEvent).EventName() {
				names = append(names, e.EventName())
			}
		}
		return names
	}
	if diff := cmp.Diff(tools(perStep[1]), tools(perStep[2])); diff != "" {
		t.Fatalf("the third identical call ran differently from the second: %s", diff)
	}
	if r.s.ToolCallsDispatched != 3 || len(r.s.Blocks) != 0 {
		t.Fatalf("%d dispatched, %d blocks", r.s.ToolCallsDispatched, len(r.s.Blocks))
	}
}

func TestDueLoopReminderRequiresPayloadRef(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	for step := 1; step <= 3; step++ {
		r.model(step, FinishReasonToolUse, []*ToolCallPlannedEvent{repeatedPlan(step)}, nil)
		if step < 3 {
			r.tool(step, 0)
		}
	}
	for range 3 {
		r.toolStep(3, 0)
	}
	before := cloneState(r.s)
	events, err := r.try(driverResult(3, 0), func(f *DecisionFacts) {
		f.ModelPlan = modelPlan("m4", "a4")
		f.LoopReminderRef = ""
	})
	assertRejected(t, r, events, err, &before)
	if _, err = r.try(driverResult(3, 0), func(f *DecisionFacts) { f.ModelPlan = modelPlan("m4", "a4") }); err != nil {
		t.Fatalf("result with a reminder ref: %v", err)
	}
}

func TestLoopReminderDueLevels(t *testing.T) {
	t.Parallel()
	s := NewState()
	for repeat := range 11 {
		id := fmt.Sprintf("t%d", repeat)
		s.ToolInvocations[id] = &ToolInvocation{ToolCallID: id, Repeat: repeat}
		level, due := LoopReminderDue(s, id)
		want := repeat == 3 || repeat == 5 || repeat == 8
		if due != want || (due && level != repeat) || (!due && level != 0) {
			t.Fatalf("repeat %d: level %d due %v", repeat, level, due)
		}
	}
	if _, due := LoopReminderDue(s, "unknown"); due {
		t.Fatal("an unknown call is never due")
	}
}

func plannedCall(seq uint64, id string, repeats bool) *ToolCallPlannedEvent {
	return &ToolCallPlannedEvent{
		Envelope: budgetEnvelope(seq), ToolCallID: id, SourcePosition: 0, DescriptorVersion: drvDescriptor,
		EffectClass: EffectClassPureRead, FirstAttemptID: "att-" + id, InputRef: drvInputRef, Terminates: false, RepeatsPrevious: repeats,
	}
}

func TestLoopCounterResets(t *testing.T) {
	t.Parallel()
	counter := func(events ...Event) []int {
		s := NewState()
		seen := make([]int, 0, len(events))
		for _, e := range events {
			s = Evolve(s, e)
			seen = append(seen, s.LoopRepeat)
		}
		return seen
	}
	steer := &InputSteeredEvent{Envelope: budgetEnvelope(3), InputRef: "in", Origin: "user", Timestamp: budgetEnvelope(3).Timestamp}
	interject := &InputInterjectedEvent{Envelope: budgetEnvelope(3), InputRef: "in", MergedSteerRefs: nil, TargetAttemptID: "", TargetInvocationID: "", QuiesceResult: ""}
	cases := map[string]struct {
		events []Event
		want   []int
	}{
		"identical calls count up": {
			[]Event{plannedCall(1, "a", false), plannedCall(2, "b", true), plannedCall(3, "c", true)}, []int{1, 2, 3},
		},
		"another tool restarts at one": {
			[]Event{plannedCall(1, "a", false), plannedCall(2, "b", true), plannedCall(3, "c", false), plannedCall(4, "d", true)}, []int{1, 2, 1, 2},
		},
		"a steer resets to zero": {
			[]Event{plannedCall(1, "a", false), plannedCall(2, "b", true), steer, plannedCall(4, "c", false)}, []int{1, 2, 0, 1},
		},
		"an interjection resets to zero": {
			[]Event{plannedCall(1, "a", false), plannedCall(2, "b", true), interject}, []int{1, 2, 0},
		},
		"a replayed plan does not count twice": {
			[]Event{plannedCall(1, "a", false), plannedCall(2, "a", true)}, []int{1, 1},
		},
		"a new run starts at zero": {[]Event{}, []int{}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, counter(tc.events...)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
	if NewState().LoopRepeat != 0 {
		t.Fatal("a new run must start with no repetition")
	}
}

func TestCutToolCallsDoNotAdvanceLoopCounter(t *testing.T) {
	t.Parallel()
	response := &ModelResponseRecordedEvent{
		Envelope: budgetEnvelope(2), AttemptID: "a1", InvocationID: "m1", ResponseRef: "r", UsageStatus: drvKnown,
		FinishReason: FinishReasonLength,
	}
	s := NewState()
	modelCall := modelPlan("m1", "a1")
	modelCall.Envelope = budgetEnvelope(1)
	s = Evolve(s, modelCall)
	s = Evolve(s, plannedCall(3, "a", false))
	s = Evolve(s, plannedCall(4, "b", true))
	if s.LoopRepeat != 2 {
		t.Fatalf("LoopRepeat = %d before the cut answer", s.LoopRepeat)
	}
	s = Evolve(s, response)
	s = Evolve(s, plannedCall(5, "cut", true))
	if s.LoopRepeat != 2 || s.ToolInvocations["cut"].Repeat != 0 {
		t.Fatalf("a call that never ran counted: LoopRepeat=%d Repeat=%d", s.LoopRepeat, s.ToolInvocations["cut"].Repeat)
	}
}

func TestLoopReminderCountsOnlyCallsThatRan(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, DefaultRunBudgets())
	for step := 1; step <= 2; step++ {
		r.model(step, FinishReasonToolUse, []*ToolCallPlannedEvent{repeatedPlan(step)}, nil)
		r.tool(step, 0)
	}
	// Step 3 repeats the call again but the answer is cut, so the call never runs.
	r.model(3, FinishReasonLength, []*ToolCallPlannedEvent{repeatedPlan(3)}, func(f *DecisionFacts) {
		f.SyntheticResultRefs = []string{cutRef0}
	})
	if _, err := r.advance(4, nil); err != nil {
		t.Fatal(err)
	}
	// The next call that runs is the third that ran: its reminder is due.
	r.model(4, FinishReasonToolUse, []*ToolCallPlannedEvent{repeatedPlan(4)}, nil)
	events := r.tool(4, 0)
	reminders := reminderEvents(events)
	if len(reminders) != 1 || reminders[0].Tag != "level_3" {
		t.Fatalf("reminders after the cut call: %+v", reminders)
	}
}
