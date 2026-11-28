package workspacememory

import (
	"context"
	"database/sql"

	"github.com/syurodev/fugue/internal/platform/errcode"
)

type ftsSnapshot struct {
	rowid        int64
	label        string
	keywordsJSON string
	summary      string
	body         string
	validity     string
}

func loadFTSSnapshot(ctx context.Context, tx *sql.Tx, workspaceID, id string) (ftsSnapshot, error) {
	var snap ftsSnapshot
	err := tx.QueryRowContext(
		ctx,
		`SELECT rowid, label, keywords_json, summary, body, validity
		FROM workspace_memory_entries
		WHERE workspace_id = ? AND id = ?`,
		workspaceID, id,
	).Scan(&snap.rowid, &snap.label, &snap.keywordsJSON, &snap.summary, &snap.body, &snap.validity)
	if err != nil {
		return ftsSnapshot{}, errcode.Wrap(errcode.Internal, "load workspace memory FTS snapshot", err)
	}
	return snap, nil
}

func ftsInsertByID(ctx context.Context, tx *sql.Tx, entryID string) error {
	const wordSQL = `INSERT INTO workspace_memory_fts_words(rowid, label, keywords_json, summary, body)
		SELECT rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = ?`
	if _, err := tx.ExecContext(ctx, wordSQL, entryID); err != nil {
		return errcode.Wrap(errcode.Internal, "insert workspace memory FTS words", err)
	}
	const trigramSQL = `INSERT INTO workspace_memory_fts_trigram(rowid, label, keywords_json, summary, body)
		SELECT rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = ?`
	if _, err := tx.ExecContext(ctx, trigramSQL, entryID); err != nil {
		return errcode.Wrap(errcode.Internal, "insert workspace memory FTS trigram", err)
	}
	return nil
}

func ftsDeleteSnapshot(ctx context.Context, tx *sql.Tx, snap *ftsSnapshot) error {
	if snap.validity != string(ValidityValid) {
		return nil
	}
	const wordDel = `INSERT INTO workspace_memory_fts_words(
		workspace_memory_fts_words, rowid, label, keywords_json, summary, body
	) VALUES ('delete', ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, wordDel, snap.rowid, snap.label, snap.keywordsJSON, snap.summary, snap.body); err != nil {
		return errcode.Wrap(errcode.Internal, "delete workspace memory FTS words", err)
	}
	const trigramDel = `INSERT INTO workspace_memory_fts_trigram(
		workspace_memory_fts_trigram, rowid, label, keywords_json, summary, body
	) VALUES ('delete', ?, ?, ?, ?, ?)`
	if _, err := tx.ExecContext(ctx, trigramDel, snap.rowid, snap.label, snap.keywordsJSON, snap.summary, snap.body); err != nil {
		return errcode.Wrap(errcode.Internal, "delete workspace memory FTS trigram", err)
	}
	return nil
}
