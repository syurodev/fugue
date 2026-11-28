package workspacememory

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/storage"
)

func TestFTS5AndTrigramSupport(t *testing.T) {
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

	_, err = db.ExecContext(ctx, `INSERT INTO workspace_memory_entries (
		id, workspace_id, fts_rowid, kind, label, summary, body, keywords_json, anchors_json,
		author, verified, validity, invalid_reason, superseded_by, provenance_json, revision, updated_at
	) VALUES (
		'e1', 'ws-fts', 1, 'fact', 'fts-probe',
		'Cấu hình layerWeights verified rule',
		'The layerWeights map lives in internal/workspacememory/store_read.go.',
		'["configuration"]', '[]', 'user', 1, 'valid', '', '', '{}', 1, 1000
	)`)
	if err != nil {
		t.Fatalf("insert probe row: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_memory_fts_words(rowid, label, keywords_json, summary, body)
		SELECT fts_rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = 'e1'`)
	if err != nil {
		t.Fatalf("insert fts words: %v", err)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO workspace_memory_fts_trigram(rowid, label, keywords_json, summary, body)
		SELECT fts_rowid, label, keywords_json, summary, body FROM workspace_memory_entries WHERE id = 'e1'`)
	if err != nil {
		t.Fatalf("insert fts trigram: %v", err)
	}

	var id string
	var score float64
	if qErr := db.QueryRowContext(ctx, `
		SELECT e.id, bm25(workspace_memory_fts_words, 10.0, 10.0, 4.0, 1.0)
		FROM workspace_memory_fts_words
		JOIN workspace_memory_entries e ON e.fts_rowid = workspace_memory_fts_words.rowid
		WHERE workspace_memory_fts_words MATCH '"cau" "hinh"'`,
	).Scan(&id, &score); qErr != nil || id != "e1" {
		t.Fatalf("word FTS5 query got (%q, %v, %v)", id, score, qErr)
	}

	if qErr := db.QueryRowContext(ctx, `
		SELECT e.id, bm25(workspace_memory_fts_trigram, 10.0, 10.0, 4.0, 1.0)
		FROM workspace_memory_fts_trigram
		JOIN workspace_memory_entries e ON e.fts_rowid = workspace_memory_fts_trigram.rowid
		WHERE workspace_memory_fts_trigram MATCH '"Weights"'`,
	).Scan(&id, &score); qErr != nil || id != "e1" {
		t.Fatalf("trigram FTS5 query got (%q, %v, %v)", id, score, qErr)
	}
}

const (
	testVerifiedRuleLabel = "verified-rule"
	testLifecycleLabel    = "lifecycle-sync"
)

func seedWME03Fixtures(t *testing.T, ctx context.Context, env *testEnv) map[string]Entry {
	t.Helper()
	const anchorRel = "internal/workspacememory/store_read.go"
	env.anchors.setFile(testMainRoot, anchorRel, []byte("package workspacememory\n"))

	specs := wme03FixtureSpecs(anchorRel)
	out := make(map[string]Entry, len(specs))
	for i := range specs {
		env.clk.Advance(time.Second)
		entry, err := env.store.Add(ctx, &specs[i])
		if err != nil {
			t.Fatalf("Add fixture %s: %v", specs[i].Label, err)
		}
		out[specs[i].Label] = entry
	}
	return out
}

func wme03FixtureSpecs(anchorRel string) []AddInput {
	return []AddInput{
		{
			WorkspaceID: testWSID,
			Kind:        KindConvention,
			Label:       testVerifiedRuleLabel,
			Summary:     "Every entry carries a verified status flag set by the host.",
			Body:        "User writes mark entries verified so agents know which notes were reviewed.",
			Keywords:    []string{"verification", "review"},
			Anchors:     []AnchorInput{{RootName: testMainRoot, RelPath: anchorRel}},
			Author:      AuthorUser,
		},
		{
			WorkspaceID: testWSID,
			Kind:        KindFact,
			Label:       "prompt-layer-weights",
			Summary:     "Prompt builder uses layerWeights to order sections.",
			Body:        "The layerWeights table assigns priority to pinned blocks over collection summaries.",
			Keywords:    []string{"prompt", "ranking"},
			Author:      AuthorAgent,
		},
		{
			WorkspaceID: testWSID,
			Kind:        KindHowto,
			Label:       "read-path-guide",
			Summary:     "Read path hydrates anchor freshness after closing SQLite rows.",
			Body:        "See internal/workspacememory/store_read.go for how GetByID and List evaluate anchors.",
			Keywords:    []string{"sqlite", "freshness"},
			Author:      AuthorUser,
		},
		{
			WorkspaceID: testWSID,
			Kind:        KindNote,
			Label:       "vietnamese-config-note",
			Summary:     "Workspace config notes mention cấu hình for local settings.",
			Body:        "The configuration file is stored per workspace and keeps the term \"cấu hình\" for UI labels.",
			Keywords:    []string{"cấu hình", "settings"},
			Author:      AuthorUser,
		},
		{
			WorkspaceID: testWSID,
			Kind:        KindDecision,
			Label:       "sqlite-lock-policy",
			Summary:     "Single-connection database pool handles all metadata commits.",
			Body:        "Transactions stay brief so concurrent readers do not block on the pool.",
			Keywords:    []string{"concurrency", "mutex", "serialization"},
			Author:      AuthorAgent,
		},
		{
			WorkspaceID: testWSID,
			Kind:        KindNote,
			Label:       "noise-unverified",
			Summary:     "Background filler note about verified builds and miscellaneous topics.",
			Body:        "Sometimes developers mention verified builds in passing without details.",
			Author:      AuthorAgent,
		},
	}
}

func TestWME03SearchEvidence(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})
	entries := seedWME03Fixtures(t, ctx, env)

	queries := []struct {
		name      string
		query     string
		wantLabel string
	}{
		{"prefix", "verif", testVerifiedRuleLabel},
		{"code identifier substring", "Weights", "prompt-layer-weights"},
		{"path substring", "workspacememory/store_read", "read-path-guide"},
		{"vietnamese without diacritics", "cau hinh", "vietnamese-config-note"},
		{"keyword synonym only", "serialization", "sqlite-lock-policy"},
	}

	for _, tc := range queries {
		resp, err := env.store.Search(ctx, &SearchInput{
			WorkspaceID: testWSID,
			Query:       tc.query,
		})
		if err != nil {
			t.Fatalf("Search(%s): %v", tc.name, err)
		}
		if resp.Approximate || len(resp.Results) == 0 {
			t.Fatalf("Search(%s) got approximate=%v len=%d, want exact matches", tc.name, resp.Approximate, len(resp.Results))
		}
		top3 := topLabels(resp.Results, 3)
		if !slices.Contains(top3, tc.wantLabel) {
			t.Fatalf("Search(%s) top3 = %v, want %q in top 3", tc.name, top3, tc.wantLabel)
		}
		assertSearchResultFields(t, &resp.Results[0], entries)
	}
	if resp, err := env.store.Search(ctx, &SearchInput{WorkspaceID: testWSID, Query: "verified"}); err != nil {
		t.Fatalf("Search(verified): %v", err)
	} else if len(resp.Results) < 2 || resp.Results[0].Label != testVerifiedRuleLabel {
		t.Fatalf("expected verified+fresh entry first, got %+v", topLabels(resp.Results, 3))
	}
}

