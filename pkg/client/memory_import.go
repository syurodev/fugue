package client

import (
	"context"
	"fmt"
	"time"

	"connectrpc.com/connect"

	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

// Memory import preview classes and user choices (workspace-memory §9).
const (
	MemoryImportClassAdd           = "add"
	MemoryImportClassSkip          = "skip"
	MemoryImportClassConflict      = "conflict"
	MemoryImportClassLabelConflict = "label_conflict"
	MemoryImportClassRejected      = "rejected"

	MemoryImportChoiceKeepLocal    = "keep_local"
	MemoryImportChoiceTakeImported = "take_imported"
	MemoryImportChoiceKeepBoth     = "keep_both"
	MemoryImportChoiceSkip         = "skip"
	MemoryImportChoiceRename       = "rename"
)

// MemoryImportPreviewItem describes one parsed block compared against target workspace memory.
type MemoryImportPreviewItem struct {
	ItemID        string
	EntryID       string
	Label         string
	Class         string
	DefaultChoice string
	RejectReason  string
	Local         *MemoryEntry
	Imported      *MemoryEntry
}

// MemoryImportPreview is the diff returned by PreviewMemoryImport.
type MemoryImportPreview struct {
	PreviewID     string
	WorkspaceName string
	ExportedAt    time.Time
	Items         []MemoryImportPreviewItem
}

// MemoryImportChoiceInput carries the user's resolution for one preview item.
type MemoryImportChoiceInput struct {
	ItemID   string
	Choice   string
	NewLabel string
}

// ApplyMemoryImportInput carries the user's choices for ApplyMemoryImport.
type ApplyMemoryImportInput struct {
	WorkspaceID string
	PreviewID   string
	Choices     []MemoryImportChoiceInput
	ImportFile  string
}

// MemoryImportResult summarizes the entries applied by ApplyMemoryImport.
type MemoryImportResult struct {
	Written []MemoryEntry
	Added   int32
	Updated int32
	Skipped int32
}

// PreviewMemoryImport parses a fugue-memory/1 Markdown payload and returns a merge diff.
func (c *Client) PreviewMemoryImport(ctx context.Context, workspaceID string, content []byte) (MemoryImportPreview, error) {
	svc := memoryv1connect.NewMemoryServiceClient(
		c.hc, c.base,
		connect.WithSendMaxBytes(memoryExportWireMaxBytes),
		connect.WithReadMaxBytes(memoryExportWireMaxBytes),
	)
	res, err := svc.PreviewImport(ctx, connect.NewRequest(&memoryv1.PreviewImportRequest{
		WorkspaceId: workspaceID,
		Content:     content,
	}))
	if err != nil {
		return MemoryImportPreview{}, fmt.Errorf("preview workspace memory import: %w", err)
	}
	items := make([]MemoryImportPreviewItem, 0, len(res.Msg.GetItems()))
	for _, it := range res.Msg.GetItems() {
		var localEntry, importedEntry *MemoryEntry
		if it.GetLocal() != nil {
			le := memoryEntryFromProto(it.GetLocal())
			localEntry = &le
		}
		if it.GetImported() != nil {
			ie := memoryEntryFromProto(it.GetImported())
			importedEntry = &ie
		}
		items = append(items, MemoryImportPreviewItem{
			ItemID:        it.GetItemId(),
			EntryID:       it.GetEntryId(),
			Label:         it.GetLabel(),
			Class:         memoryImportClassFromProto(it.GetItemClass()),
			DefaultChoice: memoryImportChoiceFromProto(it.GetDefaultChoice()),
			RejectReason:  it.GetRejectReason(),
			Local:         localEntry,
			Imported:      importedEntry,
		})
	}
	var exportedAt time.Time
	if ts := res.Msg.GetExportedAt(); ts != nil {
		exportedAt = ts.AsTime()
	}
	return MemoryImportPreview{
		PreviewID:     res.Msg.GetPreviewId(),
		WorkspaceName: res.Msg.GetWorkspaceName(),
		ExportedAt:    exportedAt,
		Items:         items,
	}, nil
}

// ApplyMemoryImport applies the previewed import choices in one host transaction.
func (c *Client) ApplyMemoryImport(ctx context.Context, in *ApplyMemoryImportInput) (MemoryImportResult, error) {
	if in == nil {
		return MemoryImportResult{}, fmt.Errorf("apply workspace memory import: %w", errMemoryInputRequired)
	}
	choices := make([]*memoryv1.MemoryImportItemChoice, 0, len(in.Choices))
	for _, ch := range in.Choices {
		choices = append(choices, &memoryv1.MemoryImportItemChoice{
			ItemId:   ch.ItemID,
			Choice:   memoryImportChoiceToProto(ch.Choice),
			NewLabel: ch.NewLabel,
		})
	}
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).ApplyImport(ctx,
		connect.NewRequest(&memoryv1.ApplyImportRequest{
			WorkspaceId: in.WorkspaceID,
			PreviewId:   in.PreviewID,
			Choices:     choices,
			ImportFile:  in.ImportFile,
		}))
	if err != nil {
		return MemoryImportResult{}, fmt.Errorf("apply workspace memory import: %w", err)
	}
	written := make([]MemoryEntry, 0, len(res.Msg.GetWritten()))
	for _, e := range res.Msg.GetWritten() {
		written = append(written, memoryEntryFromProto(e))
	}
	return MemoryImportResult{
		Written: written,
		Added:   res.Msg.GetAdded(),
		Updated: res.Msg.GetUpdated(),
		Skipped: res.Msg.GetSkipped(),
	}, nil
}

