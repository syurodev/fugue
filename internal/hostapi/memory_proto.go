package hostapi

import (
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/syurodev/fugue/internal/app"
	"github.com/syurodev/fugue/internal/platform/errcode"
	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
)

func memorySearchParamsFromProto(msg *memoryv1.SearchEntriesRequest) (*app.MemorySearchParams, error) {
	kind, err := memoryKindFromProto(msg.GetKind(), true)
	if err != nil {
		return nil, err
	}
	author, err := memoryAuthorFromProto(msg.GetAuthor(), true)
	if err != nil {
		return nil, err
	}
	validity, err := memoryValidityFromProto(msg.GetValidity(), true)
	if err != nil {
		return nil, err
	}
	return &app.MemorySearchParams{
		WorkspaceID:    msg.GetWorkspaceId(),
		Query:          msg.GetQuery(),
		Kind:           kind,
		IncludeInvalid: msg.GetIncludeInvalid(),
		Limit:          int(msg.GetLimit()),
		Verified:       msg.Verified,
		Author:         author,
		Validity:       validity,
	}, nil
}

func memoryWriteCmdFromProto(msg *memoryv1.WriteEntryRequest) (*app.MemoryWriteCommand, error) {
	op, err := writeEntryOpFromProto(msg.GetOp())
	if err != nil {
		return nil, err
	}
	allowEmptyKind := op == app.MemoryOpInvalidate
	kind, err := memoryKindFromProto(msg.GetKind(), allowEmptyKind)
	if err != nil {
		return nil, err
	}
	anchors := make([]app.MemoryAnchorInput, 0, len(msg.GetAnchors()))
	for _, a := range msg.GetAnchors() {
		anchors = append(anchors, app.MemoryAnchorInput{
			RootName: a.GetRootName(),
			RelPath:  a.GetRelPath(),
		})
	}
	return &app.MemoryWriteCommand{
		WorkspaceID:      msg.GetWorkspaceId(),
		Op:               op,
		ID:               msg.GetId(),
		ExpectedRevision: msg.GetExpectedRevision(),
		Kind:             kind,
		Label:            msg.GetLabel(),
		Summary:          msg.GetSummary(),
		Body:             msg.GetBody(),
		Keywords:         msg.GetKeywords(),
		Anchors:          anchors,
		Reason:           msg.GetReason(),
		SupersededBy:     msg.GetSupersededBy(),
	}, nil
}

func writeEntryOpFromProto(op memoryv1.WriteEntryOp) (string, error) {
	switch op {
	case memoryv1.WriteEntryOp_WRITE_ENTRY_OP_ADD:
		return app.MemoryOpAdd, nil
	case memoryv1.WriteEntryOp_WRITE_ENTRY_OP_UPDATE:
		return app.MemoryOpUpdate, nil
	case memoryv1.WriteEntryOp_WRITE_ENTRY_OP_INVALIDATE:
		return app.MemoryOpInvalidate, nil
	case memoryv1.WriteEntryOp_WRITE_ENTRY_OP_UNSPECIFIED:
		return "", errcode.New(errcode.InvalidArgument, "op is required")
	default:
		return "", errcode.New(errcode.InvalidArgument, "op is required")
	}
}

func memoryKindFromProto(k memoryv1.MemoryKind, allowEmpty bool) (string, error) {
	switch k {
	case memoryv1.MemoryKind_MEMORY_KIND_UNSPECIFIED:
		if allowEmpty {
			return "", nil
		}
		return "", errcode.New(errcode.InvalidArgument, "kind is required")
	case memoryv1.MemoryKind_MEMORY_KIND_PINNED:
		return app.MemoryKindPinned, nil
	case memoryv1.MemoryKind_MEMORY_KIND_FACT:
		return app.MemoryKindFact, nil
	case memoryv1.MemoryKind_MEMORY_KIND_DECISION:
		return app.MemoryKindDecision, nil
	case memoryv1.MemoryKind_MEMORY_KIND_CONVENTION:
		return app.MemoryKindConvention, nil
	case memoryv1.MemoryKind_MEMORY_KIND_HOWTO:
		return app.MemoryKindHowto, nil
	case memoryv1.MemoryKind_MEMORY_KIND_NOTE:
		return app.MemoryKindNote, nil
	default:
		return "", errcode.New(errcode.InvalidArgument, "unknown memory kind")
	}
}

