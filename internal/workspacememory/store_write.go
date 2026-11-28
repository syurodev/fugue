package workspacememory

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

type rawConflictError struct {
	cur entryRow
}

func (e *rawConflictError) Error() string {
	return "memory entry revision conflict"
}

type entryRow struct {
	id            string
	workspaceID   string
	ftsRowID      int64
	kind          Kind
	label         string
	summary       string
	body          string
	keywords      []string
	anchors       []Anchor
	author        Author
	verified      bool
	validity      Validity
	invalidReason string
	supersededBy  string
	provenance    Provenance
	revision      uint64
	updatedAtMs   int64
}

// Add creates a new workspace memory entry, records history, and emits MemoryEntryWritten.
func (s *Store) Add(ctx context.Context, in *AddInput) (Entry, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" {
		return Entry{}, errInvalidArg("workspace_id is required")
	}
	cand := writeCandidate{
		workspaceID:  in.WorkspaceID,
		kind:         in.Kind,
		label:        in.Label,
		summary:      in.Summary,
		body:         in.Body,
		keywords:     slices.Clone(in.Keywords),
		author:       in.Author,
		sessionID:    in.Provenance.SessionID,
		checkContent: true,
	}
	if err := s.runPureChecks(&cand); err != nil {
		return Entry{}, err
	}
	anchors, err := s.resolveAnchors(ctx, in.WorkspaceID, in.Anchors)
	if err != nil {
		return Entry{}, err
	}
	return s.runWriteTx(ctx, OpAdd, &cand, func(tx *sql.Tx) (entryRow, error) {
		return s.execAdd(ctx, tx, in, anchors)
	})
}

func (s *Store) execAdd(ctx context.Context, tx *sql.Tx, in *AddInput, anchors []Anchor) (entryRow, error) {
	id, err := s.ids.NewID()
	if err != nil {
		return entryRow{}, errcode.Wrap(errcode.Internal, "generate workspace memory entry ID", err)
	}
	row := entryRow{
		id:          id,
		workspaceID: in.WorkspaceID,
		kind:        in.Kind,
		label:       in.Label,
		summary:     in.Summary,
		body:        in.Body,
		keywords:    nonNilKeywords(in.Keywords),
		anchors:     anchors,
		author:      in.Author,
		verified:    in.Author == AuthorUser,
		validity:    ValidityValid,
		provenance:  in.Provenance,
		revision:    1,
		updatedAtMs: s.clk.Now().UTC().UnixMilli(),
	}
	if err := insertEntryRow(ctx, tx, &row); err != nil {
		return entryRow{}, err
	}
	if err := ftsInsertByID(ctx, tx, row.id); err != nil {
		return entryRow{}, err
	}
	return row, nil
}

// Update modifies an existing entry under CAS and records a new history snapshot.
func (s *Store) Update(ctx context.Context, in *UpdateInput) (Entry, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ID) == "" {
		return Entry{}, errInvalidArg("workspace_id and id are required")
	}
	if in.ExpectedRevision == 0 {
		return Entry{}, errInvalidArg("expected_revision must be >= 1")
	}
	cand := writeCandidate{
		workspaceID:  in.WorkspaceID,
		entryID:      in.ID,
		kind:         in.Kind,
		label:        in.Label,
		summary:      in.Summary,
		body:         in.Body,
		keywords:     slices.Clone(in.Keywords),
		author:       in.Author,
		sessionID:    in.Provenance.SessionID,
		checkContent: true,
	}
	if err := s.runPureChecks(&cand); err != nil {
		return Entry{}, err
	}
	anchors, err := s.resolveAnchors(ctx, in.WorkspaceID, in.Anchors)
	if err != nil {
		return Entry{}, err
	}
	return s.runWriteTx(ctx, OpUpdate, &cand, func(tx *sql.Tx) (entryRow, error) {
		return s.execUpdate(ctx, tx, in, anchors)
	})
}

func (s *Store) execUpdate(ctx context.Context, tx *sql.Tx, in *UpdateInput, anchors []Anchor) (entryRow, error) {
	cur, err := s.loadAndCheckCAS(ctx, tx, in.WorkspaceID, in.ID, in.ExpectedRevision)
	if err != nil {
		return entryRow{}, err
	}
	if delErr := ftsDeleteByEntryID(ctx, tx, in.WorkspaceID, in.ID); delErr != nil {
		return entryRow{}, delErr
	}
	next := entryRow{
		id:          cur.id,
		workspaceID: cur.workspaceID,
		kind:        in.Kind,
		label:       in.Label,
		summary:     in.Summary,
		body:        in.Body,
		keywords:    nonNilKeywords(in.Keywords),
		anchors:     anchors,
		author:      in.Author,
		verified:    in.Author == AuthorUser,
		validity:    ValidityValid,
		provenance:  in.Provenance,
		revision:    cur.revision + 1,
		updatedAtMs: s.clk.Now().UTC().UnixMilli(),
	}
	if err := updateEntryRow(ctx, tx, &next); err != nil {
		return entryRow{}, err
	}
	if err := ftsInsertByID(ctx, tx, next.id); err != nil {
		return entryRow{}, err
	}
	return next, nil
}

