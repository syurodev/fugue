package kernel

import (
	"math"
	"time"
)

// RunBudgets are the four per-run limits pinned at RunAccepted (budget §4).
// ActiveTime is pinned here but enforced by F3.4, not by this package yet.
type RunBudgets struct {
	Steps      int64
	Tokens     int64
	ToolCalls  int64
	ActiveTime time.Duration
}

// DefaultRunBudgets returns the design defaults used when a workspace sets no override.
func DefaultRunBudgets() RunBudgets {
	return RunBudgets{Steps: 100, Tokens: 2_000_000, ToolCalls: 500, ActiveTime: 2 * time.Hour}
}

// DefaultExtension is the amount a user extension grants when the user names none.
// The time extension is in seconds, the unit BudgetLimit reports for that kind.
func DefaultExtension(k BudgetKind) int64 {
	switch k {
	case BudgetKindSteps:
		return 50
	case BudgetKindTokens:
		return 500_000
	case BudgetKindTime:
		return int64((30 * time.Minute) / time.Second)
	case BudgetKindToolCalls:
		return 200
	}
	return 0
}

// maxResponseTokens bounds the tokens one model response may report. It is 50
// times the default run budget and far above any real context window plus
// output, so honest usage never reaches it, while a hostile or broken report
// cannot push the run's counter toward overflow in a few observations.
const maxResponseTokens = 100_000_000

// addSaturating adds a non-negative amount without wrapping: a counter fed by
// remote data must stop at the top, where every limit reads as exhausted.
func addSaturating(a, b int64) int64 {
	if a > math.MaxInt64-b {
		return math.MaxInt64
	}
	return a + b
}

// valid reports whether every pinned limit is positive, so a run can never be
// accepted with a budget that is exhausted before it starts.
func (b RunBudgets) valid() bool {
	return b.Steps > 0 && b.Tokens > 0 && b.ToolCalls > 0 && b.ActiveTime > 0
}

// BudgetLimit is the pinned limit of a kind plus every extension of that kind.
// Time is in seconds. An unknown kind has no limit and reports zero.
func (s *State) BudgetLimit(k BudgetKind) int64 {
	var pinned int64
	switch k {
	case BudgetKindSteps:
		pinned = s.Budgets.Steps
	case BudgetKindTokens:
		pinned = s.Budgets.Tokens
	case BudgetKindToolCalls:
		pinned = s.Budgets.ToolCalls
	case BudgetKindTime:
		pinned = int64(s.Budgets.ActiveTime / time.Second)
	default:
		return 0
	}
	return pinned + s.Extensions[k]
}

// BudgetUsed is what the committed events have charged against a kind. Time
// is not charged here: active-time windows belong to F3.4.
func (s *State) BudgetUsed(k BudgetKind) int64 {
	switch k {
	case BudgetKindSteps:
		return s.StepsUsed
	case BudgetKindTokens:
		return s.TokensUsed
	case BudgetKindToolCalls:
		return s.ToolCallsDispatched
	case BudgetKindTime:
		return 0
	}
	return 0
}
