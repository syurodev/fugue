package workspacememory

import (
	"context"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// OverviewBudgetBytes is the 10 KiB session-start overview budget (C-05).
const OverviewBudgetBytes = 10 * 1024

// OverviewBlock is a full pinned block included in the workspace overview.
type OverviewBlock struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Summary   string    `json:"summary"`
	Body      string    `json:"body"`
	Author    Author    `json:"author"`
	Verified  bool      `json:"verified"`
	Freshness Freshness `json:"freshness"`
	Revision  uint64    `json:"revision"`
}

// OverviewIndexLine is one collection summary entry in the workspace overview.
type OverviewIndexLine struct {
	ID        string    `json:"id"`
	Label     string    `json:"label"`
	Kind      Kind      `json:"kind"`
	Summary   string    `json:"summary"`
	Author    Author    `json:"author"`
	Verified  bool      `json:"verified"`
	Freshness Freshness `json:"freshness"`
	Revision  uint64    `json:"revision"`
}

// OverviewResult is the structured content rendered for memory_overview (§5).
type OverviewResult struct {
	Blocks       []OverviewBlock     `json:"blocks"`
	IndexLines   []OverviewIndexLine `json:"index_lines"`
	OmittedCount int                 `json:"omitted_count"`
	Tail         string              `json:"tail"`
	UsedBytes    int                 `json:"used_bytes"`
	BudgetBytes  int                 `json:"budget_bytes"`
}

// Overview builds the 10 KiB workspace overview with 50/50 split and bidirectional borrowing.
func (s *Store) Overview(ctx context.Context, workspaceID string) (OverviewResult, error) {
	return s.overviewWithBudget(ctx, workspaceID, OverviewBudgetBytes)
}

func (s *Store) overviewWithBudget(_ context.Context, _ string, _ int) (OverviewResult, error) {
	return OverviewResult{}, errcode.New(errcode.Unavailable, "workspace memory overview is pending")
}
