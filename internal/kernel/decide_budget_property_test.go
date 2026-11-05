package kernel

import (
	"fmt"
	"testing"
	"time"

	"pgregory.net/rapid"
)

// walkBlocked resolves a budget wait the way a user can: extend it, end the run,
// or try (and fail) to extend as someone who may not.
func walkBlocked(rt *rapid.T, r *runDriver) {
	var block *Block
	for _, b := range r.s.Blocks {
		block = b
	}
	switch rapid.IntRange(0, 3).Draw(rt, "resolution") {
	case 0:
		before := cloneState(r.s)
		if events, err := r.try(extendCommand(block.BudgetKind, actorModel, 5), nil); err == nil || len(events) != 0 {
			rt.Fatalf("a non-user extended a budget: %v", err)
		}
		assertSameState(rt, &before, r.s)
	case 1:
		if hasOpenStep(r.s) {
			before := cloneState(r.s)
			if events, err := r.try(endCommand(block.BlockerID), nil); err == nil || len(events) != 0 {
				rt.Fatalf("a run with an open step was ended: %v", err)
			}
			assertSameState(rt, &before, r.s)
			return
		}
		r.must(endCommand(block.BlockerID), nil)
	default:
		r.must(extendCommand(block.BudgetKind, testUserActor, rapid.Int64Range(1, 30).Draw(rt, "amount")), nil)
	}
}

func assertSameState(rt *rapid.T, before, after *State) {
	if ids := CheckInvariants(after); len(ids) != 0 {
		rt.Fatalf("invariants: %v", ids)
	}
	if before.LastSequence != after.LastSequence {
		rt.Fatalf("a rejected command changed the state")
	}
}

func walkModelStep(rt *rapid.T, r *runDriver, step int) {
	finish := rapid.SampledFrom([]FinishReason{
		FinishReasonToolUse, FinishReasonToolUse, FinishReasonStop, FinishReasonLength, FinishReasonRefusal,
	}).Draw(rt, "finish")
	var tools []*ToolCallPlannedEvent
	var refs []string
	if finish == FinishReasonToolUse || (finish == FinishReasonLength && rapid.Bool().Draw(rt, "cut calls")) {
		for i := range rapid.IntRange(1, 2).Draw(rt, "tools") {
			tools = append(tools, driverToolPlan(step, i))
			refs = append(refs, fmt.Sprintf("cut-%d-%d", step, i))
		}
	}
	if finish == FinishReasonLength && tools == nil {
		rt.Log("length without tool calls")
	}
	steer := rapid.Bool().Draw(rt, "steer")
	r.model(step, finish, tools, func(f *DecisionFacts) {
		f.SteerPending = steer
		f.SyntheticResultRefs = refs
	})
}

func walkRun(rt *rapid.T, r *runDriver) {
	s := r.s
	step := s.CurrentStepIndex
	inv := s.ModelInvocations[s.CurrentModelInvocationID]
	switch {
	case len(s.Blocks) != 0:
		walkBlocked(rt, r)
	case s.StepCompleted:
		if _, err := r.advance(step+1, func(f *DecisionFacts) { f.ContinuationPromptRef = drvPrompt }); err != nil {
			rt.Fatalf("advance: %v", err)
		}
	case !inv.Completed:
		walkModelStep(rt, r, step)
	default:
		for pos, id := range s.ToolOrder {
			if !s.ToolInvocations[id].Closed {
				r.toolStep(step, pos)
				return
			}
		}
		rt.Fatal("an open step has neither a pending model call nor an open tool")
	}
}

func TestRapidBudgetSequences(t *testing.T) {
	t.Parallel()
	rapid.Check(t, func(rt *rapid.T) {
		budgets := RunBudgets{
			Steps: rapid.Int64Range(1, 3).Draw(rt, "steps"), Tokens: rapid.Int64Range(10, 40).Draw(rt, "tokens"),
			ToolCalls: rapid.Int64Range(1, 3).Draw(rt, "tool calls"), ActiveTime: time.Hour,
		}
		r := newRunDriver(t, budgets)
		for range rapid.IntRange(5, 40).Draw(rt, "actions") {
			if r.s.Finished {
				break
			}
			walkRun(rt, r)
			checkPropertyReplay(rt, r.s, r.history)
			for _, kind := range []BudgetKind{BudgetKindSteps, BudgetKindToolCalls} {
				if r.s.BudgetUsed(kind) > r.s.BudgetLimit(kind) {
					rt.Fatalf("%s used %d of %d", kind, r.s.BudgetUsed(kind), r.s.BudgetLimit(kind))
				}
			}
		}
	})
}
