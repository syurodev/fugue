package workspacememory

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// OverviewBudgetBytes is the 10 KiB session-start overview budget (C-05).
const OverviewBudgetBytes = 10 * 1024

const (
	overviewBlockOverhead = 24
	overviewLineOverhead  = 20
)

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

func (s *Store) overviewWithBudget(ctx context.Context, workspaceID string, budget int) (OverviewResult, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return OverviewResult{}, errInvalidArg("workspace_id is required")
	}
	if budget <= 0 {
		return OverviewResult{}, errInvalidArg("overview budget must be positive")
	}
	entries, err := s.List(ctx, ListFilter{WorkspaceID: workspaceID, Validity: ValidityValid})
	if err != nil {
		return OverviewResult{}, err
	}
	pinned, collection := partitionOverviewEntries(entries)
	sortOverviewEntries(pinned)
	sortOverviewEntries(collection)

	return packOverview(pinned, collection, budget), nil
}

func partitionOverviewEntries(entries []Entry) (pinned, collection []Entry) {
	for i := range entries {
		if entries[i].Kind == KindPinned {
			pinned = append(pinned, entries[i])
		} else {
			collection = append(collection, entries[i])
		}
	}
	return pinned, collection
}

func sortOverviewEntries(entries []Entry) {
	slices.SortFunc(entries, func(a, b Entry) int {
		if a.Verified != b.Verified {
			if a.Verified {
				return -1
			}
			return 1
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			if a.UpdatedAt.After(b.UpdatedAt) {
				return -1
			}
			return 1
		}
		return strings.Compare(a.ID, b.ID)
	})
}

func packOverview(pinned, collection []Entry, budget int) OverviewResult {
	half := budget / 2
	pCount, pUsed := packPrefix(pinned, half, pinnedEntryBytes)
	cCount, cUsed := packPrefix(collection, half, indexEntryBytes)

	if pCount < len(pinned) && cUsed < half {
		extra, extraUsed := packPrefix(pinned[pCount:], half-cUsed, pinnedEntryBytes)
		pCount += extra
		pUsed += extraUsed
	}
	if cCount < len(collection) && pUsed < half {
		extra, extraUsed := packPrefix(collection[cCount:], half-pUsed, indexEntryBytes)
		cCount += extra
		cUsed += extraUsed
	}

	pCount, cCount, pUsed, cUsed = trimForTail(pinned, collection, pCount, cCount, pUsed, cUsed, budget)
	omitted := (len(pinned) - pCount) + (len(collection) - cCount)
	tail := formatOverviewTail(omitted)

	return OverviewResult{
		Blocks:       buildOverviewBlocks(pinned[:pCount]),
		IndexLines:   buildOverviewLines(collection[:cCount]),
		OmittedCount: omitted,
		Tail:         tail,
		UsedBytes:    pUsed + cUsed + len(tail),
		BudgetBytes:  budget,
	}
}

func packPrefix(entries []Entry, maxBytes int, sizeFn func(*Entry) int) (count, used int) {
	for i := range entries {
		sz := sizeFn(&entries[i])
		if used+sz > maxBytes {
			break
		}
		used += sz
		count++
	}
	return count, used
}

func trimForTail(
	pinned, collection []Entry,
	pCount, cCount, pUsed, cUsed, budget int,
) (outPCount, outCCount, outPUsed, outCUsed int) {
	for {
		omitted := (len(pinned) - pCount) + (len(collection) - cCount)
		tailLen := len(formatOverviewTail(omitted))
		if pUsed+cUsed+tailLen <= budget {
			return pCount, cCount, pUsed, cUsed
		}
		if cCount > 0 {
			cCount--
			cUsed -= indexEntryBytes(&collection[cCount])
			continue
		}
		if pCount > 0 {
			pCount--
			pUsed -= pinnedEntryBytes(&pinned[pCount])
			continue
		}
		return 0, 0, 0, 0
	}
}

func pinnedEntryBytes(e *Entry) int {
	return len(e.Label) + len(e.Summary) + len(e.Body) + overviewBlockOverhead
}

func indexEntryBytes(e *Entry) int {
	return len(e.Label) + len(e.Summary) + overviewLineOverhead
}

func formatOverviewTail(omitted int) string {
	if omitted <= 0 {
		return ""
	}
	return fmt.Sprintf("%d more entries, use memory_search", omitted)
}

func buildOverviewBlocks(entries []Entry) []OverviewBlock {
	out := make([]OverviewBlock, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		out = append(out, OverviewBlock{
			ID:        e.ID,
			Label:     e.Label,
			Summary:   e.Summary,
			Body:      e.Body,
			Author:    e.Author,
			Verified:  e.Verified,
			Freshness: e.Freshness,
			Revision:  e.Revision,
		})
	}
	return out
}

func buildOverviewLines(entries []Entry) []OverviewIndexLine {
	out := make([]OverviewIndexLine, 0, len(entries))
	for i := range entries {
		e := &entries[i]
		out = append(out, OverviewIndexLine{
			ID:        e.ID,
			Label:     e.Label,
			Kind:      e.Kind,
			Summary:   e.Summary,
			Author:    e.Author,
			Verified:  e.Verified,
			Freshness: e.Freshness,
			Revision:  e.Revision,
		})
	}
	return out
}
