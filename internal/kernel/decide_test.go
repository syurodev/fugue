package kernel

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const (
	decToolAttempt = "ta1"
	decOrigin      = "host-bound"
)

type decisionCase struct {
	command Command
	facts   DecisionFacts
	names   []string
}

func modelPlan(id, attempt string) *ModelRequestPlannedEvent {
	return &ModelRequestPlannedEvent{
		Envelope: Envelope{}, InvocationID: id, Purpose: "turn",
		FirstAttemptID: attempt, ModelRef: "model-ref", ContextRef: decContext,
		ToolDeclarationRefs: []string{"declaration-ref"}, EstimatedBudgetTokens: 10,
	}
}

func modelDispatch(id, attempt string) *ModelDispatchStartedEvent {
	return &ModelDispatchStartedEvent{
		Envelope: Envelope{}, InvocationID: id, AttemptID: attempt,
		Token: 7, ExecutorFence: 11, ReservationTokens: 10,
		ProviderSettingsRef: "settings-ref", AccountID: "account-id", CatalogVersion: "catalog-v1",
	}
}

func traceCases() []decisionCase {
	f := DecisionFacts{
		OwnerIncarnation: 3, ExecutorFence: 11, WorkerReservationRef: "worker-reservation",
		DispatchAuthorizationRef: "current-authorization", BudgetReservationRef: "tool-budget-ref", ContextRefs: []string{decContext},
	}
	accept := new(AcceptRunCommand)
	accept.InputRef, accept.SessionID, accept.QueueItemRef, accept.ProtocolBinding = "input-ref", "session", "queue-ref", "config-ref"
	accept.Budgets = DefaultRunBudgets()
	admit := new(AdmitRunCommand)
	admit.Token, admit.OwnerIncarnation, admit.QueueDecisionRef = 7, 3, "queue-decision"
	admit.ContextEpoch, admit.InitialContextRevision, admit.StartRunID = 1, 1, testTargetRun
	first := f
	first.ModelPlan = modelPlan("m1", "a1")
	p1 := f
	p1.ModelDispatch = modelDispatch("m1", "a1")
	p2 := f
	p2.ModelDispatch = modelDispatch("m2", "a2")
	r1 := f
	r1.Usage = &BudgetObservedEvent{Envelope: Envelope{}, AttemptID: "a1", WindowID: "", TokensUsed: 8, DurationUsed: 0, Basis: "provider"}
	r1.ToolPlans = []*ToolCallPlannedEvent{{
		Envelope: Envelope{}, ToolCallID: "t1", SourcePosition: 0,
		DescriptorVersion: "descriptor-v1", EffectClass: EffectClassPureRead, FirstAttemptID: decToolAttempt, InputRef: "raw-args-ref", Terminates: false, RepeatsPrevious: false,
	}}
	r2 := f
	r2.Usage = &BudgetObservedEvent{Envelope: Envelope{}, AttemptID: "a2", WindowID: "", TokensUsed: 9, DurationUsed: 0, Basis: "provider"}
	next := f
	next.ModelPlan = modelPlan("m2", "a2")
	return []decisionCase{
		{accept, f, []string{"RunAccepted"}},
		{admit, first, []string{"RunAdmitted", new(StepStartedEvent).EventName(), new(ModelRequestPlannedEvent).EventName()}},
		{&PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: "m1", AttemptID: "a1"}, p1, []string{new(ModelDispatchStartedEvent).EventName()}},
		{&ObserveModelStreamCommand{CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: "a1", InvocationID: "m1", FirstChunkRef: "chunk-ref"}, f, []string{new(ModelStreamStartedEvent).EventName()}},
		{&ObserveModelResponseCommand{CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: "a1", InvocationID: "m1", ResponseRef: "response-ref", UsageStatus: "known", FinishReason: FinishReasonToolUse}, r1, []string{new(ModelResponseRecordedEvent).EventName(), new(BudgetObservedEvent).EventName(), "ToolCallPlanned"}},
		{&PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: "t1", AttemptID: decToolAttempt}, f, []string{"ToolCheckStarted"}},
		{&ObserveToolCheckCommand{CommandID: "", RunID: "", Origin: "", Token: 7, ToolCallID: "t1", AttemptID: decToolAttempt, CheckGeneration: 1, ValidatedArgsRef: "validated-args-ref", GateObservation: "allowed", ApprovalReceiptRef: ""}, f, []string{"ToolReadyRecorded"}},
		{&PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: "t1", AttemptID: decToolAttempt}, f, []string{"ToolDispatchStarted"}},
		{&ObserveToolResultCommand{CommandID: "", RunID: "", Origin: "", Token: 7, ToolCallID: "t1", AttemptID: decToolAttempt, Outcome: string(ToolResultOutcomeSucceeded), ResultRef: "result-ref", Code: "ok", EffectEvidence: "completion-ref", SourcePosition: 0}, next, []string{"ToolResultRecorded", new(StepCompletedEvent).EventName(), new(StepStartedEvent).EventName(), new(ModelRequestPlannedEvent).EventName()}},
		{&PrepareDispatchCommand{CommandID: "", RunID: "", Origin: "", Token: 7, InvocationID: "m2", AttemptID: "a2"}, p2, []string{new(ModelDispatchStartedEvent).EventName()}},
		{&ObserveModelStreamCommand{CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: "a2", InvocationID: "m2", FirstChunkRef: "chunk-ref"}, f, []string{new(ModelStreamStartedEvent).EventName()}},
		{&ObserveModelResponseCommand{CommandID: "", RunID: "", Origin: "", Token: 7, AttemptID: "a2", InvocationID: "m2", ResponseRef: "final-ref", UsageStatus: "known", FinishReason: FinishReasonStop}, r2, []string{new(ModelResponseRecordedEvent).EventName(), new(BudgetObservedEvent).EventName(), new(StepCompletedEvent).EventName(), "RunFinished"}},
	}
}

