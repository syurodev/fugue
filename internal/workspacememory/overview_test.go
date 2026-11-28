package workspacememory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestOverviewBudgetBorrowingOrderAndTail(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	t.Run("ordering verified first then newest and omitted tail", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv(t, Config{})
		seedOverviewOrderingFixtures(t, ctx, env)

		res, err := env.store.overviewWithBudget(ctx, testWSID, 220)
		if err != nil {
			t.Fatalf("Overview: %v", err)
		}
		if res.UsedBytes > res.BudgetBytes {
			t.Fatalf("UsedBytes %d exceeds BudgetBytes %d", res.UsedBytes, res.BudgetBytes)
		}
		if len(res.IndexLines) == 0 || !res.IndexLines[0].Verified || res.IndexLines[0].Label != "v-newer" {
			t.Fatalf("expected verified newest entry first, got %+v", res.IndexLines)
		}
		wantTail := fmt.Sprintf("%d more entries, use memory_search", res.OmittedCount)
		if res.OmittedCount == 0 || res.Tail != wantTail {
			t.Fatalf("omitted=%d tail=%q, want non-zero omitted and %q", res.OmittedCount, res.Tail, wantTail)
		}
	})

	t.Run("collection borrows unused pinned budget", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv(t, Config{})
		for i := range 20 {
			env.clk.Advance(time.Second)
			if _, err := env.store.Add(ctx, &AddInput{
				WorkspaceID: testWSID,
				Kind:        KindNote,
				Label:       fmt.Sprintf("col-%02d", i),
				Summary:     fmt.Sprintf("Collection summary line number %02d with enough bytes to test borrowing.", i),
				Body:        "This entry contributes to collection index borrowing when no pinned blocks exist.",
				Author:      AuthorUser,
			}); err != nil {
				t.Fatalf("Add col-%02d: %v", i, err)
			}
		}

		res, err := env.store.overviewWithBudget(ctx, testWSID, 1200)
		if err != nil {
			t.Fatalf("Overview: %v", err)
		}
		if res.UsedBytes <= 600 || res.UsedBytes > 1200 {
			t.Fatalf("collection did not borrow unused pinned half: used=%d (budget=1200)", res.UsedBytes)
		}
	})

	t.Run("pinned borrows unused collection budget within 10 KiB", func(t *testing.T) {
		t.Parallel()
		env := newTestEnv(t, Config{})
		body := strings.Repeat("The host enforces rules and checks requests cleanly in the workspace. ", 25) // ~1750 bytes each
		for i := range 5 {
			env.clk.Advance(time.Second)
			if _, err := env.store.Add(ctx, &AddInput{
				WorkspaceID: testWSID,
				Kind:        KindPinned,
				Label:       fmt.Sprintf("pin-%02d", i),
				Summary:     fmt.Sprintf("Pinned block %02d summary.", i),
				Body:        body,
				Author:      AuthorUser,
			}); err != nil {
				t.Fatalf("Add pin-%02d: %v", i, err)
			}
		}

		res, err := env.store.Overview(ctx, testWSID)
		if err != nil {
			t.Fatalf("Overview: %v", err)
		}
		if res.BudgetBytes != OverviewBudgetBytes || res.UsedBytes > OverviewBudgetBytes {
			t.Fatalf("budget mismatch: used=%d budget=%d", res.UsedBytes, res.BudgetBytes)
		}
		if len(res.Blocks) < 4 {
			t.Fatalf("pinned blocks = %d, expected >= 4 by borrowing collection half", len(res.Blocks))
		}
	})
}

func seedOverviewOrderingFixtures(t *testing.T, ctx context.Context, env *testEnv) {
	t.Helper()
	items := []struct {
		label  string
		author Author
	}{
		{"u-older", AuthorAgent},
		{"v-older", AuthorUser},
		{"u-newer", AuthorAgent},
		{"v-newer", AuthorUser},
	}
	for _, it := range items {
		env.clk.Advance(time.Minute)
		if _, err := env.store.Add(ctx, &AddInput{
			WorkspaceID: testWSID,
			Kind:        KindFact,
			Label:       it.label,
			Summary:     "Summary line for ordering test " + it.label + " with extra padding text.",
			Body:        "This body provides valid English prose for overview ordering tests.",
			Author:      it.author,
		}); err != nil {
			t.Fatalf("Add %s: %v", it.label, err)
		}
	}
}
