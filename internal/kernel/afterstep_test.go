package kernel

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"pgregory.net/rapid"
)

func stepOutcome(mutate func(*StepOutcome)) StepOutcome {
	o := StepOutcome{
		FinishReason: FinishReasonStop, ToolCalls: 0, AllTerminate: false, SteerPending: false,
		StopIntent: false, AutoContinued: false, StepsUsed: 1, StepsLimit: 100,
	}
	mutate(&o)
	return o
}

type afterStepCase struct {
	name string
	in   StepOutcome
	want StepDecision
}

func afterStepRows() []afterStepCase {
	return []afterStepCase{
		{
			"natural end", stepOutcome(func(*StepOutcome) {}),
			StepDecision{Reason: ContinuationReasonNaturalEnd, Outcome: RunOutcomeCompleted},
		},
		{
			"length once", stepOutcome(func(o *StepOutcome) { o.FinishReason = FinishReasonLength }),
			StepDecision{Reason: ContinuationReasonTruncatedContinue, Continue: true},
		},
		{
			"length twice", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.AutoContinued = FinishReasonLength, true }),
			StepDecision{Reason: ContinuationReasonTruncatedEnd, Outcome: RunOutcomeCompleted, Truncated: true},
		},
		{
			"refusal", stepOutcome(func(o *StepOutcome) { o.FinishReason = FinishReasonRefusal }),
			StepDecision{Reason: ContinuationReasonRefused, Outcome: RunOutcomeFailed, FailureCode: "refused"},
		},
		{
			"tool calls", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls = FinishReasonToolUse, 2 }),
			StepDecision{Reason: ContinuationReasonToolCalls, Continue: true},
		},
		{
			"truncated args", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls = FinishReasonLength, 1 }),
			StepDecision{Reason: ContinuationReasonTruncatedArgs, Continue: true},
		},
		{"truncated args never terminate", stepOutcome(func(o *StepOutcome) {
			o.FinishReason, o.ToolCalls, o.AllTerminate = FinishReasonLength, 1, true
		}), StepDecision{Reason: ContinuationReasonTruncatedArgs, Continue: true}},
		{
			"steer pending", stepOutcome(func(o *StepOutcome) { o.SteerPending = true }),
			StepDecision{Reason: ContinuationReasonSteerPending, Continue: true},
		},
		{
			"steer beats truncation", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.SteerPending = FinishReasonLength, true }),
			StepDecision{Reason: ContinuationReasonSteerPending, Continue: true},
		},
		{
			"tool terminate", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls, o.AllTerminate = FinishReasonToolUse, 1, true }),
			StepDecision{Reason: ContinuationReasonToolTerminate, Outcome: RunOutcomeCompleted},
		},
		{"terminate beats steer", stepOutcome(func(o *StepOutcome) {
			o.FinishReason, o.ToolCalls, o.AllTerminate, o.SteerPending = FinishReasonToolUse, 1, true, true
		}), StepDecision{Reason: ContinuationReasonToolTerminate, Outcome: RunOutcomeCompleted}},
		{
			"stop intent", stepOutcome(func(o *StepOutcome) { o.StopIntent = true }),
			StepDecision{Reason: ContinuationReasonStopIntent},
		},
		{
			"stop intent beats refusal", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.StopIntent = FinishReasonRefusal, true }),
			StepDecision{Reason: ContinuationReasonStopIntent},
		},
	}
}

