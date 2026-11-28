package app

import (
	"context"
	"math"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspacememory"
)

const (
	// MemoryExportImportMaxBytes caps Markdown export and import payloads at 32 MiB.
	MemoryExportImportMaxBytes = 32 * 1024 * 1024
	// MemoryExportImportWireMaxBytes allows Connect protobuf envelope overhead around 32 MiB.
	MemoryExportImportWireMaxBytes = MemoryExportImportMaxBytes + 64*1024
	// MemoryImportPreviewTTL bounds how long an import preview remains valid.
	MemoryImportPreviewTTL = 30 * time.Minute
	// MemoryImportPreviewCap bounds the number of cached previews in host memory.
	MemoryImportPreviewCap = 8
)

// Import classification constants mirrored for transport boundaries.
const (
	MemoryImportClassAdd           = string(workspacememory.ImportClassAdd)
	MemoryImportClassSkip          = string(workspacememory.ImportClassSkip)
	MemoryImportClassConflict      = string(workspacememory.ImportClassConflict)
	MemoryImportClassLabelConflict = string(workspacememory.ImportClassLabelConflict)
	MemoryImportClassRejected      = string(workspacememory.ImportClassRejected)
)

// Import choice constants mirrored for transport boundaries.
const (
	MemoryImportChoiceKeepLocal    = string(workspacememory.ImportChoiceKeepLocal)
	MemoryImportChoiceTakeImported = string(workspacememory.ImportChoiceTakeImported)
	MemoryImportChoiceKeepBoth     = string(workspacememory.ImportChoiceKeepBoth)
	MemoryImportChoiceSkip         = string(workspacememory.ImportChoiceSkip)
	MemoryImportChoiceRename       = string(workspacememory.ImportChoiceRename)
)

type storedImportPreview struct {
	previewID   string
	workspaceID string
	createdAt   time.Time
	inFlight    bool
	preview     workspacememory.ImportPreview
}

// MemoryImportPreviewItemView exposes one preview item at the app boundary.
type MemoryImportPreviewItemView struct {
	ItemID        string
	EntryID       string
	Label         string
	Class         string
	DefaultChoice string
	RejectReason  string
	Local         *MemoryEntryView
	Imported      *MemoryEntryView
}

// MemoryImportPreviewView exposes a stored import preview at the app boundary.
type MemoryImportPreviewView struct {
	PreviewID     string
	WorkspaceName string
	ExportedAt    time.Time
	Items         []MemoryImportPreviewItemView
}

// MemoryImportItemChoiceCommand carries a caller's resolution for one preview item.
type MemoryImportItemChoiceCommand struct {
	ItemID   string
	Choice   string
	NewLabel string
}

// MemoryApplyImportCommand carries parameters for ApplyImport.
type MemoryApplyImportCommand struct {
	WorkspaceID string
	PreviewID   string
	Choices     []MemoryImportItemChoiceCommand
	ImportFile  string
}

// MemoryApplyImportResultView summarizes applied import writes.
type MemoryApplyImportResultView struct {
	Written []MemoryEntryView
	Added   int32
	Updated int32
	Skipped int32
}

// ExportMemory exports all entries of a workspace as a fugue-memory/1 Markdown
// file for any local principal.
func (s *MemoryService) ExportMemory(ctx context.Context, principal, workspaceID string) ([]byte, error) {
	if err := requireLocalMemoryPrincipal(principal); err != nil {
		return nil, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return nil, errcode.New(errcode.InvalidArgument, "workspace_id is required")
	}
	wsName, err := s.resolveWorkspaceName(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	data, err := s.store.ExportMarkdown(ctx, workspaceID, wsName)
	if err != nil {
		return nil, wrapMemoryStoreError(err)
	}
	if len(data) > MemoryExportImportMaxBytes {
		return nil, errcode.New(errcode.MemoryTooLarge, "exported memory file exceeds 32 MiB limit")
	}
	return data, nil
}

// PreviewImport parses and diffs a Markdown import file for the app client kind.
func (s *MemoryService) PreviewImport(ctx context.Context, principal, workspaceID string, content []byte) (MemoryImportPreviewView, error) {
	if err := requireAppPrincipal(principal); err != nil {
		return MemoryImportPreviewView{}, err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return MemoryImportPreviewView{}, errcode.New(errcode.InvalidArgument, "workspace_id is required")
	}
	if len(content) > MemoryExportImportMaxBytes {
		return MemoryImportPreviewView{}, errcode.New(errcode.MemoryImportInvalid, "memory import file exceeds 32 MiB limit")
	}
	if _, err := s.resolveWorkspaceName(ctx, workspaceID); err != nil {
		return MemoryImportPreviewView{}, err
	}
	prev, err := s.store.PreviewImport(ctx, workspaceID, content)
	if err != nil {
		return MemoryImportPreviewView{}, wrapMemoryStoreError(err)
	}
	previewID, err := s.ids.NewID()
	if err != nil {
		return MemoryImportPreviewView{}, errcode.Wrap(errcode.Internal, "generate memory import preview ID", err)
	}
	s.storeImportPreview(previewID, workspaceID, &prev)
	return memoryImportPreviewToView(previewID, &prev), nil
}

// ApplyImport applies a previously previewed import for the app client kind.
func (s *MemoryService) ApplyImport(ctx context.Context, principal string, cmd *MemoryApplyImportCommand) (MemoryApplyImportResultView, error) {
	if err := requireAppPrincipal(principal); err != nil {
		return MemoryApplyImportResultView{}, err
	}
	if cmd == nil || strings.TrimSpace(cmd.WorkspaceID) == "" || strings.TrimSpace(cmd.PreviewID) == "" {
		return MemoryApplyImportResultView{}, errcode.New(errcode.InvalidArgument, "workspace_id and preview_id are required")
	}
	stored, err := s.acquireImportPreview(cmd.WorkspaceID, cmd.PreviewID)
	if err != nil {
		return MemoryApplyImportResultView{}, err
	}
	consumed := false
	defer func() {
		s.releaseImportPreview(cmd.PreviewID, consumed)
	}()

	if s.beforeApplyImport != nil {
		s.beforeApplyImport()
	}

	choices := make([]workspacememory.ImportItemChoice, 0, len(cmd.Choices))
	for _, c := range cmd.Choices {
		choices = append(choices, workspacememory.ImportItemChoice{
			ItemID:   c.ItemID,
			Choice:   workspacememory.ImportChoice(c.Choice),
			NewLabel: c.NewLabel,
		})
	}
	res, err := s.store.ApplyImport(ctx, &workspacememory.ApplyImportInput{
		WorkspaceID: cmd.WorkspaceID,
		Principal:   principal,
		ImportFile:  cmd.ImportFile,
		Preview:     stored.preview,
		Choices:     choices,
	})
	if err != nil {
		if errcode.CodeOf(err) == errcode.MemoryImportStale {
			consumed = true
		}
		return MemoryApplyImportResultView{}, wrapMemoryStoreError(err)
	}
	consumed = true
	written := make([]MemoryEntryView, 0, len(res.Written))
	for i := range res.Written {
		written = append(written, memoryEntryToView(&res.Written[i]))
	}
	return MemoryApplyImportResultView{
		Written: written,
		Added:   clampInt32(res.Added),
		Updated: clampInt32(res.Updated),
		Skipped: clampInt32(res.Skipped),
	}, nil
}

func clampInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}
	if n < 0 {
		return 0
	}
	return int32(n)
}

