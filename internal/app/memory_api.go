package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspacememory"
)

// MemoryRequestMaxBytes bounds one Connect request to fugue.memory.v1.
const MemoryRequestMaxBytes = 256 * 1024

// MemoryService exposes workspace memory operations to local Host API callers.
type MemoryService struct {
	store *workspacememory.Store
}

func requireLocalMemoryPrincipal(principal string) error {
	if !IsLocalPrincipal(principal) {
		return errcode.New(errcode.PermissionDenied, "only local principals may access workspace memory")
	}
	return nil
}

func wrapMemoryStoreError(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("workspace memory: %w", err)
}

// ListEntries lists entries matching filter for a local principal.
func (s *MemoryService) ListEntries(ctx context.Context, principal string, filter MemoryListFilter) ([]MemoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return nil, err
	}
	items, err := s.store.List(ctx, workspacememory.ListFilter{
		WorkspaceID: filter.WorkspaceID,
		Kind:        workspacememory.Kind(filter.Kind),
		Verified:    filter.Verified,
		Author:      workspacememory.Author(filter.Author),
		Validity:    workspacememory.Validity(filter.Validity),
	})
	if err != nil {
		return nil, wrapMemoryStoreError(err)
	}
	out := make([]MemoryEntryView, 0, len(items))
	for i := range items {
		out = append(out, memoryEntryToView(&items[i]))
	}
	return out, nil
}

// SearchEntries searches entries for a local principal with optional post-filters.
func (s *MemoryService) SearchEntries(ctx context.Context, principal string, in *MemorySearchParams) (MemorySearchResponseView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return MemorySearchResponseView{}, err
	}
	if in == nil {
		return MemorySearchResponseView{}, errcode.New(errcode.InvalidArgument, "search parameters are required")
	}
	includeInvalid := in.IncludeInvalid || in.Validity == MemoryValidityInvalid || in.Validity == MemoryValiditySuperseded
	resp, err := s.store.Search(ctx, &workspacememory.SearchInput{
		WorkspaceID:    in.WorkspaceID,
		Query:          in.Query,
		Kind:           workspacememory.Kind(in.Kind),
		IncludeInvalid: includeInvalid,
		Limit:          in.Limit,
	})
	if err != nil {
		return MemorySearchResponseView{}, wrapMemoryStoreError(err)
	}
	if in.Verified != nil || in.Author != "" || in.Validity != "" {
		resp, err = filterMemorySearchResponse(ctx, s.store, in, resp)
		if err != nil {
			return MemorySearchResponseView{}, wrapMemoryStoreError(err)
		}
	}
	out := make([]MemorySearchResultView, 0, len(resp.Results))
	for i := range resp.Results {
		out = append(out, memorySearchResultToView(&resp.Results[i]))
	}
	return MemorySearchResponseView{Results: out, Approximate: resp.Approximate}, nil
}

func filterMemorySearchResponse(
	ctx context.Context,
	store *workspacememory.Store,
	in *MemorySearchParams,
	resp workspacememory.SearchResponse,
) (workspacememory.SearchResponse, error) {
	filtered := make([]workspacememory.SearchResult, 0, len(resp.Results))
	for i := range resp.Results {
		r := resp.Results[i]
		if in.Verified != nil && r.Verified != *in.Verified {
			continue
		}
		if in.Author != "" && string(r.Author) != in.Author {
			continue
		}
		if in.Validity != "" {
			entry, err := store.GetByID(ctx, in.WorkspaceID, r.ID)
			if err != nil {
				return workspacememory.SearchResponse{}, wrapMemoryStoreError(err)
			}
			if string(entry.Validity) != in.Validity {
				continue
			}
		}
		filtered = append(filtered, r)
	}
	resp.Results = filtered
	return resp, nil
}

// GetEntry retrieves one entry by ID or label for a local principal.
func (s *MemoryService) GetEntry(ctx context.Context, principal, workspaceID, id, label string) (MemoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return MemoryEntryView{}, err
	}
	var (
		entry workspacememory.Entry
		err   error
	)
	switch {
	case strings.TrimSpace(id) != "":
		entry, err = s.store.GetByID(ctx, workspaceID, id)
	case strings.TrimSpace(label) != "":
		entry, err = s.store.GetByLabel(ctx, workspaceID, label)
	default:
		return MemoryEntryView{}, errcode.New(errcode.InvalidArgument, "either id or label is required")
	}
	if err != nil {
		return MemoryEntryView{}, wrapMemoryStoreError(err)
	}
	return memoryEntryToView(&entry), nil
}

