package workspacememory

import (
	"bytes"
	"context"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
)

const (
	testDstWSID          = "ws-dst"
	testConflictLabel    = "conflict-entry"
	testAlphaImportFile  = "alpha.md"
	testExportSrcName    = "src"
	testSharedLabel      = "shared-label"
	testRoundTripKeyword = "authorization"
)

func newEnvWithIDPrefix(t *testing.T, prefix string) *testEnv {
	t.Helper()
	env := newTestEnv(t, Config{})
	gen := ids.NewSequence(prefix)
	store, err := NewStore(env.db, env.clk, gen, env.scanner, env.anchors, env.events, Config{})
	if err != nil {
		t.Fatalf("NewStore(%s): %v", prefix, err)
	}
	env.ids = gen
	env.store = store
	return env
}

func TestWME06ExportImportRoundTripEmptyWorkspace(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	src.anchors.setFile(testMainRoot, "README.md", []byte("readme v1"))
	dst.anchors.setFile(testMainRoot, "README.md", []byte("readme v1"))
	agentNote := seedRoundTripSourceEntries(t, ctx, src)

	raw, err := src.store.ExportMarkdown(ctx, testWSID, "alpha-ws")
	if err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	preview := mustPreviewAddAll(t, ctx, dst, testDstWSID, raw, 3)

	res, err := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, ImportFile: testAlphaImportFile, Preview: preview,
	})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.Added != 3 || res.Updated != 0 || res.Skipped != 0 || len(res.Written) != 3 {
		t.Fatalf("ApplyImport counts = %+v", res)
	}
	assertRoundTripEntriesMatch(t, ctx, src, dst, agentNote.ID)
}

func seedRoundTripSourceEntries(t *testing.T, ctx context.Context, src *testEnv) Entry {
	t.Helper()
	pinned, err := src.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindPinned, Label: "pinned-rules",
		Summary: "Core rules for the repository.", Body: "Always enforce authorization in Go services.",
		Keywords: []string{testRoundTripKeyword, "rules"}, Anchors: []AnchorInput{{RootName: testMainRoot, RelPath: "README.md"}},
		Author: AuthorUser, Provenance: Provenance{Principal: testAppPrincipal},
	})
	if err != nil {
		t.Fatalf("Add pinned: %v", err)
	}
	agentNote, err := src.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindNote, Label: "agent-observation",
		Summary: "Build uses Taskfile targets.", Body: "Targeted tests run through task test-pkg in this repository.",
		Keywords: []string{"task", "build"}, Author: AuthorAgent,
		Provenance: Provenance{Principal: "session", SessionID: "s-1", Role: "lead"},
	})
	if err != nil {
		t.Fatalf("Add agentNote: %v", err)
	}
	invTarget, err := src.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindDecision, Label: "old-decision",
		Summary: "Deprecated transport choice.", Body: "Superseded before moving to Connect over private sockets.",
		Author: AuthorUser, Provenance: Provenance{Principal: testAppPrincipal},
	})
	if err != nil {
		t.Fatalf("Add invTarget: %v", err)
	}
	if _, invErr := src.store.Invalidate(ctx, &InvalidateInput{
		WorkspaceID: testWSID, ID: invTarget.ID, ExpectedRevision: invTarget.Revision,
		Reason: "Replaced by Connect.", SupersededBy: pinned.ID, Author: AuthorUser,
	}); invErr != nil {
		t.Fatalf("Invalidate: %v", invErr)
	}
	return agentNote
}

