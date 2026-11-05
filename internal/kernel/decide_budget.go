package kernel

import (
	"math"
	"strconv"
)

// actorUser is the only actor that may extend a budget or end a run that waits on one.
const actorUser = "user"

// budgetBlock is the wait dependency a budget limit creates. Its ID is
// deterministic so that a replay and a live run agree on which block to clear.
func (d *decision) budgetBlock(kind BudgetKind) *RunBlockAddedEvent {
	return &RunBlockAddedEvent{
		Envelope: d.envelope(), BlockerID: budgetBlockerID(kind), Kind: WaitReasonBudget, BudgetKind: kind,
		Reference: "", ResumeCondition: "budget_extended",
	}
}

func budgetBlockerID(kind BudgetKind) string { return "budget:" + string(kind) }

// budgetExhausted reports whether the charged amount of a kind has reached its
// limit (the pinned value plus every extension).
func (d *decision) budgetExhausted(kind BudgetKind) bool {
	return d.state.BudgetUsed(kind) >= d.state.BudgetLimit(kind)
}

// openBudgetBlock returns the budget block with the given ID, or nil.
func (d *decision) openBudgetBlock(blockerID string) *Block {
	b := d.state.Blocks[blockerID]
	if b == nil || b.Kind != WaitReasonBudget {
		return nil
	}
	return b
}

// extend lets the user grant more of a kind that the run is waiting on. The run
// continues as the same run: counters are not reset, only the limit grows.
func (d *decision) extend(c *ExtendBudgetCommand) ([]Event, error) {
	if c == nil {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	if err := d.endable(); err != nil {
		return nil, err
	}
	// Peers, parents, the model and goal mode can never grant themselves more budget.
	if c.Actor != actorUser || c.ExtensionAmount <= 0 || !c.Kind.valid() ||
		c.ExtensionAmount > math.MaxInt64-d.state.BudgetLimit(c.Kind) {
		return nil, Reject(RejectionCodeCommandRejected, "invalid budget extension")
	}
	blockerID := budgetBlockerID(c.Kind)
	if d.openBudgetBlock(blockerID) == nil {
		return nil, Reject(RejectionCodeInvalidTransition, "run is not waiting on that budget")
	}
	return []Event{
		&BudgetExtendedEvent{Envelope: d.envelope(), Kind: c.Kind, ExtensionAmount: c.ExtensionAmount, Actor: c.Actor},
		&RunBlockClearedEvent{Envelope: d.envelope(), BlockerID: blockerID, ResolvingEvidence: "budget_extended"},
	}, nil
}

func (d *decision) endable() error {
	if d.state.Finished {
		return Reject(RejectionCodeRunAlreadyFinished, "run finished")
	}
	if d.state.StopIntent != nil {
		return Reject(RejectionCodeStopRequested, "stop pending")
	}
	return nil
}

// requestStop is deliberately narrow (Q-17): it ends a run that waits on a
// budget because the user chose "end". Every other stop belongs to F3.2.
func (d *decision) requestStop(c *RequestStopCommand) ([]Event, error) {
	if c == nil || c.Reason != StopReasonBudget || c.Mode != StopModeTerminal || c.Actor != actorUser {
		return nil, Reject(RejectionCodeUnrecognizedCommand, "unsupported or nil command")
	}
	if err := d.target(c.CommandID, c.RunID, c.Origin, c.Token); err != nil {
		return nil, err
	}
	if err := d.endable(); err != nil {
		return nil, err
	}
	// The target names the budget block being ended, which keeps the kind in the record.
	if d.openBudgetBlock(c.Target) == nil || hasActiveAttempt(d.state) || c.RequestTime.IsZero() {
		return nil, Reject(RejectionCodeInvalidTransition, "run is not waiting on a budget")
	}
	// A tool_calls wait leaves the step open with tools that never ran. Finishing
	// then needs typed dispositions for them (state-machine §9), which belong to F3.2.
	if hasOpenStep(d.state) {
		return nil, Reject(RejectionCodeInvalidTransition, "step still has tool calls to dispose")
	}
	stop := &RunStopRequestedEvent{
		Envelope: d.envelope(), Reason: c.Reason, Mode: c.Mode, Actor: c.Actor, Target: c.Target, RequestTime: c.RequestTime,
	}
	// The block is cleared with the finish so a finished run never projects a wait reason.
	cleared := &RunBlockClearedEvent{Envelope: d.envelope(), BlockerID: c.Target, ResolvingEvidence: "run_ended"}
	finished := &RunFinishedEvent{
		Envelope: d.envelope(), Outcome: RunOutcomeBudgetExceeded, Truncated: false, FailureCode: "",
		PrimaryStopRef:    d.state.RunID + "#" + strconv.FormatUint(stop.Envelope.Sequence, 10),
		SecondaryStopRefs: nil, UnresolvedExposureRefs: nil,
	}
	return []Event{stop, cleared, finished}, nil
}