// Invalidate marks an entry as invalid or superseded under CAS without deleting it.
func (s *Store) Invalidate(ctx context.Context, in *InvalidateInput) (Entry, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ID) == "" {
		return Entry{}, errInvalidArg("workspace_id and id are required")
	}
	if in.ExpectedRevision == 0 {
		return Entry{}, errInvalidArg("expected_revision must be >= 1")
	}
	if !validAuthor(in.Author) {
		return Entry{}, errInvalidArg("invalid workspace memory author")
	}
	if strings.TrimSpace(in.Reason) == "" {
		return Entry{}, errInvalidArg("invalidation reason is required")
	}
	cand := writeCandidate{
		workspaceID:   in.WorkspaceID,
		entryID:       in.ID,
		invalidReason: in.Reason,
		author:        in.Author,
		sessionID:     in.Provenance.SessionID,
		checkContent:  false,
	}
	return s.runWriteTx(ctx, OpInvalidate, &cand, func(tx *sql.Tx) (entryRow, error) {
		return s.execInvalidate(ctx, tx, in)
	})
}

func (s *Store) execInvalidate(ctx context.Context, tx *sql.Tx, in *InvalidateInput) (entryRow, error) {
	cur, err := s.loadAndCheckCAS(ctx, tx, in.WorkspaceID, in.ID, in.ExpectedRevision)
	if err != nil {
		return entryRow{}, err
	}
	if delErr := ftsDeleteByEntryID(ctx, tx, in.WorkspaceID, in.ID); delErr != nil {
		return entryRow{}, delErr
	}
	validity := ValidityInvalid
	if strings.TrimSpace(in.SupersededBy) != "" {
		validity = ValiditySuperseded
	}
	cur.author = in.Author
	cur.verified = in.Author == AuthorUser
	cur.validity = validity
	cur.invalidReason = in.Reason
	cur.supersededBy = in.SupersededBy
	cur.provenance = in.Provenance
	cur.revision++
	cur.updatedAtMs = s.clk.Now().UTC().UnixMilli()
	if err := updateEntryRow(ctx, tx, &cur); err != nil {
		return entryRow{}, err
	}
	return cur, nil
}

// Restore restores an earlier revision from history as a user write under CAS.
func (s *Store) Restore(ctx context.Context, in *RestoreInput) (Entry, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ID) == "" {
		return Entry{}, errInvalidArg("workspace_id and id are required")
	}
	if in.ExpectedRevision == 0 || in.RestoreRevision == 0 {
		return Entry{}, errInvalidArg("expected_revision and restore_revision must be >= 1")
	}
	cand := writeCandidate{
		workspaceID: in.WorkspaceID,
		entryID:     in.ID,
		author:      AuthorUser,
		sessionID:   in.Provenance.SessionID,
	}
	return s.runWriteTx(ctx, OpRestore, &cand, func(tx *sql.Tx) (entryRow, error) {
		return s.execRestore(ctx, tx, in)
	})
}

func (s *Store) execRestore(ctx context.Context, tx *sql.Tx, in *RestoreInput) (entryRow, error) {
	cur, err := s.loadAndCheckCAS(ctx, tx, in.WorkspaceID, in.ID, in.ExpectedRevision)
	if err != nil {
		return entryRow{}, err
	}
	snap, err := loadHistorySnapshot(ctx, tx, in.WorkspaceID, in.ID, in.RestoreRevision)
	if err != nil {
		return entryRow{}, err
	}
	restoredCand := writeCandidate{
		workspaceID:  in.WorkspaceID,
		entryID:      in.ID,
		kind:         snap.Kind,
		label:        snap.Label,
		summary:      snap.Summary,
		body:         snap.Body,
		keywords:     snap.Keywords,
		author:       AuthorUser,
		sessionID:    in.Provenance.SessionID,
		checkContent: true,
	}
	if chkErr := s.runWriteChecks(ctx, tx, &restoredCand); chkErr != nil {
		return entryRow{}, chkErr
	}
	if delErr := ftsDeleteByEntryID(ctx, tx, in.WorkspaceID, in.ID); delErr != nil {
		return entryRow{}, delErr
	}
	next := entryRow{
		id:          cur.id,
		workspaceID: cur.workspaceID,
		kind:        snap.Kind,
		label:       snap.Label,
		summary:     snap.Summary,
		body:        snap.Body,
		keywords:    nonNilKeywords(snap.Keywords),
		anchors:     slices.Clone(snap.Anchors),
		author:      AuthorUser,
		verified:    true,
		validity:    ValidityValid,
		provenance:  in.Provenance,
		revision:    cur.revision + 1,
		updatedAtMs: s.clk.Now().UTC().UnixMilli(),
	}
	if err := updateEntryRow(ctx, tx, &next); err != nil {
		return entryRow{}, err
	}
	if err := ftsInsertByID(ctx, tx, next.id); err != nil {
		return entryRow{}, err
	}
	return next, nil
}