func mustPreviewAddAll(t *testing.T, ctx context.Context, env *testEnv, wsID string, raw []byte, wantCount int) ImportPreview {
	t.Helper()
	preview, err := env.store.PreviewImport(ctx, wsID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if len(preview.Items) != wantCount {
		t.Fatalf("PreviewImport items = %d, want %d", len(preview.Items), wantCount)
	}
	for _, it := range preview.Items {
		if it.Class != ImportClassAdd || it.DefaultChoice != ImportChoiceTakeImported {
			t.Fatalf("item %s = (%q, %q), want (add, take_imported)", it.EntryID, it.Class, it.DefaultChoice)
		}
	}
	return preview
}

func assertRoundTripEntriesMatch(t *testing.T, ctx context.Context, src, dst *testEnv, agentNoteID string) {
	t.Helper()
	srcEntries, err := src.store.List(ctx, ListFilter{WorkspaceID: testWSID})
	if err != nil {
		t.Fatalf("src.List: %v", err)
	}
	dstEntries, err := dst.store.List(ctx, ListFilter{WorkspaceID: testDstWSID})
	if err != nil {
		t.Fatalf("dst.List: %v", err)
	}
	if len(srcEntries) != len(dstEntries) {
		t.Fatalf("dst len = %d, want %d", len(dstEntries), len(srcEntries))
	}
	normalizeForRoundTripCompare(srcEntries)
	normalizeForRoundTripCompare(dstEntries)
	if diff := cmp.Diff(srcEntries, dstEntries); diff != "" {
		t.Fatalf("round-trip entry mismatch (-src +dst):\n%s", diff)
	}
	hist, err := dst.store.History(ctx, testDstWSID, agentNoteID)
	if err != nil {
		t.Fatalf("dst.History: %v", err)
	}
	if len(hist) != 1 || hist[0].Provenance.ImportFile != testAlphaImportFile {
		t.Fatalf("dst.History = %+v, want 1 entry with ImportFile=%s", hist, testAlphaImportFile)
	}
}

func normalizeForRoundTripCompare(entries []Entry) {
	for i := range entries {
		entries[i].WorkspaceID = ""
		entries[i].Provenance = Provenance{}
		entries[i].Revision = 0
		entries[i].UpdatedAt = time.Time{}
	}
}

func TestWME06MergeClasses(t *testing.T) {
	t.Parallel()
	t.Run("add and skip", testMergeAddAndSkip)
	t.Run("conflict defaults to newer updated_at and supports keep_both", testMergeConflictAndKeepBoth)
	t.Run("label conflict supports skip and rename", testMergeLabelConflict)
	t.Run("rejected entries carry reason without blocking valid entries", testMergeRejectedEntries)
}

func testMergeAddAndSkip(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	same := mustAddSimple(t, ctx, src, testWSID, KindFact, "same-entry", "Unchanged summary across workspaces.", "This entry is identical on both source and destination.", AuthorUser)
	addedOnly := mustAddSimple(t, ctx, src, testWSID, KindHowto, "new-entry", "Only present in source export.", "This entry does not exist on the destination workspace yet.", AuthorAgent)
	raw, err := src.store.ExportMarkdown(ctx, testWSID, testExportSrcName)
	if err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	seedIdenticalWithBumpedRevision(t, ctx, src, dst, &same)

	preview, err := dst.store.PreviewImport(ctx, testDstWSID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	byID := previewItemsByEntryID(preview.Items)
	if byID[same.ID].Class != ImportClassSkip || byID[same.ID].DefaultChoice != ImportChoiceSkip {
		t.Fatalf("same item = %+v, want skip", byID[same.ID])
	}
	if byID[addedOnly.ID].Class != ImportClassAdd || byID[addedOnly.ID].DefaultChoice != ImportChoiceTakeImported {
		t.Fatalf("addedOnly item = %+v, want add", byID[addedOnly.ID])
	}

	res, err := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, ImportFile: "merge.md", Preview: preview,
	})
	if err != nil {
		t.Fatalf("ApplyImport: %v", err)
	}
	if res.Added != 1 || res.Skipped != 1 || res.Updated != 0 {
		t.Fatalf("ApplyImport result = %+v, want added=1 skipped=1 updated=0", res)
	}
}

