package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"

	memoryv1 "github.com/syurodev/fugue/pkg/pb/fugue/memory/v1"
	"github.com/syurodev/fugue/pkg/pb/fugue/memory/v1/memoryv1connect"
)

const memoryExportWireMaxBytes = 32*1024*1024 + 64*1024

// MemoryImportMaxBytes is the maximum Markdown file size accepted for import (32 MiB).
const MemoryImportMaxBytes = 32 * 1024 * 1024

// TopicMemory and EventMemoryEntryWritten mirror the host's ephemeral memory topic.
const (
	TopicMemory             = "memory"
	EventMemoryEntryWritten = "MemoryEntryWritten"
)

// Public workspace memory Host API error codes (workspace-memory §7).
const (
	MemoryCodeRevisionConflict = "memory.revision_conflict"
	MemoryCodeContainsSecret   = "memory.contains_secret"
	MemoryCodeLabelTaken       = "memory.label_taken"
	MemoryCodeTooLarge         = "memory.too_large"
	MemoryCodeLanguage         = "memory.language"
	MemoryCodeRateLimited      = "memory.rate_limited"
	MemoryCodeLimitReached     = "memory.limit_reached"
	MemoryCodeImportInvalid    = "memory.import_invalid"
	MemoryCodeImportStale      = "memory.import_stale"
)

// Workspace memory entry kinds, authors, validity, freshness, operations, and write ops.
const (
	MemoryKindPinned     = "pinned"
	MemoryKindFact       = "fact"
	MemoryKindDecision   = "decision"
	MemoryKindConvention = "convention"
	MemoryKindHowto      = "howto"
	MemoryKindNote       = "note"

	MemoryAuthorUser  = "user"
	MemoryAuthorAgent = "agent"

	MemoryValidityValid      = "valid"
	MemoryValiditySuperseded = "superseded"
	MemoryValidityInvalid    = "invalid"

	MemoryFreshnessFresh   = "fresh"
	MemoryFreshnessStale   = "stale"
	MemoryFreshnessUnknown = "unknown"

	MemoryOpAdd        = "add"
	MemoryOpUpdate     = "update"
	MemoryOpInvalidate = "invalidate"
	MemoryOpRestore    = "restore"
	MemoryOpVerify     = "verify"
)

// ErrMemoryImportFileTooLarge is returned when a picked import file exceeds MemoryImportMaxBytes.
var ErrMemoryImportFileTooLarge = errors.New("memory import file exceeds 32 MiB")

var errMemoryInputRequired = errors.New("memory input is required")

// MemoryAnchorInput identifies a workspace-relative file anchor supplied on write.
type MemoryAnchorInput struct {
	RootName string
	RelPath  string
}

// MemoryAnchor is a persisted file anchor with its SHA-256 content hash at write time.
type MemoryAnchor struct {
	RootName    string
	RelPath     string
	ContentHash string
}

// MemoryAnchorStatus reports a persisted anchor and its evaluated freshness on read.
type MemoryAnchorStatus struct {
	RootName    string
	RelPath     string
	ContentHash string
	Freshness   string
}

// MemoryProvenance records the origin of the latest write.
type MemoryProvenance struct {
	Principal  string
	SessionID  string
	Role       string
	RunID      string
	ImportFile string
}

// MemoryEntry is a workspace memory entry returned by the Host API.
type MemoryEntry struct {
	ID             string
	WorkspaceID    string
	Kind           string
	Label          string
	Summary        string
	Body           string
	Keywords       []string
	Anchors        []MemoryAnchor
	AnchorStatuses []MemoryAnchorStatus
	Author         string
	Verified       bool
	Validity       string
	InvalidReason  string
	SupersededBy   string
	Provenance     MemoryProvenance
	Revision       uint64
	UpdatedAt      time.Time
	Freshness      string
}

// MemoryHistoryEntry is one immutable revision snapshot from entry history.
type MemoryHistoryEntry struct {
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
	Anchors       []MemoryAnchor
	Author        string
	Verified      bool
	Validity      string
	InvalidReason string
	SupersededBy  string
	Provenance    MemoryProvenance
	SessionID     string
	WrittenAt     time.Time
}