func bindDecision(c *decisionCase, s *State, id string) {
	c.facts.Envelope = Envelope{
		RunID: testTargetRun, CommandID: id, Origin: decOrigin, Token: 7,
		CausationID: "cause", Sequence: s.LastSequence + 1, Timestamp: time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
	}
	switch cmd := c.command.(type) {
	case *AcceptRunCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
		c.facts.Envelope.Token = 0
	case *AdmitRunCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *PrepareDispatchCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *ObserveModelStreamCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *ObserveModelResponseCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *ObserveToolCheckCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *ObserveToolResultCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *AdvanceRunCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *ExtendBudgetCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	case *RequestStopCommand:
		cmd.CommandID, cmd.RunID, cmd.Origin = id, testTargetRun, decOrigin
	}
}

func traceState(t *testing.T, count int) (*State, []decisionCase) {
	t.Helper()
	s := NewState()
	cases := traceCases()
	for i := range count {
		bindDecision(&cases[i], s, fmt.Sprintf("command-%d", i))
		events, err := Decide(s, cases[i].command, &cases[i].facts)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			s = Evolve(s, event)
		}
	}
	return s, cases
}

func TestDecideTraceA(t *testing.T) {
	t.Parallel()
	s := NewState()
	replay := NewState()
	cases := traceCases()
	for i := range cases {
		c := &cases[i]
		bindDecision(c, s, fmt.Sprintf("command-%d", i))
		before := cloneState(s)
		events, err := Decide(s, c.command, &c.facts)
		if err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(&before, s); diff != "" {
			t.Fatal(diff)
		}
		assertDecisionBatch(t, s, c, events)
		for _, event := range events {
			replay = Evolve(replay, event)
		}
		for _, event := range events {
			s = Evolve(s, event)
		}
	}
	if s.CurrentStepIndex != 2 || s.LastSequence != 22 || s.Outcome != RunOutcomeCompleted || DeriveLifecycle(s) != LifecycleFinished {
		t.Fatal("trace did not finish")
	}
	if diff := cmp.Diff(s, replay); diff != "" {
		t.Fatal(diff)
	}
}

func TestDecideDuplicateObservations(t *testing.T) {
	t.Parallel()
	for _, index := range []int{2, 3, 4, 6, 8, 11} {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, index+1)
			c := cases[index]
			bindDecision(&c, s, "duplicate")
			events, err := Decide(s, c.command, &c.facts)
			if err != nil || len(events) != 0 {
				t.Fatalf("duplicate: %v, %d events", err, len(events))
			}
		})
	}
}