func seedIdenticalWithBumpedRevision(t *testing.T, ctx context.Context, src, dst *testEnv, same *Entry) {
	t.Helper()
	dst.clk.Advance(time.Hour)
	seedPreview, err := dst.store.PreviewImport(ctx, testDstWSID, mustExportSingle(t, ctx, src, same.ID))
	if err != nil {
		t.Fatalf("seed PreviewImport: %v", err)
	}
	if _, applyErr := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, ImportFile: "seed.md", Preview: seedPreview,
	}); applyErr != nil {
		t.Fatalf("seed ApplyImport: %v", applyErr)
	}
	dstSame, err := dst.store.GetByID(ctx, testDstWSID, same.ID)
	if err != nil {
		t.Fatalf("dst GetByID: %v", err)
	}
	if _, updErr := dst.store.Update(ctx, &UpdateInput{
		WorkspaceID: testDstWSID, ID: same.ID, ExpectedRevision: dstSame.Revision,
		Kind: same.Kind, Label: same.Label, Summary: same.Summary, Body: same.Body,
		Author: AuthorUser, Provenance: Provenance{Principal: "cli"},
	}); updErr != nil {
		t.Fatalf("dst Update: %v", updErr)
	}
}

func mustAddSimple(t *testing.T, ctx context.Context, env *testEnv, wsID string, kind Kind, label, summary, body string, author Author) Entry {
	t.Helper()
	e, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: wsID, Kind: kind, Label: label, Summary: summary, Body: body, Author: author,
	})
	if err != nil {
		t.Fatalf("Add %s: %v", label, err)
	}
	return e
}

func mustExportSingle(t *testing.T, ctx context.Context, env *testEnv, id string) []byte {
	t.Helper()
	e, err := env.store.GetByID(ctx, testWSID, id)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	raw, err := EncodeMemoryFile(testExportSrcName, env.clk.Now().UTC(), []Entry{e})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}
	return raw
}

func testMergeConflictAndKeepBoth(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	base := mustAddSimple(t, ctx, src, testWSID, KindDecision, testConflictLabel, "Original summary.", "Original body content in English for conflict testing.", AuthorUser)
	mustImportSingleEntry(t, ctx, src, dst, &base)

	dst.clk.Advance(2 * time.Hour)
	if _, updErr := dst.store.Update(ctx, &UpdateInput{
		WorkspaceID: testDstWSID, ID: base.ID, ExpectedRevision: 1,
		Kind: KindDecision, Label: testConflictLabel, Summary: "Newer local summary.",
		Body: "Local workspace edited this entry later than the imported file.", Author: AuthorUser,
	}); updErr != nil {
		t.Fatalf("dst Update: %v", updErr)
	}

	assertOlderImportDefaultsKeepLocal(t, ctx, src, dst, base.ID)
	assertNewerImportDefaultsTakeImportedAndKeepBoth(t, ctx, src, dst, base.ID)
}

func mustImportSingleEntry(t *testing.T, ctx context.Context, src, dst *testEnv, e *Entry) {
	t.Helper()
	seedRaw, err := EncodeMemoryFile(testExportSrcName, src.clk.Now().UTC(), []Entry{*e})
	if err != nil {
		t.Fatalf("EncodeMemoryFile seed: %v", err)
	}
	seedPrev, err := dst.store.PreviewImport(ctx, testDstWSID, seedRaw)
	if err != nil {
		t.Fatalf("seed Preview: %v", err)
	}
	if _, applyErr := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, Preview: seedPrev,
	}); applyErr != nil {
		t.Fatalf("seed Apply: %v", applyErr)
	}
}

func assertOlderImportDefaultsKeepLocal(t *testing.T, ctx context.Context, src, dst *testEnv, entryID string) {
	t.Helper()
	src.clk.Advance(time.Hour)
	olderImp, err := src.store.Update(ctx, &UpdateInput{
		WorkspaceID: testWSID, ID: entryID, ExpectedRevision: 1,
		Kind: KindDecision, Label: testConflictLabel, Summary: "Older imported summary.",
		Body: "Source workspace edited this entry earlier than destination.", Author: AuthorAgent,
	})
	if err != nil {
		t.Fatalf("src Update older: %v", err)
	}
	olderRaw, err := EncodeMemoryFile(testExportSrcName, src.clk.Now().UTC(), []Entry{olderImp})
	if err != nil {
		t.Fatalf("Encode older: %v", err)
	}
	prev, err := dst.store.PreviewImport(ctx, testDstWSID, olderRaw)
	if err != nil {
		t.Fatalf("Preview A: %v", err)
	}
	if len(prev.Items) != 1 || prev.Items[0].Class != ImportClassConflict || prev.Items[0].DefaultChoice != ImportChoiceKeepLocal {
		t.Fatalf("Preview A item = %+v, want conflict with default keep_local", prev.Items)
	}
}

