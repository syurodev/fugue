package app

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspacememory"
)

func TestWME06PreviewExpiryAndEviction(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(t, t.TempDir(), clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	hp, err := StartHost(ctx, deps)
	if err != nil {
		t.Fatalf("StartHost: %v", err)
	}
	closeAppStateHost(t, hp)

	ws, err := hp.rt.workspaces.Create(ctx, testAppStateOwner, "import-ttl-ws")
	if err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	memSvc, err := hp.rt.MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}

	raw, err := workspacememory.EncodeMemoryFile("src", clk.Now().UTC(), []workspacememory.Entry{{
		ID: "mem-ttl-1", Kind: workspacememory.KindFact, Label: "ttl-fact",
		Summary: "TTL check fact entry.", Body: "Valid English prose for preview expiration testing.",
		Author: workspacememory.AuthorUser, Verified: true, Validity: workspacememory.ValidityValid,
		Revision: 1, UpdatedAt: clk.Now().UTC(),
	}})
	if err != nil {
		t.Fatalf("EncodeMemoryFile: %v", err)
	}

	// Unknown preview_id -> memory.import_stale.
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: ws.ID, PreviewID: "unknown-preview",
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("unknown preview_id code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}

	// Expired after 30 minutes -> memory.import_stale.
	expPrev, err := memSvc.PreviewImport(ctx, string(KindApp), ws.ID, raw)
	if err != nil {
		t.Fatalf("PreviewImport for expiry: %v", err)
	}
	clk.Advance(MemoryImportPreviewTTL + time.Second)
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: ws.ID, PreviewID: expPrev.PreviewID,
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("expired preview_id code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}

	// Bounded to 8 previews: 9th evicts 1st.
	previews := make([]string, 0, MemoryImportPreviewCap+1)
	for range MemoryImportPreviewCap + 1 {
		p, pErr := memSvc.PreviewImport(ctx, string(KindApp), ws.ID, raw)
		if pErr != nil {
			t.Fatalf("PreviewImport eviction loop: %v", pErr)
		}
		previews = append(previews, p.PreviewID)
	}
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: ws.ID, PreviewID: previews[0],
	}); errcode.CodeOf(applyErr) != errcode.MemoryImportStale {
		t.Fatalf("evicted preview[0] code = %q, want %q", errcode.CodeOf(applyErr), errcode.MemoryImportStale)
	}
	if _, applyErr := memSvc.ApplyImport(ctx, string(KindApp), &MemoryApplyImportCommand{
		WorkspaceID: ws.ID, PreviewID: previews[len(previews)-1],
	}); applyErr != nil {
		t.Fatalf("latest preview apply failed: %v", applyErr)
	}
}

func TestMemoryImportPrincipalAndSizeGuards(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(t, t.TempDir(), clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	hp, err := StartHost(ctx, deps)
	if err != nil {
		t.Fatalf("StartHost: %v", err)
	}
	closeAppStateHost(t, hp)

	ws, err := hp.rt.workspaces.Create(ctx, testAppStateOwner, "import-guard-ws")
	if err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	memSvc, err := hp.rt.MemoryService()
	if err != nil {
		t.Fatalf("MemoryService: %v", err)
	}

	for _, nonApp := range []string{string(KindCLI), string(KindTUI), string(KindAppBackground), PrincipalSession, "guard"} {
		if _, pErr := memSvc.PreviewImport(ctx, nonApp, ws.ID, []byte("x")); errcode.CodeOf(pErr) != errcode.PermissionDenied {
			t.Fatalf("PreviewImport(%q) code = %q, want permission_denied", nonApp, errcode.CodeOf(pErr))
		}
		if _, aErr := memSvc.ApplyImport(ctx, nonApp, &MemoryApplyImportCommand{WorkspaceID: ws.ID, PreviewID: "p"}); errcode.CodeOf(aErr) != errcode.PermissionDenied {
			t.Fatalf("ApplyImport(%q) code = %q, want permission_denied", nonApp, errcode.CodeOf(aErr))
		}
	}
	for _, nonLocal := range []string{PrincipalSession, "guard"} {
		if _, eErr := memSvc.ExportMemory(ctx, nonLocal, ws.ID); errcode.CodeOf(eErr) != errcode.PermissionDenied {
			t.Fatalf("ExportMemory(%q) code = %q, want permission_denied", nonLocal, errcode.CodeOf(eErr))
		}
	}
	if _, eErr := memSvc.ExportMemory(ctx, string(KindCLI), ws.ID); eErr != nil {
		t.Fatalf("ExportMemory(cli) failed: %v", eErr)
	}

	oversized := bytes.Repeat([]byte("a"), MemoryExportImportMaxBytes+1)
	if _, sizeErr := memSvc.PreviewImport(ctx, string(KindApp), ws.ID, oversized); errcode.CodeOf(sizeErr) != errcode.MemoryImportInvalid {
		t.Fatalf("oversized PreviewImport code = %q, want %q", errcode.CodeOf(sizeErr), errcode.MemoryImportInvalid)
	}
	// Broken frontmatter fails with memory.import_invalid.
	if _, badErr := memSvc.PreviewImport(ctx, string(KindApp), ws.ID, []byte(strings.Repeat("not yaml\n", 3))); errcode.CodeOf(badErr) != errcode.MemoryImportInvalid {
		t.Fatalf("bad frontmatter code = %q, want %q", errcode.CodeOf(badErr), errcode.MemoryImportInvalid)
	}
}
