package workspacememory

import (
	"context"
	"slices"
	"strings"
)

// PreviewImport parses a fugue-memory/1 file, runs §3 content checks on each
// entry, and classifies every block against workspaceID without writing (§9).
// It records all target entry revisions and the valid-entry ID set so
// ApplyImport can fail closed with memory.import_stale on any concurrent change.
func (s *Store) PreviewImport(ctx context.Context, workspaceID string, content []byte) (ImportPreview, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return ImportPreview{}, errInvalidArg("workspace_id is required")
	}
	decoded, err := DecodeMemoryFile(content)
	if err != nil {
		return ImportPreview{}, err
	}
	existing, err := s.List(ctx, ListFilter{WorkspaceID: workspaceID})
	if err != nil {
		return ImportPreview{}, err
	}

	state := buildPreviewTargetState(existing)
	items := make([]ImportPreviewItem, 0, len(decoded.Blocks))
	for i := range decoded.Blocks {
		item := s.classifyDecodedBlock(i, workspaceID, &decoded.Blocks[i], state)
		items = append(items, item)
	}

	return ImportPreview{
		WorkspaceName:     decoded.Workspace,
		ExportedAt:        decoded.ExportedAt,
		Items:             items,
		SnapshotRevisions: state.snapshotRevs,
		SnapshotValidIDs:  state.snapshotValidIDs,
	}, nil
}

type previewTargetState struct {
	byID             map[string]*Entry
	validByLabel     map[string]*Entry
	snapshotRevs     map[string]uint64
	snapshotValidIDs []string
	seenFileIDs      map[string]struct{}
	plannedLabels    map[string]struct{}
	plannedValidAdds int
}

func buildPreviewTargetState(existing []Entry) *previewTargetState {
	st := &previewTargetState{
		byID:          make(map[string]*Entry, len(existing)),
		validByLabel:  make(map[string]*Entry),
		snapshotRevs:  make(map[string]uint64, len(existing)),
		seenFileIDs:   make(map[string]struct{}),
		plannedLabels: make(map[string]struct{}),
	}
	for i := range existing {
		e := &existing[i]
		st.byID[e.ID] = e
		st.snapshotRevs[e.ID] = e.Revision
		if e.Validity == ValidityValid {
			st.validByLabel[e.Label] = e
			st.snapshotValidIDs = append(st.snapshotValidIDs, e.ID)
		}
	}
	slices.Sort(st.snapshotValidIDs)
	return st
}

func (s *Store) classifyDecodedBlock(
	idx int,
	workspaceID string,
	blk *DecodedBlock,
	st *previewTargetState,
) ImportPreviewItem {
	itemID := "item-" + formatUint64(uint64(idx+1))
	if blk.RejectReason != "" || blk.Entry == nil {
		return ImportPreviewItem{
			ItemID:        itemID,
			Label:         blk.RawLabel,
			Class:         ImportClassRejected,
			DefaultChoice: ImportChoiceSkip,
			RejectReason:  blk.RejectReason,
		}
	}
	imp := blk.Entry
	impView := importedEntryToEntry(workspaceID, imp)
	if _, dup := st.seenFileIDs[imp.ID]; dup {
		return rejectedPreviewItem(itemID, imp, &impView, "duplicate entry id in import file")
	}
	st.seenFileIDs[imp.ID] = struct{}{}

	if err := s.validateImportedCandidate(workspaceID, imp); err != nil {
		return rejectedPreviewItem(itemID, imp, &impView, err.Error())
	}

	local, exists := st.byID[imp.ID]
	if exists {
		return s.classifyExistingImport(itemID, local, imp, &impView, st)
	}
	return s.classifyNewImport(itemID, imp, &impView, st)
}

func (s *Store) validateImportedCandidate(workspaceID string, imp *ImportedEntry) error {
	cand := writeCandidate{
		workspaceID:   workspaceID,
		entryID:       imp.ID,
		kind:          imp.Kind,
		label:         imp.Label,
		summary:       imp.Summary,
		body:          imp.Body,
		keywords:      slices.Clone(imp.Keywords),
		invalidReason: imp.InvalidReason,
		author:        imp.Author,
		checkContent:  true,
	}
	return s.runPureChecks(&cand)
}