func assertNewerImportDefaultsTakeImportedAndKeepBoth(t *testing.T, ctx context.Context, src, dst *testEnv, entryID string) {
	t.Helper()
	prev, item := previewNewerConflict(t, ctx, src, dst, entryID)
	res, err := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, ImportFile: "newer.md", Preview: prev,
		Choices: []ImportItemChoice{{ItemID: item.ItemID, Choice: ImportChoiceKeepBoth}},
	})
	if err != nil {
		t.Fatalf("Apply keep_both: %v", err)
	}
	if res.Added != 1 || len(res.Written) != 1 {
		t.Fatalf("keep_both result = %+v, want added=1", res)
	}
	keptImp := res.Written[0]
	if keptImp.ID == entryID || keptImp.Label != "conflict-entry (imported)" || keptImp.Author != AuthorAgent || keptImp.Verified {
		t.Fatalf("keep_both written = %+v", keptImp)
	}
	localGot, err := dst.store.GetByID(ctx, testDstWSID, entryID)
	if err != nil || localGot.Summary != "Newer local summary." {
		t.Fatalf("local after keep_both = (%+v, %v)", localGot, err)
	}
}

func previewNewerConflict(t *testing.T, ctx context.Context, src, dst *testEnv, entryID string) (ImportPreview, ImportPreviewItem) {
	t.Helper()
	src.clk.Advance(3 * time.Hour)
	newerImp, err := src.store.Update(ctx, &UpdateInput{
		WorkspaceID: testWSID, ID: entryID, ExpectedRevision: 2,
		Kind: KindDecision, Label: testConflictLabel, Summary: "Newest imported summary.",
		Body: "Source workspace edited this entry after the destination did.", Author: AuthorAgent,
	})
	if err != nil {
		t.Fatalf("src Update newer: %v", err)
	}
	newerRaw, err := EncodeMemoryFile(testExportSrcName, src.clk.Now().UTC(), []Entry{newerImp})
	if err != nil {
		t.Fatalf("Encode newer: %v", err)
	}
	prev, err := dst.store.PreviewImport(ctx, testDstWSID, newerRaw)
	if err != nil {
		t.Fatalf("Preview B: %v", err)
	}
	item := prev.Items[0]
	if item.Class != ImportClassConflict || item.DefaultChoice != ImportChoiceTakeImported || item.Local == nil || item.Imported == nil {
		t.Fatalf("Preview B item = %+v, want conflict with default take_imported and both sides", item)
	}
	return prev, item
}

func testMergeLabelConflict(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	mustAddSimple(t, ctx, dst, testDstWSID, KindFact, testSharedLabel, "Destination entry owning shared-label.", "Destination already uses this label for a different ID.", AuthorUser)
	imp := mustAddSimple(t, ctx, src, testWSID, KindFact, testSharedLabel, "Source entry with colliding label.", "Source has a different ID but the same label.", AuthorUser)
	raw, err := EncodeMemoryFile(testExportSrcName, src.clk.Now().UTC(), []Entry{imp})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}
	preview, err := dst.store.PreviewImport(ctx, testDstWSID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if len(preview.Items) != 1 || preview.Items[0].Class != ImportClassLabelConflict {
		t.Fatalf("label conflict preview = %+v", preview.Items)
	}
	item := preview.Items[0]
	resSkip, err := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, Preview: preview,
		Choices: []ImportItemChoice{{ItemID: item.ItemID, Choice: ImportChoiceSkip}},
	})
	if err != nil || resSkip.Skipped != 1 || resSkip.Added != 0 {
		t.Fatalf("skip label conflict = (%+v, %v)", resSkip, err)
	}
	resRen, err := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, ImportFile: "ren.md", Preview: preview,
		Choices: []ImportItemChoice{{ItemID: item.ItemID, Choice: ImportChoiceRename, NewLabel: "shared-label-renamed"}},
	})
	if err != nil || resRen.Added != 1 || len(resRen.Written) != 1 || resRen.Written[0].Label != "shared-label-renamed" {
		t.Fatalf("rename label conflict = (%+v, %v)", resRen, err)
	}
}