func TestDecideRejectsInvalidTransitions(t *testing.T) {
	t.Parallel()
	for _, index := range []int{2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		for _, mutation := range []string{"token", "owner", testTargetRun, "id", "sequence", decOriginField, "finished"} {
			t.Run(fmt.Sprintf("%d/%s", index, mutation), func(t *testing.T) {
				t.Parallel()
				s, cases := traceState(t, index)
				c := cases[index]
				bindDecision(&c, s, "invalid")
				switch mutation {
				case "token":
					c.facts.Envelope.Token++
				case "owner":
					c.facts.OwnerIncarnation++
				case testTargetRun:
					c.facts.Envelope.RunID = "other-run"
				case "id":
					c.facts.Envelope.CommandID = "other-command"
				case "sequence":
					c.facts.Envelope.Sequence++
				case decOriginField:
					c.facts.Envelope.Origin = "untrusted"
				case "finished":
					s.Finished = true
				}
				assertDecisionRejected(t, s, &c)
			})
		}
	}
}

func assertDecisionRejected(t *testing.T, s *State, c *decisionCase) {
	t.Helper()
	before := cloneState(s)
	events, err := Decide(s, c.command, &c.facts)
	var rejection Rejection
	if !errors.As(err, &rejection) || len(events) != 0 {
		t.Fatalf("expected rejection, got %v and %d events", err, len(events))
	}
	if diff := cmp.Diff(&before, s); diff != "" {
		t.Fatal(diff)
	}
}

func TestDecideFailsClosed(t *testing.T) {
	t.Parallel()
	for _, index := range []int{1, 2, 4, 5, 6, 7, 8, 11} {
		for _, kind := range []string{"block", "terminal-intent", decUnknown, "quiesce", "context", "timer"} {
			t.Run(fmt.Sprintf("%d/%s", index, kind), func(t *testing.T) {
				t.Parallel()
				s, cases := traceState(t, index)
				c := cases[index]
				bindDecision(&c, s, "blocked")
				switch kind {
				case "block":
					s.Blocks["unknown-kind"] = nil
				case "terminal-intent":
					s.StopIntent = &StopIntent{Mode: StopModeTerminal}
				case decUnknown:
					s.InterruptionCauses[decUnknown] = nil
				case "quiesce":
					s.QuiescingAttempts["quiesce"] = true
				case "context":
					s.ContextRequirement = ContextRequirementFreshRequired
				case "timer":
					s.ActiveTimers["timer"] = nil
				}
				assertDecisionRejected(t, s, &c)
			})
		}
	}
}

func TestDecideNilAndSequenceOverflow(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 2)
	c := cases[2]
	bindDecision(&c, s, "invalid")
	for _, command := range []Command{nil, (*AcceptRunCommand)(nil), (*AdmitRunCommand)(nil), (*AdvanceRunCommand)(nil), (*PrepareDispatchCommand)(nil), (*ObserveModelStreamCommand)(nil), (*ObserveModelResponseCommand)(nil), (*ObserveToolCheckCommand)(nil), (*ObserveToolResultCommand)(nil), new(RequestStopCommand)} {
		c.command = command
		assertDecisionRejected(t, s, &c)
	}
	s.LastSequence = math.MaxUint64
	c.facts.Envelope.Sequence = 0
	assertDecisionRejected(t, s, &c)
	_, err := Decide(nil, cases[0].command, &DecisionFacts{})
	if err == nil {
		t.Fatal("accepted missing metadata")
	}
}

func assertDecisionBatch(t *testing.T, s *State, c *decisionCase, events []Event) {
	t.Helper()
	names := make([]string, 0, len(events))
	for j, event := range events {
		names = append(names, event.EventName())
		env := event.EventEnvelope()
		expected := c.facts.Envelope
		expected.Sequence = s.LastSequence + uint64(j) + 1
		if diff := cmp.Diff(expected, env); diff != "" {
			t.Fatal(diff)
		}
	}
	if diff := cmp.Diff(c.names, names); diff != "" {
		t.Fatal(diff)
	}
}