func writeEntryOpToProto(op string) memoryv1.WriteEntryOp {
	switch op {
	case MemoryOpAdd:
		return memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD
	case MemoryOpUpdate:
		return memoryv1.WriteEntryOp_WRITE_ENTRY_OP_UPDATE
	case MemoryOpInvalidate:
		return memoryv1.WriteEntryOp_WRITE_ENTRY_OP_INVALIDATE
	default:
		return memoryv1.WriteEntryOp_WRITE_ENTRY_OP_UNSPECIFIED
	}
}

func memoryKindToProto(k string) memoryv1.MemoryKind {
	switch k {
	case MemoryKindPinned:
		return memoryv1.MemoryKind_MEMORY_KIND_PINNED
	case MemoryKindFact:
		return memoryv1.MemoryKind_MEMORY_KIND_FACT
	case MemoryKindDecision:
		return memoryv1.MemoryKind_MEMORY_KIND_DECISION
	case MemoryKindConvention:
		return memoryv1.MemoryKind_MEMORY_KIND_CONVENTION
	case MemoryKindHowto:
		return memoryv1.MemoryKind_MEMORY_KIND_HOWTO
	case MemoryKindNote:
		return memoryv1.MemoryKind_MEMORY_KIND_NOTE
	default:
		return memoryv1.MemoryKind_MEMORY_KIND_UNSPECIFIED
	}
}

func memoryKindFromProto(k memoryv1.MemoryKind) string {
	switch k {
	case memoryv1.MemoryKind_MEMORY_KIND_PINNED:
		return MemoryKindPinned
	case memoryv1.MemoryKind_MEMORY_KIND_FACT:
		return MemoryKindFact
	case memoryv1.MemoryKind_MEMORY_KIND_DECISION:
		return MemoryKindDecision
	case memoryv1.MemoryKind_MEMORY_KIND_CONVENTION:
		return MemoryKindConvention
	case memoryv1.MemoryKind_MEMORY_KIND_HOWTO:
		return MemoryKindHowto
	case memoryv1.MemoryKind_MEMORY_KIND_NOTE:
		return MemoryKindNote
	default:
		return ""
	}
}

func memoryAuthorToProto(a string) memoryv1.MemoryAuthor {
	switch a {
	case MemoryAuthorUser:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER
	case MemoryAuthorAgent:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_AGENT
	default:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_UNSPECIFIED
	}
}

func memoryAuthorFromProto(a memoryv1.MemoryAuthor) string {
	switch a {
	case memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER:
		return MemoryAuthorUser
	case memoryv1.MemoryAuthor_MEMORY_AUTHOR_AGENT:
		return MemoryAuthorAgent
	default:
		return ""
	}
}