func (s *MemoryService) resolveWorkspaceName(ctx context.Context, workspaceID string) (string, error) {
	if s.workspaces == nil {
		return "", errcode.New(errcode.FailedPrecondition, "workspace service is not configured")
	}
	ws, err := s.workspaces.GetWorkspace(ctx, workspaceID)
	if err != nil {
		return "", publicWorkspaceError(err)
	}
	return ws.Name, nil
}

func (s *MemoryService) storeImportPreview(previewID, workspaceID string, prev *workspacememory.ImportPreview) {
	now := s.clk.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredPreviewsLocked(now)
	for len(s.previewOrd) >= MemoryImportPreviewCap {
		if !s.evictOldestIdlePreviewLocked() {
			break
		}
	}
	s.previews[previewID] = storedImportPreview{
		previewID:   previewID,
		workspaceID: workspaceID,
		createdAt:   now,
		preview:     *prev,
	}
	s.previewOrd = append(s.previewOrd, previewID)
}

func (s *MemoryService) evictOldestIdlePreviewLocked() bool {
	for idx, id := range s.previewOrd {
		st, ok := s.previews[id]
		if !ok || st.inFlight {
			continue
		}
		delete(s.previews, id)
		s.previewOrd = append(s.previewOrd[:idx], s.previewOrd[idx+1:]...)
		return true
	}
	return false
}

func (s *MemoryService) acquireImportPreview(workspaceID, previewID string) (storedImportPreview, error) {
	now := s.clk.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pruneExpiredPreviewsLocked(now)
	stored, ok := s.previews[previewID]
	if !ok || stored.workspaceID != workspaceID || stored.inFlight {
		return storedImportPreview{}, errcode.New(errcode.MemoryImportStale, "import preview not found or expired")
	}
	stored.inFlight = true
	s.previews[previewID] = stored
	return stored, nil
}

func (s *MemoryService) releaseImportPreview(previewID string, consume bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if consume {
		delete(s.previews, previewID)
		return
	}
	if stored, ok := s.previews[previewID]; ok {
		stored.inFlight = false
		s.previews[previewID] = stored
	}
}

func (s *MemoryService) pruneExpiredPreviewsLocked(now time.Time) {
	kept := s.previewOrd[:0]
	for _, id := range s.previewOrd {
		st, ok := s.previews[id]
		if !ok {
			continue
		}
		if !st.inFlight && !now.Before(st.createdAt.Add(MemoryImportPreviewTTL)) {
			delete(s.previews, id)
			continue
		}
		kept = append(kept, id)
	}
	s.previewOrd = kept
}

func memoryImportPreviewToView(previewID string, prev *workspacememory.ImportPreview) MemoryImportPreviewView {
	items := make([]MemoryImportPreviewItemView, 0, len(prev.Items))
	for i := range prev.Items {
		it := &prev.Items[i]
		var localView, importedView *MemoryEntryView
		if it.Local != nil {
			v := memoryEntryToView(it.Local)
			localView = &v
		}
		if it.Imported != nil {
			v := memoryEntryToView(it.Imported)
			importedView = &v
		}
		items = append(items, MemoryImportPreviewItemView{
			ItemID:        it.ItemID,
			EntryID:       it.EntryID,
			Label:         it.Label,
			Class:         string(it.Class),
			DefaultChoice: string(it.DefaultChoice),
			RejectReason:  it.RejectReason,
			Local:         localView,
			Imported:      importedView,
		})
	}
	return MemoryImportPreviewView{
		PreviewID:     previewID,
		WorkspaceName: prev.WorkspaceName,
		ExportedAt:    prev.ExportedAt,
		Items:         items,
	}
}
