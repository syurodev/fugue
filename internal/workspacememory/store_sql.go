package workspacememory

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func insertEntryRow(ctx context.Context, tx *sql.Tx, row *entryRow) error {
	ftsRowID, err := nextFTSRowID(ctx, tx)
	if err != nil {
		return err
	}
	row.ftsRowID = ftsRowID
	kwJSON, ancJSON, provJSON, err := marshalEntryJSON(row)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO workspace_memory_entries (
			id, workspace_id, fts_rowid, kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			revision, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.id, row.workspaceID, row.ftsRowID, string(row.kind), row.label, row.summary, row.body,
		kwJSON, ancJSON, string(row.author), boolToInt(row.verified),
		string(row.validity), row.invalidReason, row.supersededBy, provJSON,
		row.revision, row.updatedAtMs,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return errLabelTaken()
		}
		return errcode.Wrap(errcode.Internal, "insert workspace memory entry", err)
	}
	return nil
}

func updateEntryRow(ctx context.Context, tx *sql.Tx, row *entryRow) error {
	kwJSON, ancJSON, provJSON, err := marshalEntryJSON(row)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`UPDATE workspace_memory_entries SET
			kind = ?, label = ?, summary = ?, body = ?,
			keywords_json = ?, anchors_json = ?, author = ?, verified = ?,
			validity = ?, invalid_reason = ?, superseded_by = ?, provenance_json = ?,
			revision = ?, updated_at = ?
		WHERE workspace_id = ? AND id = ?`,
		string(row.kind), row.label, row.summary, row.body,
		kwJSON, ancJSON, string(row.author), boolToInt(row.verified),
		string(row.validity), row.invalidReason, row.supersededBy, provJSON,
		row.revision, row.updatedAtMs,
		row.workspaceID, row.id,
	)
	if err != nil {
		if isUniqueConstraintErr(err) {
			return errLabelTaken()
		}
		return errcode.Wrap(errcode.Internal, "update workspace memory entry", err)
	}
	return nil
}

func insertHistoryRow(ctx context.Context, tx *sql.Tx, op Operation, row *entryRow, sessionID string) error {
	kwJSON, ancJSON, provJSON, err := marshalEntryJSON(row)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO workspace_memory_history (
			entry_id, workspace_id, revision, op,
			kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			session_id, written_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.id, row.workspaceID, row.revision, string(op),
		string(row.kind), row.label, row.summary, row.body,
		kwJSON, ancJSON, string(row.author), boolToInt(row.verified),
		string(row.validity), row.invalidReason, row.supersededBy, provJSON,
		sessionID, row.updatedAtMs,
	)
	if err != nil {
		return errcode.Wrap(errcode.Internal, "insert workspace memory history", err)
	}
	return nil
}

func marshalEntryJSON(row *entryRow) (kwJSON, ancJSON, provJSON string, err error) {
	kwBytes, err := json.Marshal(nonNilKeywords(row.keywords))
	if err != nil {
		return "", "", "", errcode.Wrap(errcode.Internal, "marshal memory keywords", err)
	}
	anc := row.anchors
	if anc == nil {
		anc = []Anchor{}
	}
	ancBytes, err := json.Marshal(anc)
	if err != nil {
		return "", "", "", errcode.Wrap(errcode.Internal, "marshal memory anchors", err)
	}
	provBytes, err := json.Marshal(row.provenance)
	if err != nil {
		return "", "", "", errcode.Wrap(errcode.Internal, "marshal memory provenance", err)
	}
	return string(kwBytes), string(ancBytes), string(provBytes), nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func isUniqueConstraintErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed")
}

func loadEntryRow(ctx context.Context, tx *sql.Tx, workspaceID, id string) (entryRow, error) {
	row := tx.QueryRowContext(
		ctx,
		`SELECT id, workspace_id, kind, label, summary, body,
			keywords_json, anchors_json, author, verified,
			validity, invalid_reason, superseded_by, provenance_json,
			revision, updated_at
		FROM workspace_memory_entries
		WHERE workspace_id = ? AND id = ?`,
		workspaceID, id,
	)
	out, err := scanEntryRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return entryRow{}, errNotFound("workspace memory entry not found")
	}
	if err != nil {
		return entryRow{}, err
	}
	return out, nil
}
