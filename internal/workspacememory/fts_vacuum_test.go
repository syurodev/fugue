package workspacememory

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/storage"
)

func TestFTS5ContentRowIDNonPK(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("db.Close: %v", cerr)
		}
	})
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatalf("storage.Migrate: %v", migErr)
	}

	probeExternalContentNonPK(t, ctx, db)

	if _, eErr := db.ExecContext(ctx, `CREATE VIRTUAL TABLE probe_cl USING fts5(body, content='', contentless_delete=1)`); eErr != nil {
		t.Fatalf("create contentless_delete=1: %v", eErr)
	}
	if _, eErr := db.ExecContext(ctx, `INSERT INTO probe_cl(rowid, body) VALUES (10, 'alpha bravo'), (20, 'charlie delta')`); eErr != nil {
		t.Fatalf("insert contentless: %v", eErr)
	}
	if _, eErr := db.ExecContext(ctx, `DELETE FROM probe_cl WHERE rowid = 10`); eErr != nil {
		t.Fatalf("delete contentless: %v", eErr)
	}
	if _, cErr := db.ExecContext(ctx, `INSERT INTO probe_cl(probe_cl, rank) VALUES('integrity-check', 1)`); cErr != nil {
		t.Fatalf("contentless integrity-check(1): %v", cErr)
	}
}

func probeExternalContentNonPK(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	stmts := []string{
		`CREATE VIRTUAL TABLE probe_ext_fts USING fts5(body, content='workspace_memory_entries', content_rowid='fts_rowid', tokenize='unicode61 remove_diacritics 2')`,
		`INSERT INTO workspace_memory_entries(
			id, workspace_id, fts_rowid, kind, label, summary, body, keywords_json, anchors_json,
			author, verified, validity, invalid_reason, superseded_by, provenance_json, revision, updated_at
		) VALUES
			('a', 'ws-probe', 10, 'fact', 'lbl-a', 'sum a', 'alpha bravo charlie', '[]', '[]', 'user', 1, 'valid', '', '', '{}', 1, 1000),
			('b', 'ws-probe', 20, 'fact', 'lbl-b', 'sum b', 'delta echo foxtrot', '[]', '[]', 'user', 1, 'valid', '', '', '{}', 1, 2000)`,
		`INSERT INTO probe_ext_fts(rowid, body) SELECT fts_rowid, body FROM workspace_memory_entries`,
	}
	for _, s := range stmts {
		if _, eErr := db.ExecContext(ctx, s); eErr != nil {
			t.Fatalf("exec %q: %v", s, eErr)
		}
	}
	var id, snip string
	var score float64
	if qErr := db.QueryRowContext(ctx, `
		SELECT e.id, bm25(probe_ext_fts), snippet(probe_ext_fts, 0, '', '', '...', 8)
		FROM probe_ext_fts JOIN workspace_memory_entries e ON e.fts_rowid = probe_ext_fts.rowid
		WHERE probe_ext_fts MATCH 'bravo'`,
	).Scan(&id, &score, &snip); qErr != nil || id != "a" || !strings.Contains(snip, "bravo") {
		t.Fatalf("query non-PK content_rowid failed: id=%q score=%v snip=%q err=%v", id, score, snip, qErr)
	}
	if _, dErr := db.ExecContext(ctx, `INSERT INTO probe_ext_fts(probe_ext_fts, rowid, body) VALUES ('delete', 10, 'alpha bravo charlie')`); dErr != nil {
		t.Fatalf("fts delete: %v", dErr)
	}
	if _, dErr := db.ExecContext(ctx, `DELETE FROM workspace_memory_entries WHERE id = 'a'`); dErr != nil {
		t.Fatalf("delete item a: %v", dErr)
	}
	if _, vErr := db.ExecContext(ctx, `VACUUM`); vErr != nil {
		t.Fatalf("VACUUM: %v", vErr)
	}
	if _, cErr := db.ExecContext(ctx, `INSERT INTO probe_ext_fts(probe_ext_fts, rank) VALUES('integrity-check', 1)`); cErr != nil {
		t.Fatalf("integrity-check after VACUUM: %v", cErr)
	}
	if _, eErr := db.ExecContext(ctx, `INSERT INTO workspace_memory_entries(
		id, workspace_id, fts_rowid, kind, label, summary, body, keywords_json, anchors_json,
		author, verified, validity, invalid_reason, superseded_by, provenance_json, revision, updated_at
	) VALUES ('c', 'ws-probe', 30, 'fact', 'lbl-c', 'sum c', 'invalidated row omitted from fts', '[]', '[]', 'user', 1, 'invalid', 'retired', '', '{}', 1, 3000)`); eErr != nil {
		t.Fatalf("insert c: %v", eErr)
	}
	if _, cErr := db.ExecContext(ctx, `INSERT INTO probe_ext_fts(probe_ext_fts, rank) VALUES('integrity-check', 1)`); cErr == nil {
		t.Fatal("expected external-content integrity-check(1) to fail when invalidated row is omitted")
	}
}

