package app

import (
	"time"

	"github.com/syurodev/fugue/internal/workspacememory"
)

// Memory view and input constants mirrored for transport boundaries (ADR 0001).
const (
	MemoryKindPinned     = string(workspacememory.KindPinned)
	MemoryKindFact       = string(workspacememory.KindFact)
	MemoryKindDecision   = string(workspacememory.KindDecision)
	MemoryKindConvention = string(workspacememory.KindConvention)
	MemoryKindHowto      = string(workspacememory.KindHowto)
	MemoryKindNote       = string(workspacememory.KindNote)

	MemoryAuthorUser  = string(workspacememory.AuthorUser)
	MemoryAuthorAgent = string(workspacememory.AuthorAgent)

	MemoryValidityValid      = string(workspacememory.ValidityValid)
	MemoryValiditySuperseded = string(workspacememory.ValiditySuperseded)
	MemoryValidityInvalid    = string(workspacememory.ValidityInvalid)

	MemoryFreshnessFresh   = string(workspacememory.FreshnessFresh)
	MemoryFreshnessStale   = string(workspacememory.FreshnessStale)
	MemoryFreshnessUnknown = string(workspacememory.FreshnessUnknown)

	MemoryOpAdd        = string(workspacememory.OpAdd)
	MemoryOpUpdate     = string(workspacememory.OpUpdate)
	MemoryOpInvalidate = string(workspacememory.OpInvalidate)
	MemoryOpRestore    = string(workspacememory.OpRestore)
	MemoryOpVerify     = string(workspacememory.OpVerify)

	MemoryMaxPinnedBodyBytes = workspacememory.MaxPinnedBodyBytes
)

// MemoryAnchorInput identifies a workspace-relative file supplied by a caller.
type MemoryAnchorInput struct {
	RootName string
	RelPath  string
}

// MemoryAnchorView is a persisted file anchor with its SHA-256 content hash.
type MemoryAnchorView struct {
	RootName    string
	RelPath     string
	ContentHash string
}

// MemoryAnchorStatusView reports a persisted anchor and its evaluated freshness.
type MemoryAnchorStatusView struct {
	RootName    string
	RelPath     string
	ContentHash string
	Freshness   string
}

// MemoryProvenanceView records the origin of the latest memory write.
type MemoryProvenanceView struct {
	Principal  string
	SessionID  string
	Role       string
	RunID      string
	ImportFile string
}

// MemoryEntryView is a workspace memory entry exposed at the app boundary.
type MemoryEntryView struct {
	ID             string
	WorkspaceID    string
	Kind           string
	Label          string
	Summary        string
	Body           string
	Keywords       []string
	Anchors        []MemoryAnchorView
	AnchorStatuses []MemoryAnchorStatusView
	Author         string
	Verified       bool
	Validity       string
	InvalidReason  string
	SupersededBy   string
	Provenance     MemoryProvenanceView
	Revision       uint64
	UpdatedAt      time.Time
	Freshness      string
}

// MemoryHistoryEntryView is one immutable revision snapshot of a memory entry.
type MemoryHistoryEntryView struct {
	HistoryID     int64
	EntryID       string
	WorkspaceID   string
	Revision      uint64
	Op            string
	Kind          string
	Label         string
	Summary       string
	Body          string
	Keywords      []string
	Anchors       []MemoryAnchorView
	Author        string
	Verified      bool
	Validity      string
	InvalidReason string
	SupersededBy  string
	Provenance    MemoryProvenanceView
	SessionID     string
	WrittenAt     time.Time
}

// MemorySearchResultView is one ranked search hit exposed at the app boundary.
type MemorySearchResultView struct {
	ID          string
	Label       string
	Kind        string
	Summary     string
	Snippet     string
	Author      string
	Verified    bool
	Freshness   string
	Revision    uint64
	Approximate bool
	Score       float64
}

// MemorySearchResponseView holds search hits and whether approximate fallback ran.
type MemorySearchResponseView struct {
	Results     []MemorySearchResultView
	Approximate bool
}

// MemoryListFilter filters entries returned by MemoryService.ListEntries.
type MemoryListFilter struct {
	WorkspaceID string
	Kind        string
	Verified    *bool
	Author      string
	Validity    string
}

// MemorySearchParams filters and executes SearchEntries for local callers.
type MemorySearchParams struct {
	WorkspaceID    string
	Query          string
	Kind           string
	IncludeInvalid bool
	Limit          int
	Verified       *bool
	Author         string
	Validity       string
}

