package kernel

import (
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	blockSteps     = "budget:steps"
	blockTokens    = "budget:tokens"
	blockToolCalls = "budget:tool_calls"
)

func budgetsWith(mutate func(*RunBudgets)) RunBudgets {
	b := DefaultRunBudgets()
	mutate(&b)
	return b
}

func extendCommand(kind BudgetKind, actor string, amount int64) *ExtendBudgetCommand {
	return &ExtendBudgetCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, Kind: kind, ExtensionAmount: amount, Actor: actor,
	}
}

func endCommand(target string) *RequestStopCommand {
	return &RequestStopCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, Reason: StopReasonBudget, Mode: StopModeTerminal,
		Actor: testUserActor, Target: target, RequestTime: time.Date(2026, 11, 5, 1, 0, 0, 0, time.UTC),
	}
}

func assertWaitingOn(t *testing.T, s *State, blockerID string, kind BudgetKind) {
	t.Helper()
	b := s.Blocks[blockerID]
	if b == nil || b.Kind != WaitReasonBudget || b.BudgetKind != kind || len(s.Blocks) != 1 {
		t.Fatalf("blocks = %+v, want one budget(%s) block", s.Blocks, kind)
	}
	if DeriveLifecycle(s) != LifecycleWaiting || !cmp.Equal(DeriveWaitReasons(s), []WaitReason{WaitReasonBudget}) || s.Finished {
		t.Fatalf("lifecycle %s, waits %v, finished %v", DeriveLifecycle(s), DeriveWaitReasons(s), s.Finished)
	}
}

// stepBudgetWait drives a run with a one-step budget to its step-budget wait.
func stepBudgetWait(t *testing.T, tools int) (*runDriver, []Event) {
	t.Helper()
	r := newRunDriver(t, budgetsWith(func(b *RunBudgets) { b.Steps = 1 }))
	plans := make([]*ToolCallPlannedEvent, 0, tools)
	for i := range tools {
		plans = append(plans, driverToolPlan(1, i))
	}
	r.model(1, FinishReasonToolUse, plans, nil)
	var last []Event
	for i := range tools {
		last = r.tool(1, i)
	}
	return r, last
}

func TestStepBudgetWaitsAfterToolsFinish(t *testing.T) {
	t.Parallel()
	r, last := stepBudgetWait(t, 2)
	want := kinds(new(ToolResultRecordedEvent), new(StepCompletedEvent), new(RunBlockAddedEvent))
	if diff := cmp.Diff(want, eventNames(last)); diff != "" {
		t.Fatal(diff)
	}
	done := stepCompleted(t, last)
	if done.Reason != ContinuationReasonBudget || done.BudgetKind != BudgetKindSteps || done.Continuation != StepContinuationContinue ||
		len(done.OrderedResultRefs) != 2 {
		t.Fatalf("step completion: %+v", done)
	}
	assertWaitingOn(t, r.s, blockSteps, BudgetKindSteps)
	if r.s.ToolCallsDispatched != 2 || r.s.StepsUsed != 1 {
		t.Fatalf("the step must run its tools first: %d dispatched, %d steps", r.s.ToolCallsDispatched, r.s.StepsUsed)
	}
	before := cloneState(r.s)
	events, err := r.advance(2, nil)
	assertRejected(t, r, events, err, &before)
}

func TestStepBudgetWaitsWithoutTools(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, budgetsWith(func(b *RunBudgets) { b.Steps = 1 }))
	events := r.model(1, FinishReasonStop, nil, func(f *DecisionFacts) { f.SteerPending = true })
	if got := stepCompleted(t, events); got.Reason != ContinuationReasonBudget {
		t.Fatalf("step completion: %+v", got)
	}
	assertWaitingOn(t, r.s, blockSteps, BudgetKindSteps)
}