func topLabels(results []SearchResult, n int) []string {
	limit := min(n, len(results))
	out := make([]string, 0, limit)
	for i := range limit {
		out = append(out, results[i].Label)
	}
	return out
}

func assertSearchResultFields(t *testing.T, r *SearchResult, entries map[string]Entry) {
	t.Helper()
	want, ok := entries[r.Label]
	if !ok {
		t.Fatalf("unexpected label %q", r.Label)
	}
	if r.ID != want.ID || r.Kind != want.Kind || r.Summary != want.Summary ||
		r.Snippet == "" || r.Verified != want.Verified || r.Author != want.Author ||
		r.Freshness != want.Freshness || r.Revision != want.Revision {
		t.Fatalf("result fields mismatch: got %+v, want entry %+v", r, want)
	}
}

func TestSearchApproximateFallback(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})
	seedWME03Fixtures(t, ctx, env)

	resp, err := env.store.Search(ctx, &SearchInput{
		WorkspaceID: testWSID,
		Query:       "layerWeightz",
	})
	if err != nil {
		t.Fatalf("Search approximate: %v", err)
	}
	if !resp.Approximate || len(resp.Results) == 0 || len(resp.Results) > 5 {
		t.Fatalf("approximate response = (approx=%v, len=%d), want true and 1..5 results", resp.Approximate, len(resp.Results))
	}
	for _, r := range resp.Results {
		if !r.Approximate {
			t.Fatalf("result %q missing Approximate=true flag", r.Label)
		}
	}
	if !slices.Contains(topLabels(resp.Results, len(resp.Results)), "prompt-layer-weights") {
		t.Fatalf("expected prompt-layer-weights in approximate results, got %v", topLabels(resp.Results, len(resp.Results)))
	}
}

func TestSearchSanitizesHostileQueries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})
	seedWME03Fixtures(t, ctx, env)

	queries := []string{
		"",
		"   \t\n  ",
		`"unclosed quote`,
		`"""`,
		`((layerWeights)`,
		`AND OR NOT NEAR`,
		`verified*`,
		`body:verified OR (label:foo* NOT bar)`,
		`--- *** ^^^ :::`,
		`NEAR(verified weights, 5)`,
	}
	for _, q := range queries {
		if _, err := env.store.Search(ctx, &SearchInput{WorkspaceID: testWSID, Query: q}); err != nil {
			t.Fatalf("Search(%q) returned unexpected error: %v", q, err)
		}
	}
}

