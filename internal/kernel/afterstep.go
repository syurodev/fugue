package kernel

// StepOutcome holds the recorded facts of a completed step that decide what
// happens next. It carries no payloads: every field is a count, a flag or a
// reason the host already wrote into an event.
type StepOutcome struct {
	FinishReason  FinishReason
	ToolCalls     int  // tool calls in the response
	AllTerminate  bool // every tool call has a descriptor-declared terminate flag
	SteerPending  bool
	StopIntent    bool
	AutoContinued bool // a length answer already continued once in this run
	StepsUsed     int64
	StepsLimit    int64
}

// StepDecision is the after-step verdict (continuation §2).
type StepDecision struct {
	Reason      ContinuationReason
	Continue    bool       // false: finish, or wait when Reason is budget
	BudgetKind  BudgetKind // set only when Reason is budget
	Outcome     RunOutcome // set when the step finishes the run
	FailureCode string     // "refused" when Reason is refused
	Truncated   bool       // true only for truncated_end
}

// AfterStep maps step facts to one continuation reason. Precedence, highest
// first: stop_intent, refused, truncated_args, tool_terminate, tool_calls,
// steer_pending, truncated_continue or truncated_end, natural_end. A decision
// to continue then becomes budget(steps) once the step limit is reached, so a
// run that is ending is never held back by the budget.
//
// truncated_args outranks tool_terminate because a truncated call never runs:
// its terminate flag must not end the run.
func AfterStep(o StepOutcome) StepDecision {
	d := stepRow(o)
	if d.Continue && o.StepsUsed >= o.StepsLimit {
		return StepDecision{
			Reason: ContinuationReasonBudget, Continue: false, BudgetKind: BudgetKindSteps,
			Outcome: "", FailureCode: "", Truncated: false,
		}
	}
	return d
}

func stepRow(o StepOutcome) StepDecision {
	switch {
	case o.StopIntent:
		// The stop path (F3.2) owns the outcome of a stop; this row only records why.
		return StepDecision{Reason: ContinuationReasonStopIntent}
	case o.FinishReason == FinishReasonRefusal:
		return StepDecision{Reason: ContinuationReasonRefused, Outcome: RunOutcomeFailed, FailureCode: "refused"}
	case o.FinishReason == FinishReasonLength && o.ToolCalls > 0:
		return StepDecision{Reason: ContinuationReasonTruncatedArgs, Continue: true}
	case o.ToolCalls > 0 && o.AllTerminate:
		return StepDecision{Reason: ContinuationReasonToolTerminate, Outcome: RunOutcomeCompleted}
	case o.ToolCalls > 0:
		return StepDecision{Reason: ContinuationReasonToolCalls, Continue: true}
	case o.SteerPending:
		return StepDecision{Reason: ContinuationReasonSteerPending, Continue: true}
	case o.FinishReason == FinishReasonLength && !o.AutoContinued:
		return StepDecision{Reason: ContinuationReasonTruncatedContinue, Continue: true}
	case o.FinishReason == FinishReasonLength:
		return StepDecision{Reason: ContinuationReasonTruncatedEnd, Outcome: RunOutcomeCompleted, Truncated: true}
	}
	return StepDecision{Reason: ContinuationReasonNaturalEnd, Outcome: RunOutcomeCompleted}
}
