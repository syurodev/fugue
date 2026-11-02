package kernel

import (
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func admissionGuards() []guardCase {
	return []guardCase{
		{"accept-no-input", 0, func(_ *State, c *decisionCase) { fixtureValue[*AcceptRunCommand](c.command).InputRef = "" }},
		{"accept-no-session", 0, func(_ *State, c *decisionCase) { fixtureValue[*AcceptRunCommand](c.command).SessionID = "" }},
		{"accept-no-queue", 0, func(_ *State, c *decisionCase) { fixtureValue[*AcceptRunCommand](c.command).QueueItemRef = "" }},
		{"accept-no-config", 0, func(_ *State, c *decisionCase) { fixtureValue[*AcceptRunCommand](c.command).ProtocolBinding = "" }},
		{"accept-already-exists", 0, func(s *State, _ *decisionCase) { s.Accepted = true }},
		{"admit-not-accepted", 1, func(s *State, _ *decisionCase) { s.Accepted = false }},
		{"admit-already-admitted", 1, func(s *State, _ *decisionCase) { s.Admitted = true }},
		{"admit-step-open", 1, func(s *State, _ *decisionCase) { s.StepStarted = true }},
		{"admit-token-zero", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).Token = 0 }},
		{"admit-owner-zero", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).OwnerIncarnation = 0 }},
		{"admit-no-queue-decision", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).QueueDecisionRef = "" }},
		{"admit-no-context-start", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).StartRunID = "" }},
		{"admit-no-epoch", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).ContextEpoch = 0 }},
		{"admit-no-revision", 1, func(_ *State, c *decisionCase) { fixtureValue[*AdmitRunCommand](c.command).InitialContextRevision = 0 }},
		{"plan-missing", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan = nil }},
		{"plan-no-invocation", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.InvocationID = "" }},
		{"plan-no-attempt", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.FirstAttemptID = "" }},
		{"plan-no-model", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.ModelRef = "" }},
		{"plan-no-context", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.ContextRef = "" }},
		{"plan-wrong-purpose", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.Purpose = "compaction" }},
		{"plan-negative-estimate", 1, func(_ *State, c *decisionCase) { c.facts.ModelPlan.EstimatedBudgetTokens = -1 }},
		{"plan-duplicate-invocation", 1, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"] = nil }},
		{"plan-duplicate-attempt", 8, func(_ *State, c *decisionCase) { c.facts.ModelPlan.FirstAttemptID = "a1" }},
	}
}

func TestDecideAdmissionGuards(t *testing.T) {
	t.Parallel()
	for _, tc := range admissionGuards() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, tc.stage)
			c := cases[tc.stage]
			bindDecision(&c, s, "admission-guard")
			tc.mutate(s, &c)
			assertDecisionRejected(t, s, &c)
		})
	}
}

func TestDecideAdvance(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 8)
	result := cases[8]
	bindDecision(&result, s, "tool-result")
	batch, err := Decide(s, result.command, &result.facts)
	if err != nil {
		t.Fatal(err)
	}
	s = Evolve(Evolve(s, batch[0]), batch[1])
	// A step-budget wait may separate completion and planning in F2.9. Rebuild
	// the committed closed-step boundary, not an in-flight step.
	c := decisionCase{command: &AdvanceRunCommand{CommandID: "", RunID: "", Origin: "", Token: 7, StepIndex: 2}, facts: cases[8].facts}
	c.facts.ModelPlan = modelPlan("m3", "a3")
	bindDecision(&c, s, "advance")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	if s.CurrentStepIndex != 2 || s.StepCompleted || s.CurrentModelInvocationID != "m3" {
		t.Fatal("advance did not open next step")
	}
	bindDecision(&c, s, "advance-duplicate")
	assertDecisionRejected(t, s, &c)
}

func TestDecideMetadataGuards(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"run", "command", decOriginField, "causation", "time", "nil-facts"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, 2)
			c := cases[2]
			bindDecision(&c, s, "metadata")
			switch kind {
			case "run":
				c.facts.Envelope.RunID = ""
			case "command":
				c.facts.Envelope.CommandID = ""
			case decOriginField:
				c.facts.Envelope.Origin = ""
			case "causation":
				c.facts.Envelope.CausationID = ""
			case "time":
				c.facts.Envelope.Timestamp = Envelope{}.Timestamp
			case "nil-facts":
				_, err := Decide(s, c.command, nil)
				if err == nil {
					t.Fatal("nil facts accepted")
				}
				return
			}
			assertDecisionRejected(t, s, &c)
		})
	}
}

func TestDecideSequenceBoundary(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 11)
	c := cases[11]
	s.LastSequence = math.MaxUint64 - 4
	bindDecision(&c, s, "last-batch")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].EventEnvelope().Sequence != math.MaxUint64 {
		t.Fatal("incorrect last sequence")
	}
}

func TestDecideNonStreamingCompletion(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 3)
	c := cases[4]
	bindDecision(&c, s, "non-streaming")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range events {
		s = Evolve(s, e)
	}
	if !s.ModelInvocations["m1"].Completed {
		t.Fatal("non-streaming response not selected")
	}
}

func TestDecideResponseAfterNextStepIsDuplicate(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 9)
	c := cases[4]
	bindDecision(&c, s, "old-response")
	before := cloneState(s)
	events, err := Decide(s, c.command, &c.facts)
	if err != nil || len(events) != 0 {
		t.Fatalf("old duplicate: %v", err)
	}
	if diff := cmp.Diff(&before, s); diff != "" {
		t.Fatal(diff)
	}
}