func memoryValidityToProto(v string) memoryv1.MemoryValidity {
	switch v {
	case MemoryValidityValid:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID
	case MemoryValiditySuperseded:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_SUPERSEDED
	case MemoryValidityInvalid:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_INVALID
	default:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_UNSPECIFIED
	}
}

func memoryValidityFromProto(v memoryv1.MemoryValidity) string {
	switch v {
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID:
		return MemoryValidityValid
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_SUPERSEDED:
		return MemoryValiditySuperseded
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_INVALID:
		return MemoryValidityInvalid
	default:
		return ""
	}
}

func memoryFreshnessFromProto(f memoryv1.MemoryFreshness) string {
	switch f {
	case memoryv1.MemoryFreshness_MEMORY_FRESHNESS_FRESH:
		return MemoryFreshnessFresh
	case memoryv1.MemoryFreshness_MEMORY_FRESHNESS_STALE:
		return MemoryFreshnessStale
	case memoryv1.MemoryFreshness_MEMORY_FRESHNESS_UNKNOWN:
		return MemoryFreshnessUnknown
	default:
		return ""
	}
}

func memoryOperationFromProto(op memoryv1.MemoryOperation) string {
	switch op {
	case memoryv1.MemoryOperation_MEMORY_OPERATION_ADD:
		return MemoryOpAdd
	case memoryv1.MemoryOperation_MEMORY_OPERATION_UPDATE:
		return MemoryOpUpdate
	case memoryv1.MemoryOperation_MEMORY_OPERATION_INVALIDATE:
		return MemoryOpInvalidate
	case memoryv1.MemoryOperation_MEMORY_OPERATION_RESTORE:
		return MemoryOpRestore
	case memoryv1.MemoryOperation_MEMORY_OPERATION_VERIFY:
		return MemoryOpVerify
	default:
		return ""
	}
}

func memoryImportClassFromProto(c memoryv1.MemoryImportClass) string {
	switch c {
	case memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_ADD:
		return MemoryImportClassAdd
	case memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_SKIP:
		return MemoryImportClassSkip
	case memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_CONFLICT:
		return MemoryImportClassConflict
	case memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_LABEL_CONFLICT:
		return MemoryImportClassLabelConflict
	case memoryv1.MemoryImportClass_MEMORY_IMPORT_CLASS_REJECTED:
		return MemoryImportClassRejected
	default:
		return ""
	}
}

func memoryImportChoiceToProto(c string) memoryv1.MemoryImportChoice {
	switch c {
	case MemoryImportChoiceKeepLocal:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_KEEP_LOCAL
	case MemoryImportChoiceTakeImported:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_TAKE_IMPORTED
	case MemoryImportChoiceKeepBoth:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_KEEP_BOTH
	case MemoryImportChoiceSkip:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_SKIP
	case MemoryImportChoiceRename:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_RENAME
	default:
		return memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_UNSPECIFIED
	}
}

func memoryImportChoiceFromProto(c memoryv1.MemoryImportChoice) string {
	switch c {
	case memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_KEEP_LOCAL:
		return MemoryImportChoiceKeepLocal
	case memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_TAKE_IMPORTED:
		return MemoryImportChoiceTakeImported
	case memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_KEEP_BOTH:
		return MemoryImportChoiceKeepBoth
	case memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_SKIP:
		return MemoryImportChoiceSkip
	case memoryv1.MemoryImportChoice_MEMORY_IMPORT_CHOICE_RENAME:
		return MemoryImportChoiceRename
	default:
		return ""
	}
}

