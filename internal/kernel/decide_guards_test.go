package kernel

import (
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	decResultConflict = "result-conflict"
	decReadyConflict  = "ready-conflict"
	decOriginField    = "origin"
	decWrong          = "wrong"
	decOther          = "other"
	decUnknown        = "unknown"
	decConflict       = "conflicting"
	decContext        = "context-ref"
)

type guardCase struct {
	name   string
	stage  int
	mutate func(*State, *decisionCase)
}

func dispatchGuards() []guardCase {
	return []guardCase{
		{"not-admitted", 2, func(s *State, _ *decisionCase) { s.Admitted = false }},
		{"no-step", 2, func(s *State, _ *decisionCase) { s.StepStarted = false }},
		{"closed-step", 2, func(s *State, _ *decisionCase) { s.StepCompleted = true }},
		{"aborted-step", 2, func(s *State, _ *decisionCase) { s.StepAborted = true }},
		{"missing-worker", 2, func(_ *State, c *decisionCase) { c.facts.WorkerReservationRef = "" }},
		{"missing-authorization", 2, func(_ *State, c *decisionCase) { c.facts.DispatchAuthorizationRef = "" }},
		{"missing-dispatch", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch = nil }},
		{"zero-fence", 2, func(_ *State, c *decisionCase) { c.facts.ExecutorFence = 0 }},
		{"wrong-model", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.InvocationID = decWrong }},
		{"wrong-attempt", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.AttemptID = decWrong }},
		{"wrong-dispatch-token", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.Token++ }},
		{"negative-reservation", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.ReservationTokens = -1 }},
		{"missing-settings", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.ProviderSettingsRef = "" }},
		{"missing-account", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.AccountID = "" }},
		{"missing-catalog", 2, func(_ *State, c *decisionCase) { c.facts.ModelDispatch.CatalogVersion = "" }},
		{"nil-model", 2, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"] = nil }},
		{"nil-attempt", 2, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"].Attempts["a1"] = nil }},
		{"active-model", 2, func(s *State, _ *decisionCase) {
			s.ModelInvocations["m1"].Attempts["a1"].Status = ModelAttemptStatusRequesting
		}},
		{"stream-stale-fence", 3, func(_ *State, c *decisionCase) { c.facts.ExecutorFence++ }},
		{"stream-empty-ref", 3, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveModelStreamCommand](c.command).FirstChunkRef = ""
		}},
		{"stream-planned", 3, func(s *State, _ *decisionCase) {
			s.ModelInvocations["m1"].Attempts["a1"].Status = ModelAttemptStatusPlanned
		}},
		{"stream-unknown-model", 3, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveModelStreamCommand](c.command).InvocationID = decWrong
		}},
		{"response-stale-fence", 4, func(_ *State, c *decisionCase) { c.facts.ExecutorFence++ }},
		{"tool-missing-worker", 7, func(_ *State, c *decisionCase) { c.facts.WorkerReservationRef = "" }},
		{"tool-missing-authorization", 7, func(_ *State, c *decisionCase) { c.facts.DispatchAuthorizationRef = "" }},
		{"tool-zero-fence", 7, func(_ *State, c *decisionCase) { c.facts.ExecutorFence = 0 }},
	}
}

func responseGuards() []guardCase {
	return []guardCase{
		{"response-empty-ref", 4, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveModelResponseCommand](c.command).ResponseRef = ""
		}},
		{"response-empty-usage", 4, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveModelResponseCommand](c.command).UsageStatus = ""
		}},
		{"response-nil-usage", 4, func(_ *State, c *decisionCase) { c.facts.Usage = nil }},
		{"response-wrong-usage", 4, func(_ *State, c *decisionCase) { c.facts.Usage.AttemptID = decOther }},
		{"response-negative-usage", 4, func(_ *State, c *decisionCase) { c.facts.Usage.TokensUsed = -1 }},
		{"response-no-basis", 4, func(_ *State, c *decisionCase) { c.facts.Usage.Basis = "" }},
		{"response-time-usage", 4, func(_ *State, c *decisionCase) { c.facts.Usage.DurationUsed = 1 }},
		{"response-unknown-window", 4, func(_ *State, c *decisionCase) { c.facts.Usage.WindowID = "window" }},
		{"response-nil-tool", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0] = nil }},
		{"response-tool-no-id", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].ToolCallID = "" }},
		{"response-tool-no-attempt", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].FirstAttemptID = "" }},
		{"response-tool-no-input", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].InputRef = "" }},
		{"response-tool-no-descriptor", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].DescriptorVersion = "" }},
		{"response-tool-out-of-order", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].SourcePosition = 1 }},
		{"response-side-effect", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].EffectClass = EffectClassSideEffecting }},
		{"response-tool-reused-attempt", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans[0].FirstAttemptID = "a1" }},
		{"response-tool-duplicate", 4, func(_ *State, c *decisionCase) { c.facts.ToolPlans = append(c.facts.ToolPlans, c.facts.ToolPlans[0]) }},
		{"response-tool-already-known", 4, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"] = nil }},
		{"response-unstarted", 4, func(s *State, _ *decisionCase) {
			s.ModelInvocations["m1"].Attempts["a1"].Status = ModelAttemptStatusPlanned
		}},
		{"response-closed-invocation", 4, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"].Closed = true }},
		{"response-wrong-frontier", 4, func(s *State, _ *decisionCase) { s.CurrentModelInvocationID = decOther }},
	}
}

