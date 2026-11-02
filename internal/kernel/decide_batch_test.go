package kernel

import (
	"math"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const decSecondToolAttempt = "ta2"

func commitDecision(t *testing.T, s *State, c *decisionCase) (*State, []Event) {
	t.Helper()
	bindDecision(c, s, "batch-"+strconv.FormatUint(s.LastSequence+1, 10))
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	return s, events
}

func TestDecideSequentialToolFrontier(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 4)
	c := cases[4]
	second := *c.facts.ToolPlans[0]
	second.ToolCallID = "t2"
	second.FirstAttemptID = decSecondToolAttempt
	second.SourcePosition = 1
	c.facts.ToolPlans = append(c.facts.ToolPlans, &second)
	s, _ = commitDecision(t, s, &c)
	early := cases[5]
	cmd := *fixtureValue[*PrepareDispatchCommand](early.command)
	cmd.InvocationID, cmd.AttemptID = "t2", decSecondToolAttempt
	early.command = &cmd
	bindDecision(&early, s, "early")
	assertDecisionRejected(t, s, &early)
	for _, stage := range []int{5, 6, 7} {
		s, _ = commitDecision(t, s, &cases[stage])
	}
	c = cases[8]
	var batch []Event
	s, batch = commitDecision(t, s, &c)
	if len(batch) != 1 || s.CurrentStepIndex != 1 {
		t.Fatal("first result prematurely advanced step")
	}
	for _, stage := range []int{5, 6, 7, 8} {
		c = cases[stage]
		c.command = secondToolCommand(c.command)
		s, batch = commitDecision(t, s, &c)
	}
	if len(batch) != 4 || s.CurrentStepIndex != 2 {
		t.Fatal("final result did not advance")
	}
	completed := fixtureValue[*StepCompletedEvent](batch[1])
	if completed.ResponseRef != "response-ref" || completed.Continuation != StepContinuationContinue {
		t.Fatal("wrong completed step")
	}
	if diff := cmp.Diff([]string{"result-ref", "second-result-ref"}, completed.OrderedResultRefs); diff != "" {
		t.Fatal(diff)
	}
}

func secondToolCommand(command Command) Command {
	switch c := command.(type) {
	case *PrepareDispatchCommand:
		copyCommand := *c
		copyCommand.InvocationID, copyCommand.AttemptID = "t2", decSecondToolAttempt
		return &copyCommand
	case *ObserveToolCheckCommand:
		copyCommand := *c
		copyCommand.ToolCallID, copyCommand.AttemptID = "t2", decSecondToolAttempt
		return &copyCommand
	case *ObserveToolResultCommand:
		copyCommand := *c
		copyCommand.ToolCallID, copyCommand.AttemptID = "t2", decSecondToolAttempt
		copyCommand.SourcePosition = 1
		copyCommand.ResultRef = "second-result-ref"
		return &copyCommand
	}
	panic("unexpected test command")
}

func TestDecideRejectsIdentityAndCursorCorruption(t *testing.T) {
	t.Parallel()
	guards := []guardCase{
		{"tool-model-id-collision", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].ToolCallID = "m1" }},
		{"model-tool-id-collision", 8, func(_ *State, c *decisionCase) { c.facts.ModelPlan.InvocationID = "t1" }},
		{"step-overflow", 8, func(s *State, _ *decisionCase) { s.CurrentStepIndex = math.MaxInt }},
		{"step-negative", 8, func(s *State, _ *decisionCase) { s.CurrentStepIndex = -1 }},
		{"position-negative", 8, func(s *State, c *decisionCase) {
			s.ToolInvocations["t1"].SourcePosition = -1
			fixtureValue[*ObserveToolResultCommand](c.command).SourcePosition = -1
		}},
		{"position-overflow", 8, func(s *State, c *decisionCase) {
			s.ToolInvocations["t1"].SourcePosition = math.MaxInt
			fixtureValue[*ObserveToolResultCommand](c.command).SourcePosition = math.MaxInt
		}},
		{"check-generation-zero", 5, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"].CheckGeneration = 0 }},
	}
	for _, tc := range guards {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, tc.stage)
			c := cases[tc.stage]
			bindDecision(&c, s, "identity-guard")
			tc.mutate(s, &c)
			assertDecisionRejected(t, s, &c)
		})
	}
}

func TestDecideConflictingStream(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 4)
	c := cases[3]
	fixtureValue[*ObserveModelStreamCommand](c.command).FirstChunkRef = decConflict
	bindDecision(&c, s, "stream-conflict")
	assertDecisionRejected(t, s, &c)
}

func TestDecideNilStateAcceptance(t *testing.T) {
	t.Parallel()
	cases := traceCases()
	c := cases[0]
	bindDecision(&c, NewState(), "accept")
	events, err := Decide(nil, c.command, &c.facts)
	if err != nil || len(events) != 1 {
		t.Fatalf("nil initial state: %v", err)
	}
	if !Evolve(nil, events[0]).Accepted {
		t.Fatal("run not accepted")
	}
}

func TestDecideDeterministic(t *testing.T) {
	t.Parallel()
	for stage := range len(traceCases()) {
		t.Run(strconv.Itoa(stage), func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, stage)
			c := cases[stage]
			bindDecision(&c, s, "deterministic")
			first, err := Decide(s, c.command, &c.facts)
			if err != nil {
				t.Fatal(err)
			}
			second, err := Decide(s, c.command, &c.facts)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(first, second); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func TestDecideDuplicateAfterAccountingObservation(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 5)
	c := cases[4]
	later := *c.facts.Usage
	later.TokensUsed++
	later.Envelope.Sequence = s.LastSequence + 1
	s = Evolve(s, &later)
	bindDecision(&c, s, "original-response")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil || len(events) != 0 {
		t.Fatalf("accounting changed response identity: %v", err)
	}
}