func TestSearchSurvivesVacuum(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	entries, upd3 := seedVacuumEntries(t, ctx, env)

	// Delete e1 directly so VACUUM compacts implicit rowids of remaining rows.
	deleteFirstRowForVacuumTest(t, ctx, env, entries[0].ID)

	if _, vErr := env.db.ExecContext(ctx, "VACUUM"); vErr != nil {
		t.Fatalf("VACUUM: %v", vErr)
	}

	assertVacuumSearchHit(t, ctx, env, "vacGammaMarkerV2", upd3.ID)
	assertVacuumSearchHit(t, ctx, env, "vacDeltaMarker", entries[3].ID)
	assertVacuumSearchHit(t, ctx, env, "vacEpsilonMarker", entries[4].ID)

	assertPostVacuumMutations(t, ctx, env, &entries[3], &entries[4])
	assertFTSIntegrityChecks(t, ctx, env)
}

func seedVacuumEntries(t *testing.T, ctx context.Context, env *testEnv) ([]Entry, Entry) {
	t.Helper()
	entries := make([]Entry, 0, 5)
	tokens := []string{
		"vacAlphaMarker",
		"vacBetaMarker",
		"vacGammaMarker",
		"vacDeltaMarker",
		"vacEpsilonMarker",
	}
	for i, tok := range tokens {
		env.clk.Advance(time.Second)
		e, err := env.store.Add(ctx, &AddInput{
			WorkspaceID: testWSID,
			Kind:        KindFact,
			Label:       "vac-entry-" + tok,
			Summary:     "This summary mentions " + tok + " in the workspace for vacuum stability.",
			Body:        "This body text anchors " + tok + " across the database during a SQLite vacuum.",
			Author:      AuthorUser,
		})
		if err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
		entries = append(entries, e)
	}
	env.clk.Advance(time.Second)
	upd3, err := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               entries[2].ID,
		ExpectedRevision: entries[2].Revision,
		Kind:             KindFact,
		Label:            entries[2].Label,
		Summary:          "This updated summary mentions vacGammaMarkerV2 in the workspace for vacuum stability.",
		Body:             "This updated body text mentions vacGammaMarkerV2 after revision one in the store.",
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Update e3: %v", err)
	}
	if _, invErr := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               entries[1].ID,
		ExpectedRevision: entries[1].Revision,
		Reason:           "Retired before vacuum.",
		Author:           AuthorUser,
	}); invErr != nil {
		t.Fatalf("Invalidate e2: %v", invErr)
	}
	return entries, upd3
}

func assertPostVacuumMutations(t *testing.T, ctx context.Context, env *testEnv, e4, e5 *Entry) {
	t.Helper()
	if _, uErr := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               e4.ID,
		ExpectedRevision: e4.Revision,
		Kind:             KindFact,
		Label:            e4.Label,
		Summary:          "This post-vacuum summary mentions vacDeltaMarkerV2 in the workspace.",
		Body:             "This post-vacuum updated body mentions vacDeltaMarkerV2 in the store.",
		Author:           AuthorUser,
	}); uErr != nil {
		t.Fatalf("post-VACUUM Update e4: %v", uErr)
	}
	if _, iErr := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               e5.ID,
		ExpectedRevision: e5.Revision,
		Reason:           "Retired after vacuum.",
		Author:           AuthorUser,
	}); iErr != nil {
		t.Fatalf("post-VACUUM Invalidate e5: %v", iErr)
	}
	assertVacuumSearchHit(t, ctx, env, "vacDeltaMarkerV2", e4.ID)
	assertQueryMatches(t, ctx, env, "vacEpsilonMarker", false, false)
}

func assertFTSIntegrityChecks(t *testing.T, ctx context.Context, env *testEnv) {
	t.Helper()
	checks := []string{
		`INSERT INTO workspace_memory_fts_words(workspace_memory_fts_words, rank) VALUES('integrity-check', 1)`,
		`INSERT INTO workspace_memory_fts_trigram(workspace_memory_fts_trigram, rank) VALUES('integrity-check', 1)`,
	}
	for _, stmt := range checks {
		if _, chkErr := env.db.ExecContext(ctx, stmt); chkErr != nil {
			t.Fatalf("FTS5 integrity-check failed (%s): %v", stmt, chkErr)
		}
	}
}

