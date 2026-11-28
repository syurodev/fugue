package app

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/workspacememory"
)

const testMemoryGuardPrincipal = "guard"

func setupImportTestService(
	t *testing.T,
	ctx context.Context,
	clk *clock.Fake,
	wsName string,
) (wsID string, memSvc *MemoryService) {
	t.Helper()
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(t, t.TempDir(), clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	hp, err := StartHost(ctx, deps)
	if err != nil {
		t.Fatalf("StartHost: %v", err)
	}
	closeAppStateHost(t, hp)

	ws, err := hp.rt.workspaces.Create(ctx, testAppStateOwner, wsName)
	if err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	memSvc, err = hp.rt.MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}
	return ws.ID, memSvc
}

func TestWME06PreviewExpiryAndEviction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	wsID, memSvc := setupImportTestService(t, ctx, clk, "import-ttl-ws")

	raw, err := workspacememory.EncodeMemoryFile("src", clk.Now().UTC(), []workspacememory.Entry{{
		ID: "mem-ttl-1", Kind: workspacememory.KindFact, Label: "ttl-fact",
		Summary: "TTL check fact entry.", Body: "Valid English prose for preview expiration testing.",
		Author: workspacememory.AuthorUser, Verified: true, Validity: workspacememory.ValidityValid,
		Revision: 1, UpdatedAt: clk.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}

	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: "unknown-preview",
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("unknown preview_id code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}

	expPrev, err := memSvc.PreviewImport(ctx, string(KindApp), wsID, raw)
	if err != nil {
		t.Fatalf("PreviewImport for expiry: %v", err)
	}
	clk.Advance(MemoryImportPreviewTTL + time.Second)
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: expPrev.PreviewID,
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("expired preview_id code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}

	previews := make([]string, 0, MemoryImportPreviewCap+1)
	for range MemoryImportPreviewCap + 1 {
		p, pErr := memSvc.PreviewImport(ctx, string(KindApp), wsID, raw)
		if pErr != nil {
			t.Fatalf("PreviewImport eviction loop: %v", pErr)
		}
		previews = append(previews, p.PreviewID)
	}
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: previews[0],
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("evicted preview[0] code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: previews[len(previews)-1],
	}); applyErr != nil {
		t.Fatalf("latest preview apply failed: %v", applyErr)
	}
}

func TestMemoryImportPrincipalAndSizeGuards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	wsID, memSvc := setupImportTestService(t, ctx, clk, "import-guard-ws")

	for _, nonApp := range []string{string(KindCLI), string(KindTUI), string(KindAppBackground), PrincipalSession, testMemoryGuardPrincipal} {
		if _, pErr := memSvc.PreviewImport(ctx, nonApp, wsID, []byte("x")); errcode.CodeOf(pErr) != errcode.PermissionDenied {
			t.Fatalf("PreviewImport(%q) code = %q, want permission_denied", nonApp, errcode.CodeOf(pErr))
		}
		if _, aErr := memSvc.ApplyImport(ctx, nonApp, &MemoryApplyImportCommand{WorkspaceID: wsID, PreviewID: "p"}); errcode.CodeOf(aErr) != errcode.PermissionDenied {
			t.Fatalf("ApplyImport(%q) code = %q, want permission_denied", nonApp, errcode.CodeOf(aErr))
		}
	}
	for _, nonLocal := range []string{PrincipalSession, testMemoryGuardPrincipal} {
		if _, eErr := memSvc.ExportMemory(ctx, nonLocal, wsID); errcode.CodeOf(eErr) != errcode.PermissionDenied {
			t.Fatalf("ExportMemory(%q) code = %q, want permission_denied", nonLocal, errcode.CodeOf(eErr))
		}
	}
	if _, eErr := memSvc.ExportMemory(ctx, string(KindCLI), wsID); eErr != nil {
		t.Fatalf("ExportMemory(cli) failed: %v", eErr)
	}

	oversized := bytes.Repeat([]byte("a"), MemoryExportImportMaxBytes+1)
	if _, sizeErr := memSvc.PreviewImport(ctx, string(KindApp), wsID, oversized); errcode.CodeOf(sizeErr) != errcode.MemoryImportInvalid {
		t.Fatalf("oversized PreviewImport code = %q, want %q", errcode.CodeOf(sizeErr), errcode.MemoryImportInvalid)
	}
	if _, badErr := memSvc.PreviewImport(ctx, string(KindApp), wsID, []byte(strings.Repeat("not yaml\n", 3))); errcode.CodeOf(badErr) != errcode.MemoryImportInvalid {
		t.Fatalf("bad frontmatter code = %q, want %q", errcode.CodeOf(badErr), errcode.MemoryImportInvalid)
	}
}

func TestWME06PreviewRetryAfterFailedApplyAndRepeatStale(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	wsID, memSvc := setupImportTestService(t, ctx, clk, "import-retry-ws")

	seedRetryTargetEntries(t, ctx, memSvc, wsID)

	raw, err := workspacememory.EncodeMemoryFile("src", clk.Now().UTC(), []workspacememory.Entry{{
		ID: "mem-retry-1", Kind: workspacememory.KindFact, Label: "taken-label",
		Summary: "Retry check entry.", Body: "Valid English prose for testing retry after failed apply.",
		Author: workspacememory.AuthorUser, Verified: true, Validity: workspacememory.ValidityValid,
		Revision: 1, UpdatedAt: clk.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}
	prev, err := memSvc.PreviewImport(ctx, string(KindApp), wsID, raw)
	if err != nil || len(prev.Items) != 1 {
		t.Fatalf("PreviewImport = (%+v, %v)", prev, err)
	}
	itemID := prev.Items[0].ItemID

	// 1. Invalid choice fails without consuming the preview.
	if _, badErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: prev.PreviewID,
		Choices: []MemoryImportItemChoiceCommand{{ItemID: itemID, Choice: "bogus-choice"}},
	}); errcode.CodeOf(badErr) != errcode.InvalidArgument {
		t.Fatalf("invalid choice code = %q, want invalid_argument", errcode.CodeOf(badErr))
	}

	// 2. Transaction failure (memory.label_taken) fails without consuming the preview.
	if _, takenErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: prev.PreviewID,
		Choices: []MemoryImportItemChoiceCommand{{ItemID: itemID, Choice: MemoryImportChoiceRename, NewLabel: "other-taken-label"}},
	}); errcode.CodeOf(takenErr) != errcode.MemoryLabelTaken {
		t.Fatalf("label_taken code = %q, want memory.label_taken", errcode.CodeOf(takenErr))
	}

	// 3. Corrected retry with the same preview_id commits.
	res, err := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: prev.PreviewID,
		Choices: []MemoryImportItemChoiceCommand{{ItemID: itemID, Choice: MemoryImportChoiceRename, NewLabel: "resolved-fresh-label"}},
	})
	if err != nil || res.Added != 1 {
		t.Fatalf("corrected retry ApplyImport = (%+v, %v)", res, err)
	}

	// 4. Repeat apply on the consumed preview_id returns memory.import_stale.
	if _, repErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: prev.PreviewID,
	}); errcode.CodeOf(repErr) != errcode.MemoryImportStale {
		t.Fatalf("repeat ApplyImport code = %q, want memory.import_stale", errcode.CodeOf(repErr))
	}
}

