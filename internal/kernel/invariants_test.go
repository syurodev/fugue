package kernel

import (
	"slices"
	"strconv"
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

func TestInvariantsTraceA(t *testing.T) {
	t.Parallel()
	s := NewState()
	cases := traceCases()
	for i := range cases {
		c := &cases[i]
		bindDecision(c, s, "invariants-"+strconv.Itoa(i))
		events, err := Decide(s, c.command, &c.facts)
		if err != nil {
			t.Fatal(err)
		}
		if ids := CheckBatch(s, events, &c.facts); len(ids) > 0 {
			t.Fatalf("batch %d: %v", i, ids)
		}
		for _, e := range events {
			s = Evolve(s, e)
		}
	}
}

type invariantCase struct {
	id     InvariantID
	stage  int
	change func(*State)
}

func snapshotViolations() []invariantCase {
	return []invariantCase{
		{SM01, 3, func(s *State) {
			s.ToolInvocations["other"] = &ToolInvocation{Attempts: map[string]*ToolAttempt{"other": {Status: ToolAttemptStatusDispatching}}}
		}},
		{SM02, 7, func(s *State) {
			s.ToolInvocations["t1"].Attempts[decToolAttempt].Status = ToolAttemptStatusAwaitingApproval
		}},
		{SM02, 2, func(s *State) {
			s.Blocks["b1"] = &Block{BlockerID: "b1", Kind: WaitReasonApproval, Reference: "no-such-request", ResumeCondition: "c"}
		}},
		{SM03, 7, func(s *State) {
			q := new(ToolApprovalRequestedEvent)
			q.ToolCallID = "t1"
			q.CheckGeneration = 1
			q.OwnerRequestRef = "request"
			q.RequestRevision = 1
			q.ExactArgsRef = "approval-args"
			s.ToolInvocations["t1"].Approval = *q
		}},
		{SM04, 2, func(s *State) { s.StepStarted = false }},
		{SM05, 3, func(s *State) { s.Finished = true }},
		{SM06, 8, func(s *State) {
			s.ToolInvocations["t1"].Attempts[decToolAttempt].Status = ToolAttemptStatusOutcomeUnknown
			s.StopIntent = &StopIntent{Mode: StopModeTerminal}
		}},
		{SM12, 2, func(s *State) { s.Admitted = false }},
	}
}

func TestEverySnapshotInvariantRejectsAViolation(t *testing.T) {
	t.Parallel()
	for _, tc := range snapshotViolations() {
		t.Run(string(tc.id), func(t *testing.T) {
			t.Parallel()
			s, _ := traceState(t, tc.stage)
			tc.change(s)
			if ids := CheckInvariants(s); !slices.Contains(ids, tc.id) {
				t.Fatalf("missing %s: %v", tc.id, ids)
			}
		})
	}
}

func TestEveryHistoricalInvariantRejectsAViolation(t *testing.T) {
	t.Parallel()
	for _, id := range []InvariantID{SM07, SM08, SM09, SM10, SM11} {
		t.Run(string(id), func(t *testing.T) {
			t.Parallel()
			s, events, facts := historicalViolation(t, id)
			if ids := CheckBatch(s, events, facts); !slices.Contains(ids, id) {
				t.Fatalf("missing %s: %v", id, ids)
			}
		})
	}
}

func historicalViolation(t *testing.T, id InvariantID) (*State, []Event, *DecisionFacts) {
	t.Helper()
	s, cases := traceState(t, 2)
	c := cases[2]
	bindDecision(&c, s, "violation")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	switch id {
	case SM07:
		e := new(StepStartedEvent)
		e.Envelope = c.facts.Envelope
		e.StepIndex = 2
		events = []Event{e}
	case SM08:
		e := new(ModelAttemptPlannedEvent)
		e.Envelope = c.facts.Envelope
		e.InvocationID = "m1"
		e.AttemptID = "a1"
		e.PreviousAttemptID = "a1"
		events = []Event{e}
	case SM09:
		c.facts.ExecutorFence++
	case SM10:
		s.StopIntent = &StopIntent{Mode: StopModeTerminal}
	case SM11:
		s, cases = traceState(t, 8)
		c = cases[8]
		bindDecision(&c, s, "unknown")
		s.ToolInvocations["t1"].Attempts[decToolAttempt].ExposureUnknown = true
		e := new(ToolReconciledEvent)
		e.Envelope = c.facts.Envelope
		e.ToolCallID = "t1"
		e.AttemptID = decToolAttempt
		e.ObservedOutcome = ToolOutcomeUnknownObservedDisconnected
		events = []Event{e}
	case SM01, SM02, SM03, SM04, SM05, SM06, SM12:
		t.Fatal("snapshot invariant requested")
	}
	return s, events, &c.facts
}

func TestRapidCommandSequences(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		state := NewState()
		cases := traceCases()
		next := 0
		var history []Event
		count := rapid.IntRange(12, 40).Draw(rt, "commands")
		for i := range count {
			index, c := drawPropertyCommand(rt, state, cases, next, i)
			events := checkPropertyDecision(rt, state, c)
			if len(events) == 0 {
				continue
			}
			for _, e := range events {
				state = Evolve(state, e)
				history = append(history, e)
			}
			if index == next {
				next++
			}
			checkPropertyReplay(rt, state, history)
		}
	})
}