func (s *Store) classifyExistingImport(
	itemID string,
	local *Entry,
	imp *ImportedEntry,
	impView *Entry,
	st *previewTargetState,
) ImportPreviewItem {
	if importedMatchesLocal(local, imp) {
		return ImportPreviewItem{
			ItemID:        itemID,
			EntryID:       imp.ID,
			Label:         imp.Label,
			Class:         ImportClassSkip,
			DefaultChoice: ImportChoiceSkip,
			Local:         cloneEntryPtr(local),
			Imported:      impView,
		}
	}
	if imp.Validity == ValidityValid {
		if owner, taken := st.validByLabel[imp.Label]; taken && owner.ID != imp.ID {
			return ImportPreviewItem{
				ItemID:        itemID,
				EntryID:       imp.ID,
				Label:         imp.Label,
				Class:         ImportClassLabelConflict,
				DefaultChoice: ImportChoiceSkip,
				Local:         cloneEntryPtr(owner),
				Imported:      impView,
			}
		}
	}
	if local.Validity != ValidityValid && imp.Validity == ValidityValid {
		if len(st.snapshotValidIDs)+st.plannedValidAdds >= s.cfg.MaxValidEntriesPerWorkspace {
			return rejectedPreviewItem(itemID, imp, impView, errLimitReached().Error())
		}
		st.plannedValidAdds++
	}
	defChoice := ImportChoiceKeepLocal
	if imp.UpdatedAt.After(local.UpdatedAt) {
		defChoice = ImportChoiceTakeImported
	}
	return ImportPreviewItem{
		ItemID:        itemID,
		EntryID:       imp.ID,
		Label:         imp.Label,
		Class:         ImportClassConflict,
		DefaultChoice: defChoice,
		Local:         cloneEntryPtr(local),
		Imported:      impView,
	}
}

func (s *Store) classifyNewImport(
	itemID string,
	imp *ImportedEntry,
	impView *Entry,
	st *previewTargetState,
) ImportPreviewItem {
	if imp.Validity == ValidityValid {
		if owner, taken := st.validByLabel[imp.Label]; taken {
			return ImportPreviewItem{
				ItemID:        itemID,
				EntryID:       imp.ID,
				Label:         imp.Label,
				Class:         ImportClassLabelConflict,
				DefaultChoice: ImportChoiceSkip,
				Local:         cloneEntryPtr(owner),
				Imported:      impView,
			}
		}
		if _, planned := st.plannedLabels[imp.Label]; planned {
			return ImportPreviewItem{
				ItemID:        itemID,
				EntryID:       imp.ID,
				Label:         imp.Label,
				Class:         ImportClassLabelConflict,
				DefaultChoice: ImportChoiceSkip,
				Imported:      impView,
			}
		}
		if len(st.snapshotValidIDs)+st.plannedValidAdds >= s.cfg.MaxValidEntriesPerWorkspace {
			return rejectedPreviewItem(itemID, imp, impView, errLimitReached().Error())
		}
		st.plannedValidAdds++
		st.plannedLabels[imp.Label] = struct{}{}
	}
	return ImportPreviewItem{
		ItemID:        itemID,
		EntryID:       imp.ID,
		Label:         imp.Label,
		Class:         ImportClassAdd,
		DefaultChoice: ImportChoiceTakeImported,
		Imported:      impView,
	}
}

func rejectedPreviewItem(itemID string, imp *ImportedEntry, impView *Entry, reason string) ImportPreviewItem {
	return ImportPreviewItem{
		ItemID:        itemID,
		EntryID:       imp.ID,
		Label:         imp.Label,
		Class:         ImportClassRejected,
		DefaultChoice: ImportChoiceSkip,
		RejectReason:  reason,
		Imported:      impView,
	}
}

func importedMatchesLocal(local *Entry, imp *ImportedEntry) bool {
	return local.Kind == imp.Kind &&
		local.Label == imp.Label &&
		local.Summary == imp.Summary &&
		local.Body == imp.Body &&
		slices.Equal(local.Keywords, imp.Keywords) &&
		slices.Equal(local.Anchors, imp.Anchors) &&
		local.Author == imp.Author &&
		local.Verified == imp.Verified &&
		local.Validity == imp.Validity &&
		local.InvalidReason == imp.InvalidReason &&
		local.SupersededBy == imp.SupersededBy
}

func importedEntryToEntry(workspaceID string, imp *ImportedEntry) Entry {
	return Entry{
		ID:            imp.ID,
		WorkspaceID:   workspaceID,
		Kind:          imp.Kind,
		Label:         imp.Label,
		Summary:       imp.Summary,
		Body:          imp.Body,
		Keywords:      nonNilKeywords(imp.Keywords),
		Anchors:       nonNilAnchors(slices.Clone(imp.Anchors)),
		AnchorStatuses: []AnchorStatus{},
		Author:        imp.Author,
		Verified:      imp.Verified,
		Validity:      imp.Validity,
		InvalidReason: imp.InvalidReason,
		SupersededBy:  imp.SupersededBy,
		Revision:      imp.Revision,
		UpdatedAt:     imp.UpdatedAt,
		Freshness:     FreshnessUnknown,
	}
}

func cloneEntryPtr(e *Entry) *Entry {
	if e == nil {
		return nil
	}
	cp := *e
	cp.Keywords = slices.Clone(e.Keywords)
	cp.Anchors = slices.Clone(e.Anchors)
	cp.AnchorStatuses = slices.Clone(e.AnchorStatuses)
	return &cp
}