func TestTokenBudgetWaitsAtStepBoundary(t *testing.T) {
	t.Parallel()
	limit := budgetsWith(func(b *RunBudgets) { b.Tokens = 10 })
	t.Run("after tools", func(t *testing.T) {
		t.Parallel()
		r := newRunDriver(t, limit)
		r.model(1, FinishReasonToolUse, []*ToolCallPlannedEvent{driverToolPlan(1, 0)}, nil)
		events := r.tool(1, 0)
		want := kinds(new(ToolResultRecordedEvent), new(StepCompletedEvent), new(RunBlockAddedEvent))
		if diff := cmp.Diff(want, eventNames(events)); diff != "" {
			t.Fatal(diff)
		}
		if got := stepCompleted(t, events); got.Reason != ContinuationReasonBudget || got.BudgetKind != BudgetKindTokens {
			t.Fatalf("step completion: %+v", got)
		}
		assertWaitingOn(t, r.s, blockTokens, BudgetKindTokens)
	})
	t.Run("at advance", func(t *testing.T) {
		t.Parallel()
		r := newRunDriver(t, limit)
		r.model(1, FinishReasonLength, nil, nil)
		events, err := r.advance(2, func(f *DecisionFacts) { f.ContinuationPromptRef = drvPrompt })
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(kinds(new(RunBlockAddedEvent)), eventNames(events)); diff != "" {
			t.Fatal(diff)
		}
		assertWaitingOn(t, r.s, blockTokens, BudgetKindTokens)
		r.must(extendCommand(BudgetKindTokens, testUserActor, DefaultExtension(BudgetKindTokens)), nil)
		resumed, err := r.advance(2, func(f *DecisionFacts) { f.ContinuationPromptRef = drvPrompt })
		if err != nil {
			t.Fatal(err)
		}
		want := kinds(new(StepStartedEvent), new(ContextInjectedEvent), new(ModelRequestPlannedEvent))
		if diff := cmp.Diff(want, eventNames(resumed)); diff != "" {
			t.Fatalf("the continuation prompt must survive the wait: %s", diff)
		}
	})
}

func TestToolCallBudgetWaitsBeforeDispatch(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, budgetsWith(func(b *RunBudgets) { b.ToolCalls = 1 }))
	r.model(1, FinishReasonToolUse, []*ToolCallPlannedEvent{driverToolPlan(1, 0), driverToolPlan(1, 1)}, nil)
	r.tool(1, 0)
	second := &PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: "t1-1", AttemptID: "ta1-1"}
	r.must(second, nil)
	r.must(&ObserveToolCheckCommand{
		CommandID: "", RunID: "", Origin: "", Token: 7, ToolCallID: "t1-1", AttemptID: "ta1-1", CheckGeneration: 1,
		ValidatedArgsRef: drvArgsRef, GateObservation: drvAllowed, ApprovalReceiptRef: "",
	}, nil)
	events := r.must(second, nil)
	if diff := cmp.Diff(kinds(new(RunBlockAddedEvent)), eventNames(events)); diff != "" {
		t.Fatal(diff)
	}
	assertWaitingOn(t, r.s, blockToolCalls, BudgetKindToolCalls)
	if r.s.ToolCallsDispatched != 1 || r.s.ToolInvocations["t1-1"].Attempts["ta1-1"].Status != ToolAttemptStatusReady {
		t.Fatal("the second call must stay ready and undispatched")
	}
	r.must(extendCommand(BudgetKindToolCalls, testUserActor, DefaultExtension(BudgetKindToolCalls)), nil)
	dispatched := r.must(second, nil)
	if diff := cmp.Diff(kinds(new(ToolDispatchStartedEvent)), eventNames(dispatched)); diff != "" {
		t.Fatalf("the extension must resume the same step: %s", diff)
	}
}

func TestOnlyUserExtendsBudget(t *testing.T) {
	t.Parallel()
	for _, actor := range []string{actorPeer, "parent_session", actorModel, "goal", "", "User"} {
		t.Run(actor, func(t *testing.T) {
			t.Parallel()
			r, _ := stepBudgetWait(t, 1)
			before := cloneState(r.s)
			events, err := r.try(extendCommand(BudgetKindSteps, actor, 50), nil)
			assertRejected(t, r, events, err, &before)
		})
	}
	r, _ := stepBudgetWait(t, 1)
	events := r.must(extendCommand(BudgetKindSteps, testUserActor, 50), nil)
	if diff := cmp.Diff(kinds(new(BudgetExtendedEvent), new(RunBlockClearedEvent)), eventNames(events)); diff != "" {
		t.Fatal(diff)
	}
}