func replayInvariantHistory(events []Event) *State {
	s := NewState()
	for _, e := range events {
		s = Evolve(s, e)
	}
	return s
}

func TestInvariantDiagnosticsAndInputs(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 4)
	c := cases[4]
	bindDecision(&c, s, "pure")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	before := cloneState(s)
	if ids := CheckBatch(s, events, &c.facts); len(ids) > 0 {
		t.Fatal(ids)
	}
	if diff := cmp.Diff(&before, s); diff != "" {
		t.Fatal(diff)
	}
	if ids := CheckInvariants(nil); len(ids) > 0 {
		t.Fatal(ids)
	}
	if ids := CheckBatch(nil, []Event{nil}, nil); !slices.Contains(ids, SM09) {
		t.Fatal(ids)
	}
}

func checkPropertyDecision(rt *rapid.T, state *State, c *decisionCase) []Event {
	before := cloneState(state)
	events, err := Decide(state, c.command, &c.facts)
	if diff := cmp.Diff(&before, state); diff != "" {
		rt.Fatalf("mutated input: %s", diff)
	}
	if err != nil {
		return nil
	}
	if ids := CheckBatch(state, events, &c.facts); len(ids) > 0 {
		rt.Fatalf("batch: %v", ids)
	}
	return events
}

func checkPropertyReplay(rt *rapid.T, state *State, history []Event) {
	first := replayInvariantHistory(history)
	if diff := cmp.Diff(state, first); diff != "" {
		rt.Fatalf("replay: %s", diff)
	}
	if diff := cmp.Diff(first, replayInvariantHistory(history)); diff != "" {
		rt.Fatalf("nondeterministic replay: %s", diff)
	}
}

func drawPropertyCommand(rt *rapid.T, state *State, cases []decisionCase, next, i int) (int, *decisionCase) {
	index := rapid.IntRange(0, len(cases)-1).Draw(rt, "candidate")
	if next < len(cases) && rapid.Bool().Draw(rt, "advance") {
		index = next
	}
	c := cases[index]
	bindDecision(&c, state, "property-"+strconv.Itoa(i))
	mutatePropertyFacts(rt, &c)
	return index, &c
}

func mutatePropertyFacts(rt *rapid.T, c *decisionCase) {
	switch rapid.IntRange(0, 7).Draw(rt, "observation") {
	case 0:
		c.facts.Envelope.Token++
	case 1:
		c.facts.OwnerIncarnation++
	case 2:
		c.facts.ExecutorFence++
	case 3:
		if c.facts.Usage != nil {
			u := *c.facts.Usage
			u.TokensUsed++
			c.facts.Usage = &u
		}
	case 4:
		if cmd, ok := c.command.(*ObserveModelResponseCommand); ok {
			copyCommand := *cmd
			copyCommand.ResponseRef += "-conflict"
			c.command = &copyCommand
		}
	}
}