func testMergeRejectedEntries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	dst := newTestEnv(t, Config{MaxValidEntriesPerWorkspace: 10})
	fakeGH := "gh" + "p_" + strings.Repeat("a1", 18)

	goodBody := "This is valid English prose for the good memory row."
	secBody := "Contains " + fakeGH + " inside body."
	content := "---\nformat: fugue-memory/1\nworkspace: bad\nexported_at: 2026-11-28T12:00:00Z\n---\n\n" +
		"<!-- fugue:entry -->\n## good-one\n\n```yaml\nid: mem-good\nkind: fact\nlabel: good-one\nsummary: Valid imported fact entry.\nkeywords: [valid]\nanchors: []\nauthor: user\nverified: true\nvalidity: valid\ninvalid_reason: \"\"\nsuperseded_by: \"\"\nupdated_at: 2026-11-28T12:00:00Z\nrevision: 1\nbody_bytes: " + strconv.Itoa(len(goodBody)) + "\n```\n" + goodBody + "\n<!-- /fugue:entry -->\n\n" +
		"<!-- fugue:entry -->\n## bad-secret\n\n```yaml\nid: mem-sec\nkind: fact\nlabel: bad-secret\nsummary: Secret imported entry.\nkeywords: []\nanchors: []\nauthor: user\nverified: true\nvalidity: valid\ninvalid_reason: \"\"\nsuperseded_by: \"\"\nupdated_at: 2026-11-28T12:00:00Z\nrevision: 1\nbody_bytes: " + strconv.Itoa(len(secBody)) + "\n```\n" + secBody + "\n<!-- /fugue:entry -->\n\n" +
		"<!-- fugue:entry -->\n## bad-unknown-field\n\n```yaml\nid: mem-unk\nkind: fact\nlabel: bad-unknown-field\nextra_field: nope\nbody_bytes: 2\n```\nhi\n<!-- /fugue:entry -->\n"

	preview, err := dst.store.PreviewImport(ctx, testDstWSID, []byte(content))
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if len(preview.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(preview.Items))
	}
	if preview.Items[0].Class != ImportClassAdd {
		t.Fatalf("item[0] = %+v, want add", preview.Items[0])
	}
	for i := 1; i <= 2; i++ {
		if preview.Items[i].Class != ImportClassRejected || preview.Items[i].RejectReason == "" {
			t.Fatalf("item[%d] = %+v, want rejected with reason", i, preview.Items[i])
		}
		if strings.Contains(preview.Items[i].RejectReason, fakeGH) {
			t.Fatal("secret leaked in reject reason")
		}
	}
}

func TestWME06ApplyImportStaleAfterConcurrentWrite(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	imp := mustAddSimple(t, ctx, src, testWSID, KindFact, "imported-fact", "Fact to import.", "This entry should not commit when target memory changes after preview.", AuthorUser)
	raw, err := src.store.ExportMarkdown(ctx, testWSID, testExportSrcName)
	if err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	preview, err := dst.store.PreviewImport(ctx, testDstWSID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}

	mustAddSimple(t, ctx, dst, testDstWSID, KindNote, "concurrent-note", "Concurrent write.", "Written between PreviewImport and ApplyImport.", AuthorAgent)

	_, applyErr := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, Preview: preview,
	})
	if errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("ApplyImport code = %q, want %q (err=%v)", errcode.CodeOf(applyErr), errcode.MemoryImportStale, applyErr)
	}
	if _, getErr := dst.store.GetByID(ctx, testDstWSID, imp.ID); errcode.CodeOf(getErr) != errcode.NotFound {
		t.Fatalf("imported entry %s was written despite stale preview (err=%v)", imp.ID, getErr)
	}
}