func toolGuards() []guardCase {
	return []guardCase{
		{"tool-unknown", 5, func(_ *State, c *decisionCase) {
			fixtureValue[*PrepareDispatchCommand](c.command).InvocationID = decOther
		}},
		{"tool-nil", 5, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"] = nil }},
		{"tool-nil-attempt", 5, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"].Attempts[decToolAttempt] = nil }},
		{"tool-model-not-complete", 5, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"].Completed = false }},
		{"tool-closed", 5, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"].Closed = true }},
		{"tool-wrong-class", 5, func(s *State, _ *decisionCase) { s.ToolInvocations["t1"].EffectClass = EffectClassSideEffecting }},
		{"tool-active-model", 5, func(s *State, _ *decisionCase) {
			s.ModelInvocations["m1"].Attempts["a1"].Status = ModelAttemptStatusStreaming
		}},
		{"tool-already-checking", 5, func(s *State, _ *decisionCase) {
			s.ToolInvocations["t1"].Attempts[decToolAttempt].Status = ToolAttemptStatusChecking
		}},
		{"tool-active-while-ready", 7, func(s *State, _ *decisionCase) {
			s.ModelInvocations["m1"].Attempts["a1"].Status = ModelAttemptStatusStreaming
		}},
		{"tool-stale-check", 6, func(_ *State, c *decisionCase) { fixtureValue[*ObserveToolCheckCommand](c.command).CheckGeneration++ }},
		{"tool-no-args", 6, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveToolCheckCommand](c.command).ValidatedArgsRef = ""
		}},
		{"tool-no-gate", 6, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveToolCheckCommand](c.command).GateObservation = ""
		}},
		{"tool-approval-required", 6, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveToolCheckCommand](c.command).ApprovalReceiptRef = "approval"
		}},
		{"tool-not-checking", 6, func(s *State, _ *decisionCase) {
			s.ToolInvocations["t1"].Attempts[decToolAttempt].Status = ToolAttemptStatusPlanned
		}},
		{"tool-result-stale-fence", 8, func(_ *State, c *decisionCase) { c.facts.ExecutorFence++ }},
		{"tool-result-no-evidence", 8, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveToolResultCommand](c.command).EffectEvidence = ""
		}},
		{"tool-result-empty", 8, func(_ *State, c *decisionCase) { fixtureValue[*ObserveToolResultCommand](c.command).ResultRef = "" }},
		{"tool-result-unknown", 8, func(_ *State, c *decisionCase) {
			fixtureValue[*ObserveToolResultCommand](c.command).Outcome = decUnknown
		}},
		{"tool-result-wrong-position", 8, func(_ *State, c *decisionCase) { fixtureValue[*ObserveToolResultCommand](c.command).SourcePosition = 1 }},
		{"tool-result-not-dispatched", 8, func(s *State, _ *decisionCase) {
			s.ToolInvocations["t1"].Attempts[decToolAttempt].Status = ToolAttemptStatusReady
		}},
		{"tool-result-no-response", 8, func(s *State, _ *decisionCase) { s.ModelInvocations["m1"].SelectedAttemptID = decOther }},
		{"tool-result-no-next-plan", 8, func(_ *State, c *decisionCase) { c.facts.ModelPlan = nil }},
	}
}

func TestDecideEvidenceGuards(t *testing.T) {
	t.Parallel()
	guards := append(dispatchGuards(), responseGuards()...)
	guards = append(guards, toolGuards()...)
	for _, tc := range guards {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s, cases := traceState(t, tc.stage)
			c := cases[tc.stage]
			bindDecision(&c, s, "guard")
			tc.mutate(s, &c)
			assertDecisionRejected(t, s, &c)
		})
	}
}

func TestDecideConflictingDuplicates(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"response", "usage", "tools", "nil-usage", "nil-tool", "missing-tool", decResultConflict, decReadyConflict} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			index := 4
			if kind == decResultConflict {
				index = 8
			}
			if kind == decReadyConflict {
				index = 6
			}
			s, cases := traceState(t, index+1)
			c := cases[index]
			bindDecision(&c, s, "conflict")
			switch kind {
			case "response":
				fixtureValue[*ObserveModelResponseCommand](c.command).ResponseRef = decConflict
			case "usage":
				c.facts.Usage.TokensUsed++
			case "tools":
				c.facts.ToolPlans[0].InputRef = decConflict
			case "nil-usage":
				c.facts.Usage = nil
			case "nil-tool":
				c.facts.ToolPlans[0] = nil
			case "missing-tool":
				s.ToolInvocations["t1"] = nil
			case decResultConflict:
				fixtureValue[*ObserveToolResultCommand](c.command).ResultRef = decConflict
			case decReadyConflict:
				fixtureValue[*ObserveToolCheckCommand](c.command).ValidatedArgsRef = decConflict
			}
			assertDecisionRejected(t, s, &c)
		})
	}
}

func TestDecideDoesNotAliasPlans(t *testing.T) {
	t.Parallel()
	s, cases := traceState(t, 1)
	c := cases[1]
	bindDecision(&c, s, "plan")
	events, err := Decide(s, c.command, &c.facts)
	if err != nil {
		t.Fatal(err)
	}
	before := *c.facts.ModelPlan
	before.ToolDeclarationRefs = slices.Clone(c.facts.ModelPlan.ToolDeclarationRefs)
	fixtureValue[*StepStartedEvent](events[1]).ContextRefs[0] = "mutated"
	fixtureValue[*ModelRequestPlannedEvent](events[2]).ToolDeclarationRefs[0] = "mutated"
	if diff := cmp.Diff(&before, c.facts.ModelPlan); diff != "" {
		t.Fatal(diff)
	}
	if c.facts.ContextRefs[0] != decContext {
		t.Fatal("context refs aliased")
	}
}

func fixtureValue[T any](value any) T {
	result, ok := value.(T)
	if !ok {
		panic("invalid test fixture type")
	}
	return result
}
