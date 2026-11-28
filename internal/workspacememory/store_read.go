package workspacememory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

type rowScanner interface {
	Scan(dest ...any) error
}

// GetByID reads a workspace memory entry by ID and evaluates anchor freshness.
func (s *Store) GetByID(ctx context.Context, workspaceID, id string) (Entry, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(id) == "" {
		return Entry{}, errInvalidArg("workspace_id and id are required")
	}
	r := s.db.QueryRowContext(
		ctx,
		`SELECT id, workspace_id, kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			revision, updated_at
		FROM workspace_memory_entries
		WHERE workspace_id = ? AND id = ?`,
		workspaceID, id,
	)
	row, err := scanEntryRow(r)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, errNotFound("workspace memory entry not found")
	}
	if err != nil {
		return Entry{}, err
	}
	return s.hydrateEntry(ctx, &row), nil
}

// GetByLabel reads a valid workspace memory entry by label and evaluates anchor freshness.
func (s *Store) GetByLabel(ctx context.Context, workspaceID, label string) (Entry, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(label) == "" {
		return Entry{}, errInvalidArg("workspace_id and label are required")
	}
	r := s.db.QueryRowContext(
		ctx,
		`SELECT id, workspace_id, kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			revision, updated_at
		FROM workspace_memory_entries
		WHERE workspace_id = ? AND label = ? AND validity = 'valid'`,
		workspaceID, label,
	)
	row, err := scanEntryRow(r)
	if errors.Is(err, sql.ErrNoRows) {
		return Entry{}, errNotFound("workspace memory entry not found")
	}
	if err != nil {
		return Entry{}, err
	}
	return s.hydrateEntry(ctx, &row), nil
}

// List returns workspace memory entries matching filter with evaluated freshness.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Entry, error) {
	query, args, err := buildListQuery(&filter)
	if err != nil {
		return nil, err
	}
	rawRows, err := s.readListRows(ctx, query, args)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(rawRows))
	for i := range rawRows {
		out = append(out, s.hydrateEntry(ctx, &rawRows[i]))
	}
	return out, nil
}

func (s *Store) readListRows(ctx context.Context, query string, args []any) ([]entryRow, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "list workspace memory entries", err)
	}
	defer closeRows(ctx, s.log, rows)

	var rawRows []entryRow
	for rows.Next() {
		row, scanErr := scanEntryRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		rawRows = append(rawRows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "iterate workspace memory entries", err)
	}
	return rawRows, nil
}

func buildListQuery(filter *ListFilter) (string, []any, error) {
	if strings.TrimSpace(filter.WorkspaceID) == "" {
		return "", nil, errInvalidArg("workspace_id is required")
	}
	if filter.Kind != "" && !validKind(filter.Kind) {
		return "", nil, errInvalidArg("invalid workspace memory kind filter")
	}
	if filter.Author != "" && !validAuthor(filter.Author) {
		return "", nil, errInvalidArg("invalid workspace memory author filter")
	}
	if filter.Validity != "" && !validValidity(filter.Validity) {
		return "", nil, errInvalidArg("invalid workspace memory validity filter")
	}
	var b strings.Builder
	b.WriteString(`SELECT id, workspace_id, kind, label, summary, body,
		keywords_json, anchors_json, author, verified,
		validity, invalid_reason, superseded_by, provenance_json,
		revision, updated_at
		FROM workspace_memory_entries WHERE workspace_id = ?`)
	args := []any{filter.WorkspaceID}
	if filter.Kind != "" {
		b.WriteString(" AND kind = ?")
		args = append(args, string(filter.Kind))
	}
	if filter.Verified != nil {
		b.WriteString(" AND verified = ?")
		args = append(args, boolToInt(*filter.Verified))
	}
	if filter.Author != "" {
		b.WriteString(" AND author = ?")
		args = append(args, string(filter.Author))
	}
	if filter.Validity != "" {
		b.WriteString(" AND validity = ?")
		args = append(args, string(filter.Validity))
	}
	b.WriteString(" ORDER BY updated_at DESC, id ASC")
	return b.String(), args, nil
}