func TestWME07ImportAnchorFreshness(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	src := newEnvWithIDPrefix(t, testExportSrcName)
	dst := newEnvWithIDPrefix(t, "dst")

	src.anchors.setFile(testMainRoot, "README.md", []byte("shared doc"))
	src.anchors.setFile("other-machine-root", "local.txt", []byte("only on source"))
	dst.anchors.setFile(testMainRoot, "README.md", []byte("shared doc"))

	eFresh, err := src.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindFact, Label: "has-target-root",
		Summary: "Anchored to main root.", Body: "Main root exists on target so anchor is hashed on target.",
		Anchors: []AnchorInput{{RootName: testMainRoot, RelPath: "README.md"}}, Author: AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add eFresh: %v", err)
	}
	eMissing, err := src.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindFact, Label: "missing-target-root",
		Summary: "Anchored to missing root.", Body: "Source root does not exist on target so anchor must become stale.",
		Anchors: []AnchorInput{{RootName: "other-machine-root", RelPath: "local.txt"}}, Author: AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add eMissing: %v", err)
	}

	raw, err := src.store.ExportMarkdown(ctx, testWSID, testExportSrcName)
	if err != nil {
		t.Fatalf("ExportMarkdown: %v", err)
	}
	preview, err := dst.store.PreviewImport(ctx, testDstWSID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}
	if _, applyErr := dst.store.ApplyImport(ctx, &ApplyImportInput{
		WorkspaceID: testDstWSID, Principal: testAppPrincipal, Preview: preview,
	}); applyErr != nil {
		t.Fatalf("ApplyImport: %v", applyErr)
	}

	gotFresh, err := dst.store.GetByID(ctx, testDstWSID, eFresh.ID)
	if err != nil || gotFresh.Freshness != FreshnessFresh {
		t.Fatalf("gotFresh = (%+v, %v), want fresh", gotFresh, err)
	}
	gotMissing, err := dst.store.GetByID(ctx, testDstWSID, eMissing.ID)
	if err != nil || gotMissing.Freshness != FreshnessStale || len(gotMissing.Anchors) != 1 || gotMissing.Anchors[0].ContentHash != "" {
		t.Fatalf("gotMissing = (%+v, %v), want stale with empty ContentHash", gotMissing, err)
	}
}

func TestMemoryFileRoundTripTrickyBodies(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		body string
	}{
		{"contains markers", "before\n<!-- fugue:entry -->\nmiddle\n<!-- /fugue:entry -->\nafter"},
		{"contains headings and yaml fence", "## Subheading\n```yaml\nid: fake\n```\ntext"},
		{"crlf lines", "line one\r\nline two\r\n<!-- /fugue:entry -->\r\n"},
		{"trailing newline", "ends with newline\n"},
		{"no trailing newline", "no trailing newline"},
		{"empty body", ""},
	}
	entries := make([]Entry, 0, len(cases))
	for i, tc := range cases {
		entries = append(entries, Entry{
			ID:        "mem-tricky-" + string(rune('a'+i)),
			Kind:      KindNote,
			Label:     "tricky-" + tc.name,
			Summary:   "Tricky body round trip.",
			Body:      tc.body,
			Keywords:  []string{"roundtrip"},
			Author:    AuthorUser,
			Verified:  true,
			Validity:  ValidityValid,
			Revision:  uint64(i + 1),
			UpdatedAt: now,
		})
	}
	raw, err := EncodeMemoryFile("ws-tricky", now, entries)
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}
	dec, err := DecodeMemoryFile(raw)
	if err != nil {
		t.Fatalf("DecodeMemoryFile: %v", err)
	}
	if len(dec.Blocks) != len(cases) {
		t.Fatalf("decoded blocks = %d, want %d", len(dec.Blocks), len(cases))
	}
	for i, tc := range cases {
		b := dec.Blocks[i]
		if b.RejectReason != "" || b.Entry == nil {
			t.Fatalf("case %q rejected: %q", tc.name, b.RejectReason)
		}
		if b.Entry.Body != tc.body {
			t.Fatalf("case %q body mismatch: got %q, want %q", tc.name, b.Entry.Body, tc.body)
		}
	}
}