// MemorySearchResult is one ranked memory match returned by SearchEntries.
type MemorySearchResult struct {
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

// MemorySearchResponse holds ranked matches and whether fallback approximate matching ran.
type MemorySearchResponse struct {
	Results     []MemorySearchResult
	Approximate bool
}

// ListMemoryInput filters entries returned by ListMemoryEntries.
type ListMemoryInput struct {
	WorkspaceID string
	Kind        string
	Verified    *bool
	Author      string
	Validity    string
}

// SearchMemoryInput configures a SearchEntries query.
type SearchMemoryInput struct {
	WorkspaceID    string
	Query          string
	Kind           string
	IncludeInvalid bool
	Limit          int32
	Verified       *bool
	Author         string
	Validity       string
}

// WriteMemoryInput carries a WriteEntry mutation (add, update, or invalidate).
type WriteMemoryInput struct {
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

// MemoryTopicSnapshot is the `memory` topic snapshot payload.
type MemoryTopicSnapshot struct {
	Unverified map[string]int `json:"unverified"`
}

// MemoryWrittenEvent is the metadata-only `MemoryEntryWritten` event payload.
type MemoryWrittenEvent struct {
	EntryID         string `json:"entry_id"`
	WorkspaceID     string `json:"workspace_id"`
	Revision        uint64 `json:"revision"`
	Author          string `json:"author"`
	Op              string `json:"op"`
	UnverifiedCount int    `json:"unverified_count"`
}

// DecodeMemoryTopicSnapshot decodes the `memory` topic snapshot JSON payload.
func DecodeMemoryTopicSnapshot(payload []byte) (MemoryTopicSnapshot, error) {
	var out MemoryTopicSnapshot
	if err := json.Unmarshal(payload, &out); err != nil {
		return MemoryTopicSnapshot{}, fmt.Errorf("decode memory topic snapshot: %w", err)
	}
	if out.Unverified == nil {
		out.Unverified = map[string]int{}
	}
	return out, nil
}

// DecodeMemoryWrittenEvent decodes a `MemoryEntryWritten` event JSON payload.
func DecodeMemoryWrittenEvent(payload []byte) (MemoryWrittenEvent, error) {
	var out MemoryWrittenEvent
	if err := json.Unmarshal(payload, &out); err != nil {
		return MemoryWrittenEvent{}, fmt.Errorf("decode memory written event: %w", err)
	}
	return out, nil
}

// MemoryCodeOf returns the public code carried in a failed memory RPC error,
// for example memory.revision_conflict.
func MemoryCodeOf(err error) (string, bool) {
	detail, ok := errorDetailOf(err)
	if !ok {
		return "", false
	}
	return detail.GetCode(), true
}

// ListMemoryEntries lists workspace memory entries matching the optional filter.
func (c *Client) ListMemoryEntries(ctx context.Context, in ListMemoryInput) ([]MemoryEntry, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).ListEntries(ctx,
		connect.NewRequest(&memoryv1.ListEntriesRequest{
			WorkspaceId: in.WorkspaceID,
			Kind:        memoryKindToProto(in.Kind),
			Verified:    in.Verified,
			Author:      memoryAuthorToProto(in.Author),
			Validity:    memoryValidityToProto(in.Validity),
		}))
	if err != nil {
		return nil, fmt.Errorf("list workspace memory entries: %w", err)
	}
	entries := make([]MemoryEntry, 0, len(res.Msg.GetEntries()))
	for _, e := range res.Msg.GetEntries() {
		entries = append(entries, memoryEntryFromProto(e))
	}
	return entries, nil
}

// SearchMemoryEntries searches workspace memory entries with FTS5 word + trigram ranking.
func (c *Client) SearchMemoryEntries(ctx context.Context, in SearchMemoryInput) (MemorySearchResponse, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).SearchEntries(ctx,
		connect.NewRequest(&memoryv1.SearchEntriesRequest{
			WorkspaceId:    in.WorkspaceID,
			Query:          in.Query,
			Kind:           memoryKindToProto(in.Kind),
			IncludeInvalid: in.IncludeInvalid,
			Limit:          in.Limit,
			Verified:       in.Verified,
			Author:         memoryAuthorToProto(in.Author),
			Validity:       memoryValidityToProto(in.Validity),
		}))
	if err != nil {
		return MemorySearchResponse{}, fmt.Errorf("search workspace memory entries: %w", err)
	}
	out := make([]MemorySearchResult, 0, len(res.Msg.GetResults()))
	for _, r := range res.Msg.GetResults() {
		out = append(out, memorySearchResultFromProto(r))
	}
	return MemorySearchResponse{
		Results:     out,
		Approximate: res.Msg.GetApproximate(),
	}, nil
}

