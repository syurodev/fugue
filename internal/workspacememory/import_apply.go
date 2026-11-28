package workspacememory

import (
	"context"
	"database/sql"
	"maps"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

type resolvedImportPlan struct {
	item    ImportPreviewItem
	choice  ImportChoice
	label   string
	anchors []Anchor
}

type committedImportWrite struct {
	row entryRow
	op  Operation
}

// ApplyImport applies the user's diff choices from a PreviewImport in one
// SQLite transaction (§9). If any entry revision or the workspace's valid-entry
// set changed since preview time, the transaction rolls back and returns
// errcode.MemoryImportStale.
func (s *Store) ApplyImport(ctx context.Context, in *ApplyImportInput) (ApplyImportResult, error) {
	if in == nil || strings.TrimSpace(in.WorkspaceID) == "" {
		return ApplyImportResult{}, errInvalidArg("workspace_id is required")
	}
	plans, skipped, err := s.buildImportPlans(ctx, in)
	if err != nil {
		return ApplyImportResult{}, err
	}

	importFile := strings.TrimSpace(in.ImportFile)
	if importFile == "" {
		importFile = "import"
	}
	prov := Provenance{
		Principal:  in.Principal,
		ImportFile: importFile,
	}

	writes, added, updated, err := s.executeImportTx(ctx, in.WorkspaceID, &in.Preview, plans, &prov)
	if err != nil {
		return ApplyImportResult{}, err
	}

	written := make([]Entry, 0, len(writes))
	for i := range writes {
		entry := s.hydrateEntry(ctx, &writes[i].row)
		written = append(written, entry)
		if s.events != nil {
			s.events.OnMemoryEntryWritten(ctx, WrittenEvent{
				EntryID:     entry.ID,
				WorkspaceID: entry.WorkspaceID,
				Revision:    entry.Revision,
				Author:      entry.Author,
				Op:          writes[i].op,
			})
		}
	}
	return ApplyImportResult{
		Written: written,
		Added:   added,
		Updated: updated,
		Skipped: skipped,
	}, nil
}

func (s *Store) buildImportPlans(
	ctx context.Context,
	in *ApplyImportInput,
) ([]resolvedImportPlan, int, error) {
	choiceMap, err := indexImportChoices(in.Choices)
	if err != nil {
		return nil, 0, err
	}
	var (
		plans   []resolvedImportPlan
		skipped int
	)
	for i := range in.Preview.Items {
		item := in.Preview.Items[i]
		if item.Class == ImportClassRejected {
			delete(choiceMap, item.ItemID)
			skipped++
			continue
		}
		choice, newLabel := selectItemChoice(&item, choiceMap)
		delete(choiceMap, item.ItemID)
		if err := validateItemChoice(item.Class, choice, newLabel); err != nil {
			return nil, 0, err
		}
		if choice == ImportChoiceSkip || choice == ImportChoiceKeepLocal {
			skipped++
			continue
		}
		if item.Imported == nil {
			return nil, 0, errInvalidArg("import item is missing imported entry")
		}
		label := item.Imported.Label
		if choice == ImportChoiceRename {
			label = newLabel
		}
		anchors := s.resolveImportAnchors(ctx, in.WorkspaceID, item.Imported.Anchors)
		plans = append(plans, resolvedImportPlan{
			item:    item,
			choice:  choice,
			label:   label,
			anchors: anchors,
		})
	}
	if len(choiceMap) > 0 {
		return nil, 0, errInvalidArg("import choice references unknown item_id")
	}
	return plans, skipped, nil
}

func indexImportChoices(choices []ImportItemChoice) (map[string]ImportItemChoice, error) {
	out := make(map[string]ImportItemChoice, len(choices))
	for _, c := range choices {
		if strings.TrimSpace(c.ItemID) == "" {
			return nil, errInvalidArg("import choice item_id is required")
		}
		if _, dup := out[c.ItemID]; dup {
			return nil, errInvalidArg("duplicate import choice item_id")
		}
		out[c.ItemID] = c
	}
	return out, nil
}

func selectItemChoice(item *ImportPreviewItem, choiceMap map[string]ImportItemChoice) (ImportChoice, string) {
	if c, ok := choiceMap[item.ItemID]; ok && c.Choice != "" {
		return c.Choice, c.NewLabel
	}
	return item.DefaultChoice, ""
}

func validateItemChoice(class ImportClass, choice ImportChoice, newLabel string) error {
	switch class {
	case ImportClassRejected:
		return nil
	case ImportClassSkip:
		return requireChoiceIn(choice, "skipped", ImportChoiceSkip, ImportChoiceKeepLocal)
	case ImportClassAdd:
		return requireChoiceIn(choice, "add", ImportChoiceTakeImported, ImportChoiceSkip, ImportChoiceKeepLocal)
	case ImportClassConflict:
		return requireChoiceIn(choice, "conflict", ImportChoiceKeepLocal, ImportChoiceSkip, ImportChoiceTakeImported, ImportChoiceKeepBoth)
	case ImportClassLabelConflict:
		if choice == ImportChoiceRename {
			if strings.TrimSpace(newLabel) == "" {
				return errInvalidArg("new_label is required for rename choice")
			}
			return nil
		}
		return requireChoiceIn(choice, "label conflict", ImportChoiceSkip, ImportChoiceKeepLocal)
	default:
		return errInvalidArg("unknown import item class")
	}
}

func requireChoiceIn(choice ImportChoice, className string, allowed ...ImportChoice) error {
	if slices.Contains(allowed, choice) {
		return nil
	}
	return errInvalidArg("invalid choice for " + className + " import item")
}

// resolveImportAnchors resolves anchors by root name on the target workspace (§9).
// Hashes in the exported file are never copied; missing roots or unreadable
// files are stored with an empty ContentHash so checkAnchor reports stale.
func (s *Store) resolveImportAnchors(ctx context.Context, workspaceID string, anchors []Anchor) []Anchor {
	if len(anchors) == 0 {
		return []Anchor{}
	}
	out := make([]Anchor, 0, len(anchors))
	for _, a := range anchors {
		hash := ""
		if s.anchors != nil {
			if digest, err := s.anchors.HashAnchor(ctx, workspaceID, a.RootName, a.RelPath); err == nil {
				hash = digest
			}
		}
		out = append(out, Anchor{
			RootName:    a.RootName,
			RelPath:     a.RelPath,
			ContentHash: hash,
		})
	}
	return out
}

func (s *Store) executeImportTx(
	ctx context.Context,
	workspaceID string,
	preview *ImportPreview,
	plans []resolvedImportPlan,
	prov *Provenance,
) (writes []committedImportWrite, added, updated int, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, 0, 0, errcode.Wrap(errcode.Internal, "begin workspace memory import transaction", err)
	}
	committed := false
	defer func() {
		if !committed {
			rollbackQuietly(tx)
		}
	}()

	if staleErr := s.verifyImportSnapshot(ctx, tx, workspaceID, preview); staleErr != nil {
		return nil, 0, 0, staleErr
	}

	writes = make([]committedImportWrite, 0, len(plans))
	for i := range plans {
		_, existsOnTarget := preview.SnapshotRevisions[plans[i].item.EntryID]
		w, isAdd, writeErr := s.execImportWrite(ctx, tx, workspaceID, &plans[i], existsOnTarget, prov)
		if writeErr != nil {
			return nil, 0, 0, writeErr
		}
		writes = append(writes, w)
		if isAdd {
			added++
		} else {
			updated++
		}
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return nil, 0, 0, errcode.Wrap(errcode.Internal, "commit workspace memory import", commitErr)
	}
	committed = true
	return writes, added, updated, nil
}