// History returns immutable revision snapshots for an entry ordered by revision ascending.
func (s *Store) History(ctx context.Context, workspaceID, entryID string) ([]HistoryEntry, error) {
	if strings.TrimSpace(workspaceID) == "" || strings.TrimSpace(entryID) == "" {
		return nil, errInvalidArg("workspace_id and entry_id are required")
	}
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT history_id, entry_id, workspace_id, revision, op,
			kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			session_id, written_at
		FROM workspace_memory_history
		WHERE workspace_id = ? AND entry_id = ?
		ORDER BY revision ASC`,
		workspaceID, entryID,
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "query workspace memory history", err)
	}
	defer closeRows(ctx, s.log, rows)

	out := []HistoryEntry{}
	for rows.Next() {
		h, scanErr := scanHistoryRow(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "iterate workspace memory history", err)
	}
	return out, nil
}

func loadHistorySnapshot(ctx context.Context, tx *sql.Tx, workspaceID, entryID string, revision uint64) (HistoryEntry, error) {
	row := tx.QueryRowContext(
		ctx,
		`SELECT history_id, entry_id, workspace_id, revision, op,
			kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			session_id, written_at
		FROM workspace_memory_history
		WHERE workspace_id = ? AND entry_id = ? AND revision = ?`,
		workspaceID, entryID, revision,
	)
	h, err := scanHistoryRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return HistoryEntry{}, errNotFound("workspace memory history revision not found")
	}
	if err != nil {
		return HistoryEntry{}, err
	}
	return h, nil
}

func scanEntryRow(sc rowScanner) (entryRow, error) {
	var (
		r                         entryRow
		kind, author, validity    string
		kwJSON, ancJSON, provJSON string
		verifiedInt               int
	)
	err := sc.Scan(
		&r.id, &r.workspaceID, &kind, &r.label, &r.summary, &r.body,
		&kwJSON, &ancJSON, &author, &verifiedInt,
		&validity, &r.invalidReason, &r.supersededBy, &provJSON,
		&r.revision, &r.updatedAtMs,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return entryRow{}, sql.ErrNoRows
		}
		return entryRow{}, errcode.Wrap(errcode.Internal, "scan workspace memory entry", err)
	}
	r.kind = Kind(kind)
	r.author = Author(author)
	r.validity = Validity(validity)
	r.verified = verifiedInt != 0
	if err := unmarshalEntryJSON(kwJSON, ancJSON, provJSON, &r.keywords, &r.anchors, &r.provenance); err != nil {
		return entryRow{}, err
	}
	return r, nil
}

func scanHistoryRow(sc rowScanner) (HistoryEntry, error) {
	var (
		h                          HistoryEntry
		op, kind, author, validity string
		kwJSON, ancJSON, provJSON  string
		verifiedInt                int
		writtenAtMs                int64
	)
	err := sc.Scan(
		&h.HistoryID, &h.EntryID, &h.WorkspaceID, &h.Revision, &op,
		&kind, &h.Label, &h.Summary, &h.Body,
		&kwJSON, &ancJSON, &author, &verifiedInt,
		&validity, &h.InvalidReason, &h.SupersededBy, &provJSON,
		&h.SessionID, &writtenAtMs,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return HistoryEntry{}, sql.ErrNoRows
		}
		return HistoryEntry{}, errcode.Wrap(errcode.Internal, "scan workspace memory history", err)
	}
	h.Op = Operation(op)
	h.Kind = Kind(kind)
	h.Author = Author(author)
	h.Validity = Validity(validity)
	h.Verified = verifiedInt != 0
	h.WrittenAt = time.UnixMilli(writtenAtMs).UTC()
	if err := unmarshalEntryJSON(kwJSON, ancJSON, provJSON, &h.Keywords, &h.Anchors, &h.Provenance); err != nil {
		return HistoryEntry{}, err
	}
	return h, nil
}

func unmarshalEntryJSON(
	kwJSON, ancJSON, provJSON string,
	keywords *[]string,
	anchors *[]Anchor,
	provenance *Provenance,
) error {
	if err := json.Unmarshal([]byte(kwJSON), keywords); err != nil {
		return errcode.Wrap(errcode.Internal, "decode memory keywords", err)
	}
	if err := json.Unmarshal([]byte(ancJSON), anchors); err != nil {
		return errcode.Wrap(errcode.Internal, "decode memory anchors", err)
	}
	if err := json.Unmarshal([]byte(provJSON), provenance); err != nil {
		return errcode.Wrap(errcode.Internal, "decode memory provenance", err)
	}
	return nil
}

func (s *Store) hydrateEntry(ctx context.Context, row *entryRow) Entry {
	statuses, overall := s.evaluateFreshness(ctx, row.workspaceID, row.anchors)
	return Entry{
		ID:             row.id,
		WorkspaceID:    row.workspaceID,
		Kind:           row.kind,
		Label:          row.label,
		Summary:        row.summary,
		Body:           row.body,
		Keywords:       nonNilKeywords(row.keywords),
		Anchors:        nonNilAnchors(row.anchors),
		AnchorStatuses: statuses,
		Author:         row.author,
		Verified:       row.verified,
		Validity:       row.validity,
		InvalidReason:  row.invalidReason,
		SupersededBy:   row.supersededBy,
		Provenance:     row.provenance,
		Revision:       row.revision,
		UpdatedAt:      time.UnixMilli(row.updatedAtMs).UTC(),
		Freshness:      overall,
	}
}

func nonNilAnchors(anc []Anchor) []Anchor {
	if len(anc) == 0 {
		return []Anchor{}
	}
	return anc
}

func (s *Store) evaluateFreshness(ctx context.Context, workspaceID string, anchors []Anchor) ([]AnchorStatus, Freshness) {
	if len(anchors) == 0 {
		return []AnchorStatus{}, FreshnessUnknown
	}
	statuses := make([]AnchorStatus, 0, len(anchors))
	overall := FreshnessFresh
	for _, a := range anchors {
		status := s.checkAnchor(ctx, workspaceID, a)
		if status.Freshness != FreshnessFresh {
			overall = FreshnessStale
		}
		statuses = append(statuses, status)
	}
	return statuses, overall
}

func (s *Store) checkAnchor(ctx context.Context, workspaceID string, a Anchor) AnchorStatus {
	st := AnchorStatus{
		RootName:    a.RootName,
		RelPath:     a.RelPath,
		ContentHash: a.ContentHash,
		Freshness:   FreshnessStale,
	}
	if s.anchors == nil {
		return st
	}
	digest, err := s.anchors.HashAnchor(ctx, workspaceID, a.RootName, a.RelPath)
	if err != nil {
		return st
	}
	if digest == a.ContentHash {
		st.Freshness = FreshnessFresh
	}
	return st
}

// CountUnverified counts valid, unverified entries in one workspace.
func (s *Store) CountUnverified(ctx context.Context, workspaceID string) (int, error) {
	if strings.TrimSpace(workspaceID) == "" {
		return 0, errInvalidArg("workspace_id is required")
	}
	var count int
	err := s.db.QueryRowContext(
		ctx,
		`SELECT COUNT(*)
		FROM workspace_memory_entries
		WHERE workspace_id = ? AND validity = 'valid' AND verified = 0`,
		workspaceID,
	).Scan(&count)
	if err != nil {
		return 0, errcode.Wrap(errcode.Internal, "count unverified workspace memory entries", err)
	}
	return count, nil
}

// CountUnverifiedByWorkspace returns the unverified valid-entry count per workspace
// for every workspace with at least one unverified valid entry.
func (s *Store) CountUnverifiedByWorkspace(ctx context.Context) (map[string]int, error) {
	rows, err := s.db.QueryContext(
		ctx,
		`SELECT workspace_id, COUNT(*)
		FROM workspace_memory_entries
		WHERE validity = 'valid' AND verified = 0
		GROUP BY workspace_id
		HAVING COUNT(*) > 0`,
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.Internal, "count unverified workspace memory entries by workspace", err)
	}
	defer closeRows(ctx, s.log, rows)

	counts := map[string]int{}
	for rows.Next() {
		var wsID string
		var n int
		if scanErr := rows.Scan(&wsID, &n); scanErr != nil {
			return nil, errcode.Wrap(errcode.Internal, "scan unverified workspace memory count", scanErr)
		}
		if n > 0 {
			counts[wsID] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, errcode.Wrap(errcode.Internal, "iterate unverified workspace memory counts", err)
	}
	return counts, nil
}