func deleteFirstRowForVacuumTest(t *testing.T, ctx context.Context, env *testEnv, id string) {
	t.Helper()
	tx, err := env.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	defer rollbackQuietly(tx)

	if delErr := ftsDeleteByEntryID(ctx, tx, testWSID, id); delErr != nil {
		t.Fatalf("ftsDeleteByEntryID: %v", delErr)
	}
	if _, delErr := tx.ExecContext(ctx, `DELETE FROM workspace_memory_entries WHERE id = ?`, id); delErr != nil {
		t.Fatalf("delete test row: %v", delErr)
	}
	if cErr := tx.Commit(); cErr != nil {
		t.Fatalf("Commit: %v", cErr)
	}
}

func TestFTSRowIDStableAcrossMutations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	added, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       "stable-rowid",
		Summary:     "Initial summary verifying that fts_rowid stays constant across mutations.",
		Body:        "This entry tests stability of fts_rowid across update, invalidate, restore, and verify.",
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	wantRowID := readEntryFTSRowID(t, ctx, env, added.ID)
	if wantRowID <= 0 {
		t.Fatalf("fts_rowid = %d, want > 0", wantRowID)
	}

	upd, err := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               added.ID,
		ExpectedRevision: added.Revision,
		Kind:             KindFact,
		Label:            "stable-rowid",
		Summary:          "Updated summary verifying that fts_rowid stays constant across mutations.",
		Body:             "Updated body text still keeping the exact same fts_rowid in the table.",
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if got := readEntryFTSRowID(t, ctx, env, added.ID); got != wantRowID {
		t.Fatalf("fts_rowid after Update = %d, want %d", got, wantRowID)
	}
	assertRowIDSurvivesInvalidateRestoreVerify(t, ctx, env, &upd, wantRowID)
}

func assertRowIDSurvivesInvalidateRestoreVerify(t *testing.T, ctx context.Context, env *testEnv, upd *Entry, wantRowID int64) {
	t.Helper()
	inv, err := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               upd.ID,
		ExpectedRevision: upd.Revision,
		Reason:           "Temporarily invalidated.",
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	if got := readEntryFTSRowID(t, ctx, env, upd.ID); got != wantRowID {
		t.Fatalf("fts_rowid after Invalidate = %d, want %d", got, wantRowID)
	}

	restored, err := env.store.Restore(ctx, &RestoreInput{
		WorkspaceID:      testWSID,
		ID:               upd.ID,
		RestoreRevision:  1,
		ExpectedRevision: inv.Revision,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if got := readEntryFTSRowID(t, ctx, env, upd.ID); got != wantRowID {
		t.Fatalf("fts_rowid after Restore = %d, want %d", got, wantRowID)
	}

	if _, vErr := env.store.Verify(ctx, &VerifyInput{
		WorkspaceID:      testWSID,
		ID:               upd.ID,
		ExpectedRevision: restored.Revision,
	}); vErr != nil {
		t.Fatalf("Verify: %v", vErr)
	}
	if got := readEntryFTSRowID(t, ctx, env, upd.ID); got != wantRowID {
		t.Fatalf("fts_rowid after Verify = %d, want %d", got, wantRowID)
	}
}

func readEntryFTSRowID(t *testing.T, ctx context.Context, env *testEnv, id string) int64 {
	t.Helper()
	var rowID int64
	if err := env.db.QueryRowContext(
		ctx,
		`SELECT fts_rowid FROM workspace_memory_entries WHERE id = ?`,
		id,
	).Scan(&rowID); err != nil {
		t.Fatalf("read fts_rowid: %v", err)
	}
	return rowID
}

func assertVacuumSearchHit(t *testing.T, ctx context.Context, env *testEnv, query, wantID string) {
	t.Helper()
	resp, err := env.store.Search(ctx, &SearchInput{WorkspaceID: testWSID, Query: query})
	if err != nil {
		t.Fatalf("Search(%q): %v", query, err)
	}
	if resp.Approximate || len(resp.Results) == 0 {
		t.Fatalf("Search(%q) got approx=%v len=%d, want exact match", query, resp.Approximate, len(resp.Results))
	}
	top := resp.Results[0]
	if top.ID != wantID {
		t.Fatalf("Search(%q) top ID = %q, want %q", query, top.ID, wantID)
	}
	if !strings.Contains(top.Snippet, query) {
		t.Fatalf("Search(%q) snippet = %q, does not match query", query, top.Snippet)
	}
}