func freshRun(t *testing.T) *runDriver {
	t.Helper()
	return newRunDriver(t, DefaultRunBudgets())
}

func TestExtensionForKindNotWaitingIsRejected(t *testing.T) {
	t.Parallel()
	waiting := func(t *testing.T) *runDriver {
		t.Helper()
		r, _ := stepBudgetWait(t, 1)
		return r
	}
	cases := map[string]struct {
		driver  func(t *testing.T) *runDriver
		command *ExtendBudgetCommand
	}{
		"other kind":      {waiting, extendCommand(BudgetKindTokens, testUserActor, 5)},
		"time kind":       {waiting, extendCommand(BudgetKindTime, testUserActor, 5)},
		"unknown kind":    {waiting, extendCommand("cost", testUserActor, 5)},
		"zero amount":     {waiting, extendCommand(BudgetKindSteps, testUserActor, 0)},
		"negative amount": {waiting, extendCommand(BudgetKindSteps, testUserActor, -5)},
		"overflow": {waiting, extendCommand(
			BudgetKindSteps, testUserActor, 1<<63-1)},
		"not waiting": {freshRun, extendCommand(BudgetKindSteps, testUserActor, 5)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := tc.driver(t)
			before := cloneState(r.s)
			events, err := r.try(tc.command, nil)
			assertRejected(t, r, events, err, &before)
		})
	}
}