// Verify marks an entry as verified by a user under CAS.
func (s *Store) Verify(ctx context.Context, in *VerifyInput) (Entry, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" || strings.TrimSpace(in.ID) == "" {
		return Entry{}, errInvalidArg("workspace_id and id are required")
	}
	if in.ExpectedRevision == 0 {
		return Entry{}, errInvalidArg("expected_revision must be >= 1")
	}
	cand := writeCandidate{
		workspaceID: in.WorkspaceID,
		entryID:     in.ID,
		author:      AuthorUser,
		sessionID:   in.Provenance.SessionID,
	}
	return s.runWriteTx(ctx, OpVerify, &cand, func(tx *sql.Tx) (entryRow, error) {
		cur, err := s.loadAndCheckCAS(ctx, tx, in.WorkspaceID, in.ID, in.ExpectedRevision)
		if err != nil {
			return entryRow{}, err
		}
		cur.author = AuthorUser
		cur.verified = true
		cur.provenance = in.Provenance
		cur.revision++
		cur.updatedAtMs = s.clk.Now().UTC().UnixMilli()
		if err := updateEntryRow(ctx, tx, &cur); err != nil {
			return entryRow{}, err
		}
		return cur, nil
	})
}

func (s *Store) runWriteTx(
	ctx context.Context,
	op Operation,
	cand *writeCandidate,
	mutate func(*sql.Tx) (entryRow, error),
) (Entry, error) {
	if err := s.runPureChecks(cand); err != nil {
		return Entry{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Entry{}, errcode.Wrap(errcode.Internal, "begin workspace memory write transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackQuietly(tx)
		}
	}()

	if dbErr := s.runDBChecks(ctx, tx, cand); dbErr != nil {
		return Entry{}, dbErr
	}
	row, err := mutate(tx)
	if err != nil {
		var conflict *rawConflictError
		if errors.As(err, &conflict) {
			rollbackQuietly(tx)
			committed = true
			hydrated := s.hydrateEntry(ctx, &conflict.cur)
			return Entry{}, newRevisionConflict(&hydrated)
		}
		return Entry{}, err
	}
	if histErr := insertHistoryRow(ctx, tx, op, &row, cand.sessionID); histErr != nil {
		return Entry{}, histErr
	}
	if commitErr := tx.Commit(); commitErr != nil {
		return Entry{}, errcode.Wrap(errcode.Internal, "commit workspace memory write", commitErr)
	}
	committed = true
	entry := s.hydrateEntry(ctx, &row)
	s.emitWrittenEvent(ctx, &entry, op)
	return entry, nil
}

func (s *Store) emitWrittenEvent(ctx context.Context, entry *Entry, op Operation) {
	if s.events == nil {
		return
	}
	count, err := s.CountUnverified(ctx, entry.WorkspaceID)
	if err != nil {
		s.log.WarnContext(ctx, "count unverified workspace memory entries", "error_code", string(errcode.CodeOf(err)))
	}
	s.events.OnMemoryEntryWritten(ctx, WrittenEvent{
		EntryID:         entry.ID,
		WorkspaceID:     entry.WorkspaceID,
		Revision:        entry.Revision,
		Author:          entry.Author,
		Op:              op,
		UnverifiedCount: count,
	})
}

func (s *Store) loadAndCheckCAS(ctx context.Context, tx *sql.Tx, workspaceID, id string, expected uint64) (entryRow, error) {
	cur, err := loadEntryRow(ctx, tx, workspaceID, id)
	if err != nil {
		return entryRow{}, err
	}
	if cur.revision != expected {
		return entryRow{}, &rawConflictError{cur: cur}
	}
	return cur, nil
}

func (s *Store) resolveAnchors(ctx context.Context, workspaceID string, inputs []AnchorInput) ([]Anchor, error) {
	if len(inputs) == 0 {
		return []Anchor{}, nil
	}
	if s.anchors == nil {
		return nil, errcode.New(errcode.FailedPrecondition, "anchor hasher is not configured")
	}
	out := make([]Anchor, 0, len(inputs))
	for _, in := range inputs {
		if strings.TrimSpace(in.RootName) == "" || strings.TrimSpace(in.RelPath) == "" {
			return nil, errInvalidArg("anchor root_name and rel_path are required")
		}
		digest, err := s.anchors.HashAnchor(ctx, workspaceID, in.RootName, in.RelPath)
		if err != nil {
			code := errcode.CodeOf(err)
			return nil, errcode.Wrap(code, "hash workspace anchor", err)
		}
		out = append(out, Anchor{
			RootName:    in.RootName,
			RelPath:     in.RelPath,
			ContentHash: digest,
		})
	}
	return out, nil
}

func nonNilKeywords(kws []string) []string {
	if len(kws) == 0 {
		return []string{}
	}
	return slices.Clone(kws)
}