func memoryKindToProto(k string) memoryv1.MemoryKind {
	switch k {
	case app.MemoryKindPinned:
		return memoryv1.MemoryKind_MEMORY_KIND_PINNED
	case app.MemoryKindFact:
		return memoryv1.MemoryKind_MEMORY_KIND_FACT
	case app.MemoryKindDecision:
		return memoryv1.MemoryKind_MEMORY_KIND_DECISION
	case app.MemoryKindConvention:
		return memoryv1.MemoryKind_MEMORY_KIND_CONVENTION
	case app.MemoryKindHowto:
		return memoryv1.MemoryKind_MEMORY_KIND_HOWTO
	case app.MemoryKindNote:
		return memoryv1.MemoryKind_MEMORY_KIND_NOTE
	default:
		return memoryv1.MemoryKind_MEMORY_KIND_UNSPECIFIED
	}
}

func memoryAuthorFromProto(a memoryv1.MemoryAuthor, allowEmpty bool) (string, error) {
	switch a {
	case memoryv1.MemoryAuthor_MEMORY_AUTHOR_UNSPECIFIED:
		if allowEmpty {
			return "", nil
		}
		return "", errcode.New(errcode.InvalidArgument, "author is required")
	case memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER:
		return app.MemoryAuthorUser, nil
	case memoryv1.MemoryAuthor_MEMORY_AUTHOR_AGENT:
		return app.MemoryAuthorAgent, nil
	default:
		return "", errcode.New(errcode.InvalidArgument, "unknown memory author")
	}
}

func memoryAuthorToProto(a string) memoryv1.MemoryAuthor {
	switch a {
	case app.MemoryAuthorUser:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_USER
	case app.MemoryAuthorAgent:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_AGENT
	default:
		return memoryv1.MemoryAuthor_MEMORY_AUTHOR_UNSPECIFIED
	}
}

func memoryValidityFromProto(v memoryv1.MemoryValidity, allowEmpty bool) (string, error) {
	switch v {
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_UNSPECIFIED:
		if allowEmpty {
			return "", nil
		}
		return "", errcode.New(errcode.InvalidArgument, "validity is required")
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID:
		return app.MemoryValidityValid, nil
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_SUPERSEDED:
		return app.MemoryValiditySuperseded, nil
	case memoryv1.MemoryValidity_MEMORY_VALIDITY_INVALID:
		return app.MemoryValidityInvalid, nil
	default:
		return "", errcode.New(errcode.InvalidArgument, "unknown memory validity")
	}
}

func memoryValidityToProto(v string) memoryv1.MemoryValidity {
	switch v {
	case app.MemoryValidityValid:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_VALID
	case app.MemoryValiditySuperseded:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_SUPERSEDED
	case app.MemoryValidityInvalid:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_INVALID
	default:
		return memoryv1.MemoryValidity_MEMORY_VALIDITY_UNSPECIFIED
	}
}

func memoryFreshnessToProto(f string) memoryv1.MemoryFreshness {
	switch f {
	case app.MemoryFreshnessFresh:
		return memoryv1.MemoryFreshness_MEMORY_FRESHNESS_FRESH
	case app.MemoryFreshnessStale:
		return memoryv1.MemoryFreshness_MEMORY_FRESHNESS_STALE
	case app.MemoryFreshnessUnknown:
		return memoryv1.MemoryFreshness_MEMORY_FRESHNESS_UNKNOWN
	default:
		return memoryv1.MemoryFreshness_MEMORY_FRESHNESS_UNSPECIFIED
	}
}

func memoryOperationToProto(op string) memoryv1.MemoryOperation {
	switch op {
	case app.MemoryOpAdd:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_ADD
	case app.MemoryOpUpdate:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_UPDATE
	case app.MemoryOpInvalidate:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_INVALIDATE
	case app.MemoryOpRestore:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_RESTORE
	case app.MemoryOpVerify:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_VERIFY
	default:
		return memoryv1.MemoryOperation_MEMORY_OPERATION_UNSPECIFIED
	}
}

