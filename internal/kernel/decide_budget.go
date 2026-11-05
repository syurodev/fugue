package kernel

// budgetBlock is the wait dependency a budget limit creates. Its ID is
// deterministic so that a replay and a live run agree on which block to clear.
func (d *decision) budgetBlock(kind BudgetKind) *RunBlockAddedEvent {
	return &RunBlockAddedEvent{
		Envelope: d.envelope(), BlockerID: "budget:" + string(kind), Kind: WaitReasonBudget, BudgetKind: kind,
		Reference: "", ResumeCondition: "budget_extended",
	}
}
