package workspacememory

import (
	"context"
	"database/sql"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

func nextFTSRowID(ctx context.Context, tx *sql.Tx) (int64, error) {
	var next int64
	if err := tx.QueryRowContext(
		ctx,
		`SELECT COALESCE(MAX(fts_rowid), 0) + 1 FROM workspace_memory_entries`,
	).Scan(&next); err != nil {
		return 0, errcode.Wrap(errcode.Internal, "allocate workspace memory fts_rowid", err)
	}
	return next, nil
}

func ftsInsertByID(ctx context.Context, tx *sql.Tx, entryID string) error {
	const wordSQL = `INSERT INTO workspace_memory_fts_words(rowid, label, keywords_json, summary, body)
		SELECT fts_rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = ?`
	if _, err := tx.ExecContext(ctx, wordSQL, entryID); err != nil {
		return errcode.Wrap(errcode.Internal, "insert workspace memory FTS words", err)
	}
	const trigramSQL = `INSERT INTO workspace_memory_fts_trigram(rowid, label, keywords_json, summary, body)
		SELECT fts_rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = ?`
	if _, err := tx.ExecContext(ctx, trigramSQL, entryID); err != nil {
		return errcode.Wrap(errcode.Internal, "insert workspace memory FTS trigram", err)
	}
	return nil
}

func ftsDeleteByEntryID(ctx context.Context, tx *sql.Tx, workspaceID, entryID string) error {
	var ftsRowID int64
	var validity string
	if err := tx.QueryRowContext(
		ctx,
		`SELECT fts_rowid, validity FROM workspace_memory_entries WHERE workspace_id = ? AND id = ?`,
		workspaceID, entryID,
	).Scan(&ftsRowID, &validity); err != nil {
		return errcode.Wrap(errcode.Internal, "load workspace memory fts_rowid", err)
	}
	if validity != string(ValidityValid) {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_memory_fts_words WHERE rowid = ?`, ftsRowID); err != nil {
		return errcode.Wrap(errcode.Internal, "delete workspace memory FTS words", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM workspace_memory_fts_trigram WHERE rowid = ?`, ftsRowID); err != nil {
		return errcode.Wrap(errcode.Internal, "delete workspace memory FTS trigram", err)
	}
	return nil
}