func memoryAnchorsToProto(anchors []app.MemoryAnchorView) []*memoryv1.MemoryAnchor {
	out := make([]*memoryv1.MemoryAnchor, 0, len(anchors))
	for _, a := range anchors {
		out = append(out, &memoryv1.MemoryAnchor{
			RootName:    a.RootName,
			RelPath:     a.RelPath,
			ContentHash: a.ContentHash,
		})
	}
	return out
}

func memoryAnchorStatusesToProto(statuses []app.MemoryAnchorStatusView) []*memoryv1.MemoryAnchorStatus {
	out := make([]*memoryv1.MemoryAnchorStatus, 0, len(statuses))
	for _, s := range statuses {
		out = append(out, &memoryv1.MemoryAnchorStatus{
			RootName:    s.RootName,
			RelPath:     s.RelPath,
			ContentHash: s.ContentHash,
			Freshness:   memoryFreshnessToProto(s.Freshness),
		})
	}
	return out
}

func memoryProvenanceToProto(p *app.MemoryProvenanceView) *memoryv1.MemoryProvenance {
	return &memoryv1.MemoryProvenance{
		Principal:  p.Principal,
		SessionId:  p.SessionID,
		Role:       p.Role,
		RunId:      p.RunID,
		ImportFile: p.ImportFile,
	}
}

func memoryEntryToProto(e *app.MemoryEntryView) *memoryv1.MemoryEntry {
	return &memoryv1.MemoryEntry{
		Id:             e.ID,
		WorkspaceId:    e.WorkspaceID,
		Kind:           memoryKindToProto(e.Kind),
		Label:          e.Label,
		Summary:        e.Summary,
		Body:           e.Body,
		Keywords:       e.Keywords,
		Anchors:        memoryAnchorsToProto(e.Anchors),
		AnchorStatuses: memoryAnchorStatusesToProto(e.AnchorStatuses),
		Author:         memoryAuthorToProto(e.Author),
		Verified:       e.Verified,
		Validity:       memoryValidityToProto(e.Validity),
		InvalidReason:  e.InvalidReason,
		SupersededBy:   e.SupersededBy,
		Provenance:     memoryProvenanceToProto(&e.Provenance),
		Revision:       e.Revision,
		UpdatedAt:      timestamppb.New(e.UpdatedAt),
		Freshness:      memoryFreshnessToProto(e.Freshness),
	}
}

func memoryHistoryEntryToProto(h *app.MemoryHistoryEntryView) *memoryv1.MemoryHistoryEntry {
	return &memoryv1.MemoryHistoryEntry{
		HistoryId:     h.HistoryID,
		EntryId:       h.EntryID,
		WorkspaceId:   h.WorkspaceID,
		Revision:      h.Revision,
		Op:            memoryOperationToProto(h.Op),
		Kind:          memoryKindToProto(h.Kind),
		Label:         h.Label,
		Summary:       h.Summary,
		Body:          h.Body,
		Keywords:      h.Keywords,
		Anchors:       memoryAnchorsToProto(h.Anchors),
		Author:        memoryAuthorToProto(h.Author),
		Verified:      h.Verified,
		Validity:      memoryValidityToProto(h.Validity),
		InvalidReason: h.InvalidReason,
		SupersededBy:  h.SupersededBy,
		Provenance:    memoryProvenanceToProto(&h.Provenance),
		SessionId:     h.SessionID,
		WrittenAt:     timestamppb.New(h.WrittenAt),
	}
}

func memorySearchResultToProto(r *app.MemorySearchResultView) *memoryv1.MemorySearchResult {
	return &memoryv1.MemorySearchResult{
		Id:          r.ID,
		Label:       r.Label,
		Kind:        memoryKindToProto(r.Kind),
		Summary:     r.Summary,
		Snippet:     r.Snippet,
		Author:      memoryAuthorToProto(r.Author),
		Verified:    r.Verified,
		Freshness:   memoryFreshnessToProto(r.Freshness),
		Revision:    r.Revision,
		Approximate: r.Approximate,
		Score:       r.Score,
	}
}