func memoryEntryFromProto(e *memoryv1.MemoryEntry) MemoryEntry {
	if e == nil {
		return MemoryEntry{}
	}
	anchors := make([]MemoryAnchor, 0, len(e.GetAnchors()))
	for _, a := range e.GetAnchors() {
		anchors = append(anchors, MemoryAnchor{
			RootName:    a.GetRootName(),
			RelPath:     a.GetRelPath(),
			ContentHash: a.GetContentHash(),
		})
	}
	statuses := make([]MemoryAnchorStatus, 0, len(e.GetAnchorStatuses()))
	for _, s := range e.GetAnchorStatuses() {
		statuses = append(statuses, MemoryAnchorStatus{
			RootName:    s.GetRootName(),
			RelPath:     s.GetRelPath(),
			ContentHash: s.GetContentHash(),
			Freshness:   memoryFreshnessFromProto(s.GetFreshness()),
		})
	}
	var updatedAt time.Time
	if ts := e.GetUpdatedAt(); ts != nil {
		updatedAt = ts.AsTime()
	}
	return MemoryEntry{
		ID:             e.GetId(),
		WorkspaceID:    e.GetWorkspaceId(),
		Kind:           memoryKindFromProto(e.GetKind()),
		Label:          e.GetLabel(),
		Summary:        e.GetSummary(),
		Body:           e.GetBody(),
		Keywords:       append([]string(nil), e.GetKeywords()...),
		Anchors:        anchors,
		AnchorStatuses: statuses,
		Author:         memoryAuthorFromProto(e.GetAuthor()),
		Verified:       e.GetVerified(),
		Validity:       memoryValidityFromProto(e.GetValidity()),
		InvalidReason:  e.GetInvalidReason(),
		SupersededBy:   e.GetSupersededBy(),
		Provenance:     memoryProvenanceFromProto(e.GetProvenance()),
		Revision:       e.GetRevision(),
		UpdatedAt:      updatedAt,
		Freshness:      memoryFreshnessFromProto(e.GetFreshness()),
	}
}

func memoryHistoryEntryFromProto(h *memoryv1.MemoryHistoryEntry) MemoryHistoryEntry {
	if h == nil {
		return MemoryHistoryEntry{}
	}
	anchors := make([]MemoryAnchor, 0, len(h.GetAnchors()))
	for _, a := range h.GetAnchors() {
		anchors = append(anchors, MemoryAnchor{
			RootName:    a.GetRootName(),
			RelPath:     a.GetRelPath(),
			ContentHash: a.GetContentHash(),
		})
	}
	var writtenAt time.Time
	if ts := h.GetWrittenAt(); ts != nil {
		writtenAt = ts.AsTime()
	}
	return MemoryHistoryEntry{
		HistoryID:     h.GetHistoryId(),
		EntryID:       h.GetEntryId(),
		WorkspaceID:   h.GetWorkspaceId(),
		Revision:      h.GetRevision(),
		Op:            memoryOperationFromProto(h.GetOp()),
		Kind:          memoryKindFromProto(h.GetKind()),
		Label:         h.GetLabel(),
		Summary:       h.GetSummary(),
		Body:          h.GetBody(),
		Keywords:      append([]string(nil), h.GetKeywords()...),
		Anchors:       anchors,
		Author:        memoryAuthorFromProto(h.GetAuthor()),
		Verified:      h.GetVerified(),
		Validity:      memoryValidityFromProto(h.GetValidity()),
		InvalidReason: h.GetInvalidReason(),
		SupersededBy:  h.GetSupersededBy(),
		Provenance:    memoryProvenanceFromProto(h.GetProvenance()),
		SessionID:     h.GetSessionId(),
		WrittenAt:     writtenAt,
	}
}

func memorySearchResultFromProto(r *memoryv1.MemorySearchResult) MemorySearchResult {
	if r == nil {
		return MemorySearchResult{}
	}
	return MemorySearchResult{
		ID:          r.GetId(),
		Label:       r.GetLabel(),
		Kind:        memoryKindFromProto(r.GetKind()),
		Summary:     r.GetSummary(),
		Snippet:     r.GetSnippet(),
		Author:      memoryAuthorFromProto(r.GetAuthor()),
		Verified:    r.GetVerified(),
		Freshness:   memoryFreshnessFromProto(r.GetFreshness()),
		Revision:    r.GetRevision(),
		Approximate: r.GetApproximate(),
		Score:       r.GetScore(),
	}
}

func memoryProvenanceFromProto(p *memoryv1.MemoryProvenance) MemoryProvenance {
	if p == nil {
		return MemoryProvenance{}
	}
	return MemoryProvenance{
		Principal:  p.GetPrincipal(),
		SessionID:  p.GetSessionId(),
		Role:       p.GetRole(),
		RunID:      p.GetRunId(),
		ImportFile: p.GetImportFile(),
	}
}
