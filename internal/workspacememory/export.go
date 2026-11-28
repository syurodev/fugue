package workspacememory

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

// ExportMarkdown exports all entries of workspaceID (including invalid and
// superseded entries) into the fugue-memory/1 Markdown format (§8).
// Before rendering, the secret scanner runs over every entry's text; any hit
// refuses the export with memory.contains_secret naming the offending entry IDs,
// labels, and finding kinds without ever repeating secret values.
func (s *Store) ExportMarkdown(ctx context.Context, workspaceID, workspaceName string) ([]byte, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errInvalidArg("workspace_id is required")
	}
	entries, err := s.List(ctx, ListFilter{WorkspaceID: workspaceID})
	if err != nil {
		return nil, err
	}
	slices.SortFunc(entries, func(a, b Entry) int {
		return strings.Compare(a.ID, b.ID)
	})
	if err := s.scanEntriesForExport(entries); err != nil {
		return nil, err
	}
	return EncodeMemoryFile(workspaceName, s.clk.Now().UTC(), entries)
}

func (s *Store) scanEntriesForExport(entries []Entry) error {
	var hits []string
	for i := range entries {
		kinds := s.scanEntryFindingKinds(&entries[i])
		if len(kinds) == 0 {
			continue
		}
		hits = append(hits, fmt.Sprintf("%s (%s: %s)", entries[i].ID, entries[i].Label, strings.Join(kinds, ", ")))
	}
	if len(hits) == 0 {
		return nil
	}
	return errcode.New(
		errcode.MemoryContainsSecret,
		"cannot export workspace memory containing detected secrets in entries: "+strings.Join(hits, "; "),
	)
}

func (s *Store) scanEntryFindingKinds(e *Entry) []string {
	fields := make([]string, 0, 4+len(e.Keywords))
	fields = append(fields, e.Label, e.Summary, e.Body, e.InvalidReason)
	fields = append(fields, e.Keywords...)
	seen := map[string]struct{}{}
	var kinds []string
	for _, field := range fields {
		if field == "" {
			continue
		}
		for _, f := range s.scanner.Scan(field) {
			if _, ok := seen[f.Kind]; ok {
				continue
			}
			seen[f.Kind] = struct{}{}
			kinds = append(kinds, f.Kind)
		}
	}
	slices.Sort(kinds)
	return kinds
}