func TestSearchIndexFollowsWrites(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	added, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID,
		Kind:        KindFact,
		Label:       testLifecycleLabel,
		Summary:     "Initial alphaToken summary for index sync testing.",
		Body:        "Original alphaToken body stored at revision one.",
		Author:      AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	assertQueryMatches(t, ctx, env, "alphaToken", false, true)

	updated, err := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               added.ID,
		ExpectedRevision: added.Revision,
		Kind:             KindFact,
		Label:            testLifecycleLabel,
		Summary:          "Updated betaToken summary after edit.",
		Body:             "Replacement betaToken body stored at revision two.",
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	assertQueryMatches(t, ctx, env, "alphaToken", false, false)
	assertQueryMatches(t, ctx, env, "betaToken", false, true)

	// Failed write (language check) must leave betaToken searchable.
	if _, badErr := env.store.Update(ctx, &UpdateInput{
		WorkspaceID:      testWSID,
		ID:               added.ID,
		ExpectedRevision: updated.Revision,
		Kind:             KindFact,
		Label:            testLifecycleLabel,
		Summary:          "Tóm tắt bằng tiếng Việt phải bị từ chối khi kiểm tra ngôn ngữ.",
		Body:             "Nội dung này hoàn toàn bằng tiếng Việt nên giao dịch phải rollback và giữ nguyên chỉ mục.",
		Author:           AuthorUser,
	}); errcode.CodeOf(badErr) != errcode.MemoryLanguage {
		t.Fatalf("failed Update code = %q, want %q", errcode.CodeOf(badErr), errcode.MemoryLanguage)
	}
	assertQueryMatches(t, ctx, env, "betaToken", false, true)

	assertInvalidateRestoreAndVerifySync(t, ctx, env, &updated)
}

func assertInvalidateRestoreAndVerifySync(t *testing.T, ctx context.Context, env *testEnv, updated *Entry) {
	t.Helper()
	inv, err := env.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID:      testWSID,
		ID:               updated.ID,
		ExpectedRevision: updated.Revision,
		Reason:           "Temporarily retired.",
		Author:           AuthorUser,
	})
	if err != nil {
		t.Fatalf("Invalidate: %v", err)
	}
	assertQueryMatches(t, ctx, env, "betaToken", false, false)
	assertQueryMatches(t, ctx, env, "betaToken", true, true)

	restored, err := env.store.Restore(ctx, &RestoreInput{
		WorkspaceID:      testWSID,
		ID:               updated.ID,
		RestoreRevision:  1,
		ExpectedRevision: inv.Revision,
	})
	if err != nil {
		t.Fatalf("Restore: %v", err)
	}
	assertQueryMatches(t, ctx, env, "alphaToken", false, true)

	if _, vErr := env.store.Verify(ctx, &VerifyInput{
		WorkspaceID:      testWSID,
		ID:               updated.ID,
		ExpectedRevision: restored.Revision,
	}); vErr != nil {
		t.Fatalf("Verify: %v", vErr)
	}
	assertQueryMatches(t, ctx, env, "alphaToken", false, true)
}

func assertQueryMatches(t *testing.T, ctx context.Context, env *testEnv, query string, includeInvalid, wantMatch bool) {
	t.Helper()
	resp, err := env.store.Search(ctx, &SearchInput{
		WorkspaceID:    testWSID,
		Query:          query,
		IncludeInvalid: includeInvalid,
	})
	if err != nil {
		t.Fatalf("Search(%q, invalid=%v): %v", query, includeInvalid, err)
	}
	matched := !resp.Approximate && len(resp.Results) > 0
	if matched != wantMatch {
		t.Fatalf("Search(%q, invalid=%v) matched=%v (approx=%v len=%d), want %v",
			query, includeInvalid, matched, resp.Approximate, len(resp.Results), wantMatch)
	}
}

func TestSearchWorkspaceIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	env := newTestEnv(t, Config{})

	if _, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: "ws-alpha",
		Kind:        KindFact,
		Label:       "alpha-only",
		Summary:     "Isolated secretMarkerZulu fact in alpha workspace.",
		Body:        "This secretMarkerZulu entry belongs exclusively to ws-alpha.",
		Author:      AuthorUser,
	}); err != nil {
		t.Fatalf("Add ws-alpha: %v", err)
	}
	if _, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: "ws-beta",
		Kind:        KindFact,
		Label:       "beta-only",
		Summary:     "Different fact in beta workspace.",
		Body:        "This entry belongs to ws-beta and mentions builds.",
		Author:      AuthorUser,
	}); err != nil {
		t.Fatalf("Add ws-beta: %v", err)
	}

	resp, err := env.store.Search(ctx, &SearchInput{
		WorkspaceID: "ws-beta",
		Query:       "secretMarkerZulu",
	})
	if err != nil {
		t.Fatalf("Search ws-beta: %v", err)
	}
	for _, r := range resp.Results {
		if r.Label == "alpha-only" {
			t.Fatalf("cross-workspace entry leaked into ws-beta search: %+v", r)
		}
	}
}