// GetMemoryEntry reads one workspace memory entry by id or label with anchor statuses.
func (c *Client) GetMemoryEntry(ctx context.Context, workspaceID, id, label string) (MemoryEntry, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).GetEntry(ctx,
		connect.NewRequest(&memoryv1.GetEntryRequest{
			WorkspaceId: workspaceID,
			Id:          id,
			Label:       label,
		}))
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("get workspace memory entry: %w", err)
	}
	return memoryEntryFromProto(res.Msg.GetEntry()), nil
}

// GetMemoryEntryHistory returns immutable revision snapshots for one entry.
func (c *Client) GetMemoryEntryHistory(ctx context.Context, workspaceID, entryID string) ([]MemoryHistoryEntry, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).GetEntryHistory(ctx,
		connect.NewRequest(&memoryv1.GetEntryHistoryRequest{
			WorkspaceId: workspaceID,
			EntryId:     entryID,
		}))
	if err != nil {
		return nil, fmt.Errorf("get workspace memory entry history: %w", err)
	}
	out := make([]MemoryHistoryEntry, 0, len(res.Msg.GetHistory()))
	for _, h := range res.Msg.GetHistory() {
		out = append(out, memoryHistoryEntryFromProto(h))
	}
	return out, nil
}

// WriteMemoryEntry adds, updates, or invalidates a workspace memory entry as user.
func (c *Client) WriteMemoryEntry(ctx context.Context, in *WriteMemoryInput) (MemoryEntry, error) {
	if in == nil {
		return MemoryEntry{}, fmt.Errorf("write workspace memory entry: %w", errMemoryInputRequired)
	}
	anchors := make([]*memoryv1.MemoryAnchorInput, 0, len(in.Anchors))
	for _, a := range in.Anchors {
		anchors = append(anchors, &memoryv1.MemoryAnchorInput{
			RootName: a.RootName,
			RelPath:  a.RelPath,
		})
	}
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).WriteEntry(ctx,
		connect.NewRequest(&memoryv1.WriteEntryRequest{
			WorkspaceId:      in.WorkspaceID,
			Op:               writeEntryOpToProto(in.Op),
			Id:               in.ID,
			ExpectedRevision: in.ExpectedRevision,
			Kind:             memoryKindToProto(in.Kind),
			Label:            in.Label,
			Summary:          in.Summary,
			Body:             in.Body,
			Keywords:         in.Keywords,
			Anchors:          anchors,
			Reason:           in.Reason,
			SupersededBy:     in.SupersededBy,
		}))
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("write workspace memory entry: %w", err)
	}
	return memoryEntryFromProto(res.Msg.GetEntry()), nil
}

// VerifyMemoryEntry marks an entry as verified under CAS.
func (c *Client) VerifyMemoryEntry(ctx context.Context, workspaceID, id string, expectedRevision uint64) (MemoryEntry, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).VerifyEntry(ctx,
		connect.NewRequest(&memoryv1.VerifyEntryRequest{
			WorkspaceId:      workspaceID,
			Id:               id,
			ExpectedRevision: expectedRevision,
		}))
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("verify workspace memory entry: %w", err)
	}
	return memoryEntryFromProto(res.Msg.GetEntry()), nil
}

// RestoreMemoryEntry restores an entry's fields from a prior revision as a user write.
func (c *Client) RestoreMemoryEntry(ctx context.Context, workspaceID, id string, restoreRevision, expectedRevision uint64) (MemoryEntry, error) {
	res, err := memoryv1connect.NewMemoryServiceClient(c.hc, c.base).RestoreEntry(ctx,
		connect.NewRequest(&memoryv1.RestoreEntryRequest{
			WorkspaceId:      workspaceID,
			Id:               id,
			RestoreRevision:  restoreRevision,
			ExpectedRevision: expectedRevision,
		}))
	if err != nil {
		return MemoryEntry{}, fmt.Errorf("restore workspace memory entry: %w", err)
	}
	return memoryEntryFromProto(res.Msg.GetEntry()), nil
}

// ExportMemory exports all workspace memory entries as a fugue-memory/1 Markdown file.
func (c *Client) ExportMemory(ctx context.Context, workspaceID string) ([]byte, error) {
	svc := memoryv1connect.NewMemoryServiceClient(
		c.hc, c.base,
		connect.WithReadMaxBytes(memoryExportWireMaxBytes),
	)
	res, err := svc.ExportMemory(ctx, connect.NewRequest(&memoryv1.ExportMemoryRequest{
		WorkspaceId: workspaceID,
	}))
	if err != nil {
		return nil, fmt.Errorf("export workspace memory: %w", err)
	}
	return res.Msg.GetContent(), nil
}