func (s *Store) verifyImportSnapshot(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID string,
	preview *ImportPreview,
) error {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT id, revision, validity FROM workspace_memory_entries WHERE workspace_id = ?`,
		workspaceID,
	)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "snapshot workspace memory revisions", err)
	}
	defer closeRows(ctx, s.log, rows)

	curRevs := map[string]uint64{}
	var curValid []string
	for rows.Next() {
		var (
			id       string
			rev      uint64
			validity string
		)
		if scanErr := rows.Scan(&id, &rev, &validity); scanErr != nil {
			return errcode.Wrap(errcode.Internal, "scan workspace memory snapshot", scanErr)
		}
		curRevs[id] = rev
		if validity == string(ValidityValid) {
			curValid = append(curValid, id)
		}
	}
	if err := rows.Err(); err != nil {
		return errcode.Wrap(errcode.Internal, "iterate workspace memory snapshot", err)
	}
	slices.Sort(curValid)

	if !maps.Equal(curRevs, preview.SnapshotRevisions) || !slices.Equal(curValid, preview.SnapshotValidIDs) {
		return errcode.New(errcode.MemoryImportStale, "workspace memory changed since import preview")
	}
	return nil
}

// execImportWrite is the dedicated owner write path for import (§9). Unlike Add
// and Update, it preserves explicit id, author, verified, validity,
// invalid_reason, and superseded_by from the reviewed file, bypasses per-session
// rate limiting, and re-runs §3 content, uniqueness, and capacity checks inside
// the transaction.
func (s *Store) execImportWrite(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID string,
	plan *resolvedImportPlan,
	existsOnTarget bool,
	prov *Provenance,
) (committedImportWrite, bool, error) {
	targetID, label, isInsert, err := s.prepareImportTarget(ctx, tx, workspaceID, plan, existsOnTarget)
	if err != nil {
		return committedImportWrite{}, false, err
	}
	imp := plan.item.Imported
	if err := s.validateImportInTx(ctx, tx, workspaceID, targetID, label, imp); err != nil {
		return committedImportWrite{}, false, err
	}
	row := buildImportedRow(workspaceID, targetID, label, imp, plan.anchors, prov, s.clk.Now().UTC().UnixMilli())
	if isInsert {
		return s.insertImportedEntry(ctx, tx, &row)
	}
	return s.updateImportedEntry(ctx, tx, &row)
}

func (s *Store) prepareImportTarget(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID string,
	plan *resolvedImportPlan,
	existsOnTarget bool,
) (targetID, label string, isInsert bool, err error) {
	imp := plan.item.Imported
	if plan.choice != ImportChoiceKeepBoth {
		return imp.ID, plan.label, !existsOnTarget, nil
	}
	newID, idErr := s.ids.NewID()
	if idErr != nil {
		return "", "", false, errcode.Wrap(errcode.Internal, "generate keep-both memory entry ID", idErr)
	}
	newLabel, lblErr := nextKeepBothLabel(ctx, tx, workspaceID, imp.Label)
	if lblErr != nil {
		return "", "", false, lblErr
	}
	return newID, newLabel, true, nil
}

func (s *Store) validateImportInTx(
	ctx context.Context,
	tx *sql.Tx,
	workspaceID, targetID, label string,
	imp *Entry,
) error {
	cand := writeCandidate{
		workspaceID:   workspaceID,
		entryID:       targetID,
		kind:          imp.Kind,
		label:         label,
		summary:       imp.Summary,
		body:          imp.Body,
		keywords:      slices.Clone(imp.Keywords),
		invalidReason: imp.InvalidReason,
		author:        imp.Author,
		checkContent:  true,
	}
	if err := s.runPureChecks(&cand); err != nil {
		return err
	}
	if imp.Validity != ValidityValid {
		return nil
	}
	if err := ensureLabelAvailable(ctx, tx, workspaceID, label, targetID); err != nil {
		return err
	}
	return s.checkWorkspaceCapacity(ctx, tx, workspaceID, targetID)
}

func buildImportedRow(
	workspaceID, targetID, label string,
	imp *Entry,
	anchors []Anchor,
	prov *Provenance,
	nowMs int64,
) entryRow {
	return entryRow{
		id:            targetID,
		workspaceID:   workspaceID,
		kind:          imp.Kind,
		label:         label,
		summary:       imp.Summary,
		body:          imp.Body,
		keywords:      nonNilKeywords(imp.Keywords),
		anchors:       anchors,
		author:        imp.Author,
		verified:      imp.Verified,
		validity:      imp.Validity,
		invalidReason: imp.InvalidReason,
		supersededBy:  imp.SupersededBy,
		provenance:    *prov,
		revision:      1,
		updatedAtMs:   nowMs,
	}
}

func (s *Store) insertImportedEntry(ctx context.Context, tx *sql.Tx, row *entryRow) (committedImportWrite, bool, error) {
	if err := insertEntryRow(ctx, tx, row); err != nil {
		return committedImportWrite{}, false, err
	}
	if err := syncImportedFTS(ctx, tx, row); err != nil {
		return committedImportWrite{}, false, err
	}
	if err := insertHistoryRow(ctx, tx, OpAdd, row, ""); err != nil {
		return committedImportWrite{}, false, err
	}
	return committedImportWrite{row: *row, op: OpAdd}, true, nil
}

func (s *Store) updateImportedEntry(ctx context.Context, tx *sql.Tx, row *entryRow) (committedImportWrite, bool, error) {
	cur, err := loadEntryRow(ctx, tx, row.workspaceID, row.id)
	if err != nil {
		return committedImportWrite{}, false, err
	}
	if delErr := ftsDeleteByEntryID(ctx, tx, row.workspaceID, row.id); delErr != nil {
		return committedImportWrite{}, false, delErr
	}
	row.revision = cur.revision + 1
	if err := updateEntryRow(ctx, tx, row); err != nil {
		return committedImportWrite{}, false, err
	}
	if err := syncImportedFTS(ctx, tx, row); err != nil {
		return committedImportWrite{}, false, err
	}
	if err := insertHistoryRow(ctx, tx, OpUpdate, row, ""); err != nil {
		return committedImportWrite{}, false, err
	}
	return committedImportWrite{row: *row, op: OpUpdate}, false, nil
}

func syncImportedFTS(ctx context.Context, tx *sql.Tx, row *entryRow) error {
	if row.validity != ValidityValid {
		return nil
	}
	return ftsInsertByID(ctx, tx, row.id)
}

// nextKeepBothLabel generates a unique label using " (imported)" then
// " (imported 2)", " (imported 3)", ... truncated in runes so the result stays
// within MaxLabelRunes (128).
func nextKeepBothLabel(ctx context.Context, tx *sql.Tx, workspaceID, base string) (string, error) {
	for n := 1; ; n++ {
		suffix := " (imported)"
		if n > 1 {
			suffix = " (imported " + strconv.Itoa(n) + ")"
		}
		candidate := fitLabelWithSuffix(base, suffix)
		err := ensureLabelAvailable(ctx, tx, workspaceID, candidate, "")
		if err == nil {
			return candidate, nil
		}
		if errcode.CodeOf(err) != errcode.MemoryLabelTaken {
			return "", err
		}
	}
}

func fitLabelWithSuffix(base, suffix string) string {
	maxBase := MaxLabelRunes - utf8.RuneCountInString(suffix)
	if maxBase <= 0 {
		return suffix
	}
	if utf8.RuneCountInString(base) <= maxBase {
		return base + suffix
	}
	runes := []rune(base)
	return strings.TrimRight(string(runes[:maxBase]), " ") + suffix
}