func seedRetryTargetEntries(t *testing.T, ctx context.Context, memSvc *MemoryService, wsID string) {
	t.Helper()
	for _, lbl := range []string{"taken-label", "other-taken-label"} {
		if _, err := memSvc.WriteEntry(ctx, string(KindApp), &MemoryWriteCommand{
			WorkspaceID: wsID, Op: MemoryOpAdd, Kind: MemoryKindFact, Label: lbl,
			Summary: "Seeded entry on target.", Body: "This entry occupies a label on the target workspace.",
		}); err != nil {
			t.Fatalf("seed %s: %v", lbl, err)
		}
	}
}

func TestWME06ConcurrentDoubleApplyCommitsOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	wsID, memSvc := setupImportTestService(t, ctx, clk, "import-concurrent-ws")

	raw, err := workspacememory.EncodeMemoryFile("src", clk.Now().UTC(), []workspacememory.Entry{{
		ID: "mem-conc-1", Kind: workspacememory.KindFact, Label: "concurrent-fact",
		Summary: "Concurrent double apply entry.", Body: "This is valid English prose for verifying that concurrent apply is serialized.",
		Author: workspacememory.AuthorUser, Verified: true, Validity: workspacememory.ValidityValid,
		Revision: 1, UpdatedAt: clk.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}
	prev, err := memSvc.PreviewImport(ctx, string(KindApp), wsID, raw)
	if err != nil {
		t.Fatalf("PreviewImport: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	memSvc.beforeApplyImport = func() {
		close(entered)
		<-release
	}

	var (
		res1 MemoryApplyImportResultView
		err1 error
	)
	task1 := supervise.Go(ctx, "test.apply-import-1", func(tCtx context.Context) error {
		res1, err1 = memSvc.ApplyImport(tCtx, string(KindApp), &MemoryApplyImportCommand{
			WorkspaceID: wsID, PreviewID: prev.PreviewID,
		})
		return nil
	})

	<-entered
	_, err2 := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: wsID, PreviewID: prev.PreviewID,
	})
	close(release)
	if wErr := task1.Wait(); wErr != nil {
		t.Fatalf("task1.Wait: %v", wErr)
	}

	if err1 != nil || res1.Added != 1 {
		t.Fatalf("first ApplyImport = (%+v, %v), want added=1", res1, err1)
	}
	if errcode.CodeOf(err2) != errcode.MemoryImportStale {
		t.Fatalf("second concurrent ApplyImport code = %q, want memory.import_stale (err=%v)", errcode.CodeOf(err2), err2)
	}
	entries, listErr := memSvc.ListEntries(ctx, string(KindApp), MemoryListFilter{WorkspaceID: wsID})
	if listErr != nil || len(entries) != 1 {
		t.Fatalf("ListEntries after concurrent double apply = (%d, %v), want 1", len(entries), listErr)
	}
}