// MemoryWriteCommand holds parameters for WriteEntry by a local user principal.
type MemoryWriteCommand struct {
	WorkspaceID      string
	Op               string
	ID               string
	ExpectedRevision uint64
	Kind             string
	Label            string
	Summary          string
	Body             string
	Keywords         []string
	Anchors          []MemoryAnchorInput
	Reason           string
	SupersededBy     string
}

func memoryEntryToView(e *workspacememory.Entry) MemoryEntryView {
	anchors := make([]MemoryAnchorView, 0, len(e.Anchors))
	for _, a := range e.Anchors {
		anchors = append(anchors, MemoryAnchorView{
			RootName:    a.RootName,
			RelPath:     a.RelPath,
			ContentHash: a.ContentHash,
		})
	}
	statuses := make([]MemoryAnchorStatusView, 0, len(e.AnchorStatuses))
	for _, st := range e.AnchorStatuses {
		statuses = append(statuses, MemoryAnchorStatusView{
			RootName:    st.RootName,
			RelPath:     st.RelPath,
			ContentHash: st.ContentHash,
			Freshness:   string(st.Freshness),
		})
	}
	return MemoryEntryView{
		ID:             e.ID,
		WorkspaceID:    e.WorkspaceID,
		Kind:           string(e.Kind),
		Label:          e.Label,
		Summary:        e.Summary,
		Body:           e.Body,
		Keywords:       e.Keywords,
		Anchors:        anchors,
		AnchorStatuses: statuses,
		Author:         string(e.Author),
		Verified:       e.Verified,
		Validity:       string(e.Validity),
		InvalidReason:  e.InvalidReason,
		SupersededBy:   e.SupersededBy,
		Provenance:     memoryProvenanceToView(&e.Provenance),
		Revision:       e.Revision,
		UpdatedAt:      e.UpdatedAt,
		Freshness:      string(e.Freshness),
	}
}

func memoryHistoryToView(h *workspacememory.HistoryEntry) MemoryHistoryEntryView {
	anchors := make([]MemoryAnchorView, 0, len(h.Anchors))
	for _, a := range h.Anchors {
		anchors = append(anchors, MemoryAnchorView{
			RootName:    a.RootName,
			RelPath:     a.RelPath,
			ContentHash: a.ContentHash,
		})
	}
	return MemoryHistoryEntryView{
		HistoryID:     h.HistoryID,
		EntryID:       h.EntryID,
		WorkspaceID:   h.WorkspaceID,
		Revision:      h.Revision,
		Op:            string(h.Op),
		Kind:          string(h.Kind),
		Label:         h.Label,
		Summary:       h.Summary,
		Body:          h.Body,
		Keywords:      h.Keywords,
		Anchors:       anchors,
		Author:        string(h.Author),
		Verified:      h.Verified,
		Validity:      string(h.Validity),
		InvalidReason: h.InvalidReason,
		SupersededBy:  h.SupersededBy,
		Provenance:    memoryProvenanceToView(&h.Provenance),
		SessionID:     h.SessionID,
		WrittenAt:     h.WrittenAt,
	}
}

func memoryProvenanceToView(p *workspacememory.Provenance) MemoryProvenanceView {
	return MemoryProvenanceView{
		Principal:  p.Principal,
		SessionID:  p.SessionID,
		Role:       p.Role,
		RunID:      p.RunID,
		ImportFile: p.ImportFile,
	}
}

func memorySearchResultToView(r *workspacememory.SearchResult) MemorySearchResultView {
	return MemorySearchResultView{
		ID:          r.ID,
		Label:       r.Label,
		Kind:        string(r.Kind),
		Summary:     r.Summary,
		Snippet:     r.Snippet,
		Author:      string(r.Author),
		Verified:    r.Verified,
		Freshness:   string(r.Freshness),
		Revision:    r.Revision,
		Approximate: r.Approximate,
		Score:       r.Score,
	}
}

func anchorInputsFromApp(in []MemoryAnchorInput) []workspacememory.AnchorInput {
	if len(in) == 0 {
		return nil
	}
	out := make([]workspacememory.AnchorInput, 0, len(in))
	for _, a := range in {
		out = append(out, workspacememory.AnchorInput{RootName: a.RootName, RelPath: a.RelPath})
	}
	return out
}