func afterStepPrecedenceRows() []afterStepCase {
	return []afterStepCase{
		{"refusal beats terminate", stepOutcome(func(o *StepOutcome) {
			o.FinishReason, o.ToolCalls, o.AllTerminate = FinishReasonRefusal, 1, true
		}), StepDecision{Reason: ContinuationReasonRefused, Outcome: RunOutcomeFailed, FailureCode: "refused"}},
		{
			"steps reached", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls, o.StepsUsed = FinishReasonToolUse, 1, 100 }),
			StepDecision{Reason: ContinuationReasonBudget, BudgetKind: BudgetKindSteps},
		},
		{
			"steps reached with steer", stepOutcome(func(o *StepOutcome) { o.SteerPending, o.StepsUsed = true, 100 }),
			StepDecision{Reason: ContinuationReasonBudget, BudgetKind: BudgetKindSteps},
		},
		{
			"steps reached by auto continue", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.StepsUsed = FinishReasonLength, 100 }),
			StepDecision{Reason: ContinuationReasonBudget, BudgetKind: BudgetKindSteps},
		},
		{
			"one step below the limit continues", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls, o.StepsUsed = FinishReasonToolUse, 1, 99 }),
			StepDecision{Reason: ContinuationReasonToolCalls, Continue: true},
		},
		{
			"natural end at limit still ends", stepOutcome(func(o *StepOutcome) { o.StepsUsed = 100 }),
			StepDecision{Reason: ContinuationReasonNaturalEnd, Outcome: RunOutcomeCompleted},
		},
		{"truncated end at limit still ends", stepOutcome(func(o *StepOutcome) {
			o.FinishReason, o.AutoContinued, o.StepsUsed = FinishReasonLength, true, 100
		}), StepDecision{Reason: ContinuationReasonTruncatedEnd, Outcome: RunOutcomeCompleted, Truncated: true}},
		{"terminate at limit still ends", stepOutcome(func(o *StepOutcome) {
			o.FinishReason, o.ToolCalls, o.AllTerminate, o.StepsUsed = FinishReasonToolUse, 1, true, 100
		}), StepDecision{Reason: ContinuationReasonToolTerminate, Outcome: RunOutcomeCompleted}},
		{
			"stop intent at limit is a stop", stepOutcome(func(o *StepOutcome) { o.StopIntent, o.StepsUsed = true, 100 }),
			StepDecision{Reason: ContinuationReasonStopIntent},
		},
		{
			"over the limit waits", stepOutcome(func(o *StepOutcome) { o.FinishReason, o.ToolCalls, o.StepsUsed = FinishReasonToolUse, 1, 101 }),
			StepDecision{Reason: ContinuationReasonBudget, BudgetKind: BudgetKindSteps},
		},
	}
}

func TestAfterStepDecisionTable(t *testing.T) {
	t.Parallel()
	for _, tc := range append(afterStepRows(), afterStepPrecedenceRows()...) {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if diff := cmp.Diff(tc.want, AfterStep(tc.in)); diff != "" {
				t.Fatal(diff)
			}
		})
	}
}

func checkDecisionShape(rt *rapid.T, o StepOutcome) {
	known := map[ContinuationReason]bool{
		ContinuationReasonToolCalls: true, ContinuationReasonTruncatedArgs: true, ContinuationReasonSteerPending: true,
		ContinuationReasonNaturalEnd: true, ContinuationReasonTruncatedContinue: true, ContinuationReasonTruncatedEnd: true,
		ContinuationReasonToolTerminate: true, ContinuationReasonRefused: true, ContinuationReasonBudget: true,
		ContinuationReasonStopIntent: true,
	}
	d := AfterStep(o)
	if !known[d.Reason] {
		rt.Fatalf("unknown reason %q", d.Reason)
	}
	if diff := cmp.Diff(d, AfterStep(o)); diff != "" {
		rt.Fatalf("not deterministic: %s", diff)
	}
	checkDecisionFlags(rt, &d)
}

func checkDecisionFlags(rt *rapid.T, d *StepDecision) {
	if d.Continue && (d.Outcome != "" || d.FailureCode != "" || d.Truncated) {
		rt.Fatalf("a continuing step finishes the run: %+v", *d)
	}
	waits := d.Reason == ContinuationReasonBudget
	if waits != (d.BudgetKind == BudgetKindSteps) || (waits && d.Continue) {
		rt.Fatalf("budget wait is inconsistent: %+v", *d)
	}
	if !d.Continue && !waits && d.Reason != ContinuationReasonStopIntent && d.Outcome == "" {
		rt.Fatalf("a finishing step has no outcome: %+v", *d)
	}
}

func TestAfterStepIsTotalAndDeterministic(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		checkDecisionShape(rt, StepOutcome{
			FinishReason: rapid.SampledFrom([]FinishReason{
				FinishReasonStop, FinishReasonToolUse, FinishReasonLength, FinishReasonRefusal, "", "unknown",
			}).Draw(rt, "finish"),
			ToolCalls:     rapid.IntRange(0, 3).Draw(rt, "tools"),
			AllTerminate:  rapid.Bool().Draw(rt, "terminate"),
			SteerPending:  rapid.Bool().Draw(rt, "steer"),
			StopIntent:    rapid.Bool().Draw(rt, "stop"),
			AutoContinued: rapid.Bool().Draw(rt, "auto"),
			StepsUsed:     rapid.Int64Range(0, 4).Draw(rt, "used"),
			StepsLimit:    rapid.Int64Range(1, 4).Draw(rt, "limit"),
		})
	})
}