func TestExportRefusesPlantedSecret(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	var logBuf bytes.Buffer
	logger := slog.New(redact.NewTextHandler(&logBuf, slog.LevelDebug))
	env := newTestEnv(t, Config{Logger: logger})

	planted := "gh" + "p_" + strings.Repeat("a1", 18)
	e, err := env.store.Add(ctx, &AddInput{
		WorkspaceID: testWSID, Kind: KindFact, Label: "secret-export-label",
		Summary: "Normal summary before secret.", Body: "Clean body text for initial write.", Author: AuthorUser,
	})
	if err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, execErr := env.db.ExecContext(ctx, `UPDATE workspace_memory_entries SET body = ? WHERE id = ?`, "contains "+planted+" value", e.ID); execErr != nil {
		t.Fatalf("seed planted secret: %v", execErr)
	}

	_, expErr := env.store.ExportMarkdown(ctx, testWSID, "ws-sec")
	if errcode.CodeOf(expErr) != errcode.MemoryContainsSecret {
		t.Fatalf("ExportMarkdown code = %q, want memory.contains_secret (err=%v)", errcode.CodeOf(expErr), expErr)
	}
	if !strings.Contains(expErr.Error(), e.ID) || !strings.Contains(expErr.Error(), "secret-export-label") {
		t.Fatalf("ExportMarkdown error %q missing entry id/label", expErr.Error())
	}
	if strings.Contains(expErr.Error(), planted) || strings.Contains(logBuf.String(), planted) {
		t.Fatal("planted secret leaked into error or logs")
	}
}

func FuzzDecodeMemoryFile(f *testing.F) {
	seeds := [][]byte{
		[]byte("---\nformat: fugue-memory/1\nworkspace: w\nexported_at: 2026-11-28T12:00:00Z\n---\n"),
		[]byte("---\nformat: fugue-memory/1\nworkspace: w\nexported_at: 2026-11-28T12:00:00Z\n---\n\n<!-- fugue:entry -->\n## lbl\n```yaml\nid: a\nkind: fact\nlabel: lbl\nsummary: s\nauthor: user\nverified: true\nvalidity: valid\nupdated_at: 2026-11-28T12:00:00Z\nrevision: 1\nbody_bytes: 20\n```\n<!-- /fugue:entry -->\n<!-- /fugue:entry -->\n"),
		[]byte("---\nformat: fugue-memory/1\nworkspace: w\nexported_at: 2026-11-28T12:00:00Z\n---\n\n<!-- fugue:entry -->\n## lbl\n```yaml\nid: a\nbody_bytes: 999999\n```\ntruncated"),
		[]byte("---\nformat: fugue-memory/1\nworkspace: w\nexported_at: 2026-11-28T12:00:00Z\n---\n\n<!-- fugue:entry -->\n## lbl\n```yaml\nid: a\nbody_bytes: -5\n```\n"),
		[]byte("---\nformat: fugue-memory/2\nworkspace: w\nexported_at: 2026-11-28T12:00:00Z\n---\n"),
		{0xff, 0xfe, 0xfd, '-', '-', '-'},
		[]byte("<!-- fugue:entry -->\n<!-- fugue:entry -->\n```yaml\n"),
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, err := DecodeMemoryFile(data)
		if err != nil && errcode.CodeOf(err) != errcode.MemoryImportInvalid {
			t.Fatalf("unexpected error code %q: %v", errcode.CodeOf(err), err)
		}
	})
}

func previewItemsByEntryID(items []ImportPreviewItem) map[string]ImportPreviewItem {
	out := make(map[string]ImportPreviewItem, len(items))
	for _, it := range items {
		out[it.EntryID] = it
	}
	return out
}