func TestExtensionContinuesSameRun(t *testing.T) {
	t.Parallel()
	r, _ := stepBudgetWait(t, 1)
	run, steps, tokens := r.s.RunID, r.s.StepsUsed, r.s.TokensUsed
	r.must(extendCommand(BudgetKindSteps, testUserActor, DefaultExtension(BudgetKindSteps)), nil)
	if r.s.BudgetLimit(BudgetKindSteps) != 51 || len(r.s.Blocks) != 0 || DeriveLifecycle(r.s) != LifecycleRunning {
		t.Fatalf("limit %d, blocks %d, lifecycle %s", r.s.BudgetLimit(BudgetKindSteps), len(r.s.Blocks), DeriveLifecycle(r.s))
	}
	if r.s.RunID != run || r.s.StepsUsed != steps || r.s.TokensUsed != tokens {
		t.Fatal("an extension must not reset counters or change the run")
	}
	events, err := r.advance(2, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, injected := findEvent[*ContextInjectedEvent](events); injected {
		t.Fatal("a budget wait is not a truncated continuation")
	}
	if r.s.CurrentStepIndex != 2 || r.s.StepsUsed != 2 {
		t.Fatalf("step %d, steps used %d: expected step 2 of the same run", r.s.CurrentStepIndex, r.s.StepsUsed)
	}
	assertExtendedRunKeepsCounting(t, r, run)
}

func assertExtendedRunKeepsCounting(t *testing.T, r *runDriver, run string) {
	t.Helper()
	r.model(2, FinishReasonToolUse, []*ToolCallPlannedEvent{driverToolPlan(2, 0)}, nil)
	r.tool(2, 0)
	if r.s.CurrentStepIndex != 3 || r.s.StepsUsed != 3 || r.s.ToolCallsDispatched != 2 || r.s.RunID != run {
		t.Fatalf("the extended run did not keep counting: step %d, %d steps, %d calls", r.s.CurrentStepIndex, r.s.StepsUsed, r.s.ToolCallsDispatched)
	}
}

func TestEndWhileWaitingFinishesBudgetExceeded(t *testing.T) {
	t.Parallel()
	r, _ := stepBudgetWait(t, 1)
	events := r.must(endCommand(blockSteps), nil)
	want := kinds(new(RunStopRequestedEvent), new(RunBlockClearedEvent), new(RunFinishedEvent))
	if diff := cmp.Diff(want, eventNames(events)); diff != "" {
		t.Fatal(diff)
	}
	stop, _ := findEvent[*RunStopRequestedEvent](events)
	finished, _ := findEvent[*RunFinishedEvent](events)
	if stop.Reason != StopReasonBudget || stop.Mode != StopModeTerminal || stop.Actor != testUserActor || stop.Target != blockSteps {
		t.Fatalf("stop request: %+v", stop)
	}
	if finished.Outcome != RunOutcomeBudgetExceeded || finished.PrimaryStopRef != "run#"+strconv.FormatUint(stop.Envelope.Sequence, 10) {
		t.Fatalf("finish: %+v", finished)
	}
	if !r.s.Finished || DeriveLifecycle(r.s) != LifecycleFinished || len(DeriveWaitReasons(r.s)) != 0 {
		t.Fatal("a run ended while waiting must be finished with no wait reasons")
	}
}

func TestEndWithoutBudgetWaitIsRejected(t *testing.T) {
	t.Parallel()
	type tweak func(c *RequestStopCommand)
	cases := map[string]tweak{
		"user cancel":   func(c *RequestStopCommand) { c.Reason = StopReasonUserCancel },
		"suspend":       func(c *RequestStopCommand) { c.Mode = StopModeSuspend },
		"peer":          func(c *RequestStopCommand) { c.Actor = actorPeer },
		"model":         func(c *RequestStopCommand) { c.Actor = actorModel },
		"other block":   func(c *RequestStopCommand) { c.Target = blockTokens },
		"no block":      func(c *RequestStopCommand) { c.Target = "" },
		"no time":       func(c *RequestStopCommand) { c.RequestTime = time.Time{} },
		"foreign token": func(c *RequestStopCommand) { c.Token = 8 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r, _ := stepBudgetWait(t, 1)
			before := cloneState(r.s)
			c := endCommand(blockSteps)
			mutate(c)
			events, err := r.try(c, nil)
			assertRejected(t, r, events, err, &before)
		})
	}
	t.Run("running run", func(t *testing.T) {
		t.Parallel()
		r := newRunDriver(t, DefaultRunBudgets())
		before := cloneState(r.s)
		events, err := r.try(endCommand(blockSteps), nil)
		assertRejected(t, r, events, err, &before)
		var rejection Rejection
		if !errors.As(err, &rejection) || rejection.Code != RejectionCodeInvalidTransition {
			t.Fatalf("code = %v", err)
		}
	})
	t.Run("active attempt", func(t *testing.T) {
		t.Parallel()
		r, _ := stepBudgetWait(t, 1)
		r.s.QuiescingAttempts["attempt"] = true
		before := cloneState(r.s)
		events, err := r.try(endCommand(blockSteps), nil)
		assertRejected(t, r, events, err, &before)
	})
	t.Run("already finished", func(t *testing.T) {
		t.Parallel()
		r, _ := stepBudgetWait(t, 1)
		r.must(endCommand(blockSteps), nil)
		before := cloneState(r.s)
		events, err := r.try(endCommand(blockSteps), nil)
		assertRejected(t, r, events, err, &before)
	})
}

func TestEndDuringToolCallWaitIsRejected(t *testing.T) {
	t.Parallel()
	r := newRunDriver(t, budgetsWith(func(b *RunBudgets) { b.ToolCalls = 1 }))
	r.model(1, FinishReasonToolUse, []*ToolCallPlannedEvent{driverToolPlan(1, 0), driverToolPlan(1, 1)}, nil)
	r.tool(1, 0)
	r.toolStep(1, 1)
	r.toolStep(1, 1)
	r.toolStep(1, 1)
	assertWaitingOn(t, r.s, blockToolCalls, BudgetKindToolCalls)
	before := cloneState(r.s)
	events, err := r.try(endCommand(blockToolCalls), nil)
	assertRejected(t, r, events, err, &before)
	r.must(extendCommand(BudgetKindToolCalls, testUserActor, 1), nil)
}

func TestEndAndExtendRejectNilCommands(t *testing.T) {
	t.Parallel()
	r, _ := stepBudgetWait(t, 1)
	c := decisionCase{command: endCommand(blockSteps), facts: driverFacts(), names: nil}
	bindDecision(&c, r.s, "nil-command")
	for _, command := range []Command{(*ExtendBudgetCommand)(nil), (*RequestStopCommand)(nil)} {
		c.command = command
		assertDecisionRejected(t, r.s, &c)
	}
}