// GetEntryHistory returns immutable revision snapshots for one entry.
func (s *MemoryService) GetEntryHistory(ctx context.Context, principal, workspaceID, entryID string) ([]MemoryHistoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return nil, err
	}
	items, err := s.store.History(ctx, workspaceID, entryID)
	if err != nil {
		return nil, wrapMemoryStoreError(err)
	}
	out := make([]MemoryHistoryEntryView, 0, len(items))
	for i := range items {
		out = append(out, memoryHistoryToView(&items[i]))
	}
	return out, nil
}

// WriteEntry adds, updates, or invalidates an entry as AuthorUser.
func (s *MemoryService) WriteEntry(ctx context.Context, principal string, cmd *MemoryWriteCommand) (MemoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return MemoryEntryView{}, err
	}
	if cmd == nil {
		return MemoryEntryView{}, errcode.New(errcode.InvalidArgument, "write command is required")
	}
	prov := workspacememory.Provenance{Principal: principal}
	var (
		entry workspacememory.Entry
		err   error
	)
	switch workspacememory.Operation(cmd.Op) {
	case workspacememory.OpAdd:
		entry, err = s.store.Add(ctx, &workspacememory.AddInput{
			WorkspaceID: cmd.WorkspaceID, Kind: workspacememory.Kind(cmd.Kind), Label: cmd.Label,
			Summary: cmd.Summary, Body: cmd.Body, Keywords: cmd.Keywords,
			Anchors: anchorInputsFromApp(cmd.Anchors), Author: workspacememory.AuthorUser, Provenance: prov,
		})
	case workspacememory.OpUpdate:
		entry, err = s.store.Update(ctx, &workspacememory.UpdateInput{
			WorkspaceID: cmd.WorkspaceID, ID: cmd.ID, ExpectedRevision: cmd.ExpectedRevision,
			Kind: workspacememory.Kind(cmd.Kind), Label: cmd.Label, Summary: cmd.Summary,
			Body: cmd.Body, Keywords: cmd.Keywords, Anchors: anchorInputsFromApp(cmd.Anchors),
			Author: workspacememory.AuthorUser, Provenance: prov,
		})
	case workspacememory.OpInvalidate:
		entry, err = s.store.Invalidate(ctx, &workspacememory.InvalidateInput{
			WorkspaceID: cmd.WorkspaceID, ID: cmd.ID, ExpectedRevision: cmd.ExpectedRevision,
			Reason: cmd.Reason, SupersededBy: cmd.SupersededBy,
			Author: workspacememory.AuthorUser, Provenance: prov,
		})
	case workspacememory.OpRestore, workspacememory.OpVerify:
		return MemoryEntryView{}, errcode.New(errcode.InvalidArgument, "unsupported write operation")
	default:
		return MemoryEntryView{}, errcode.New(errcode.InvalidArgument, "unsupported write operation")
	}
	if err != nil {
		return MemoryEntryView{}, wrapMemoryStoreError(err)
	}
	return memoryEntryToView(&entry), nil
}

// VerifyEntry marks an entry as verified by a local user under CAS.
func (s *MemoryService) VerifyEntry(ctx context.Context, principal, workspaceID, id string, expectedRevision uint64) (MemoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return MemoryEntryView{}, err
	}
	entry, err := s.store.Verify(ctx, &workspacememory.VerifyInput{
		WorkspaceID: workspaceID, ID: id, ExpectedRevision: expectedRevision,
		Provenance: workspacememory.Provenance{Principal: principal},
	})
	if err != nil {
		return MemoryEntryView{}, wrapMemoryStoreError(err)
	}
	return memoryEntryToView(&entry), nil
}

// RestoreEntry restores an immutable history revision as a verified user write.
func (s *MemoryService) RestoreEntry(ctx context.Context, principal, workspaceID, id string, restoreRevision, expectedRevision uint64) (MemoryEntryView, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return MemoryEntryView{}, err
	}
	entry, err := s.store.Restore(ctx, &workspacememory.RestoreInput{
		WorkspaceID: workspaceID, ID: id, RestoreRevision: restoreRevision, ExpectedRevision: expectedRevision,
		Provenance: workspacememory.Provenance{Principal: principal},
	})
	if err != nil {
		return MemoryEntryView{}, wrapMemoryStoreError(err)
	}
	return memoryEntryToView(&entry), nil
}
