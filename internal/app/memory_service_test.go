package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/workspace"
	"github.com/syurodev/fugue/internal/workspacememory"
)

func TestMemoryStorePortWiringAndEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC))
	fugueHome := filepath.Join(t.TempDir(), "Fugue")
	deps := testHostDeps(t, t.TempDir(), clk, newScriptedTransport())
	deps.WorkspaceEnv = config.MapEnv{config.EnvFugueHome: fugueHome}
	hp, err := StartHost(ctx, deps)
	if err != nil {
		t.Fatalf("start host: %v", err)
	}
	closeAppStateHost(t, hp)

	memStore, wsID, rootDir, anchorPath := setupMemoryWiringTest(t, ctx, hp, fugueHome)

	sub, err := hp.rt.hub.Subscribe(ctx, SubscribeParams{Principal: testAppStateOwner, Topic: TopicMemory})
	if err != nil {
		t.Fatalf("Subscribe TopicMemory: %v", err)
	}
	defer sub.Close()

	assertMemoryRejectsSecret(t, ctx, memStore, wsID)
	assertMemoryRejectsEscapeAnchor(t, ctx, memStore, wsID)
	entry := assertMemoryAnchoredWriteAndEvent(t, ctx, memStore, sub, wsID)
	assertMemoryStaleAfterEditAndDelete(t, ctx, memStore, wsID, entry.ID, anchorPath)
	assertMemoryBinaryLargeAndNewlineAnchors(t, ctx, memStore, wsID, rootDir)
}

func setupMemoryWiringTest(
	t *testing.T,
	ctx context.Context,
	hp *HostProcess,
	fugueHome string,
) (memStore *workspacememory.Store, wsID, rootDir, anchorPath string) {
	t.Helper()
	memStore, err := hp.rt.MemoryStore()
	if err != nil {
		t.Fatalf("MemoryStore: %v", err)
	}
	wsSvc := hp.rt.workspaces
	if wsSvc == nil {
		t.Fatal("workspaces is nil")
	}
	rootDir = t.TempDir()
	anchorPath = filepath.Join(rootDir, "README.md")
	if writeErr := os.WriteFile(anchorPath, []byte("initial anchor file content"), 0o600); writeErr != nil {
		t.Fatalf("WriteFile: %v", writeErr)
	}
	ws, err := wsSvc.Create(ctx, testAppStateOwner, "memory-ws")
	if err != nil {
		t.Fatalf("Create workspace: %v", err)
	}
	wsDir := wsSvc.domain.Paths().WorkspaceDir(ws.ID)
	wantPrefix := filepath.Join(fugueHome, "workspaces") + string(filepath.Separator)
	if !strings.HasPrefix(wsDir, wantPrefix) {
		t.Fatalf("workspace dir %q is not under temp Fugue home %q", wsDir, wantPrefix)
	}
	if _, statErr := os.Stat(wsDir); statErr != nil {
		t.Fatalf("workspace dir %q missing: %v", wsDir, statErr)
	}
	if _, addErr := wsSvc.AddRoot(ctx, testAppStateOwner, ws.ID, teamTestRootName, rootDir, "rw", ws.ConfigHash); addErr != nil {
		t.Fatalf("AddRoot: %v", addErr)
	}
	return memStore, ws.ID, rootDir, anchorPath
}

func assertMemoryRejectsSecret(t *testing.T, ctx context.Context, memStore *workspacememory.Store, wsID string) {
	t.Helper()
	fakeToken := "gh" + "p_" + strings.Repeat("b2", 18)
	_, secErr := memStore.Add(ctx, &workspacememory.AddInput{
		WorkspaceID: wsID,
		Kind:        workspacememory.KindFact,
		Label:       "secret-wire-test",
		Summary:     "Secret should be rejected before storage.",
		Body:        "Do not store token " + fakeToken + " in workspace memory.",
		Author:      workspacememory.AuthorAgent,
	})
	if got := errcode.CodeOf(secErr); got != errcode.MemoryContainsSecret {
		t.Fatalf("secret Add code = %q, want memory.contains_secret", got)
	}
	if strings.Contains(secErr.Error(), fakeToken) {
		t.Fatal("secret leaked into error")
	}
}

func assertMemoryRejectsEscapeAnchor(t *testing.T, ctx context.Context, memStore *workspacememory.Store, wsID string) {
	t.Helper()
	_, escErr := memStore.Add(ctx, &workspacememory.AddInput{
		WorkspaceID: wsID,
		Kind:        workspacememory.KindFact,
		Label:       "escape-anchor",
		Summary:     "Anchor outside root must be rejected.",
		Body:        "This entry attempts to reference a file outside the workspace root.",
		Anchors:     []workspacememory.AnchorInput{{RootName: teamTestRootName, RelPath: "../outside.txt"}},
		Author:      workspacememory.AuthorUser,
	})
	if errcode.CodeOf(escErr) != errcode.WorkspacePathOutsideRoot {
		t.Fatalf("escape anchor code = %q, want %q (err=%v)", errcode.CodeOf(escErr), errcode.WorkspacePathOutsideRoot, escErr)
	}
}

func assertMemoryAnchoredWriteAndEvent(
	t *testing.T,
	ctx context.Context,
	memStore *workspacememory.Store,
	sub *Subscription,
	wsID string,
) workspacememory.Entry {
	t.Helper()
	entry, err := memStore.Add(ctx, &workspacememory.AddInput{
		WorkspaceID: wsID,
		Kind:        workspacememory.KindFact,
		Label:       "readme-anchor",
		Summary:     "Workspace README documents the build steps.",
		Body:        "The README file contains build instructions for contributors.",
		Anchors:     []workspacememory.AnchorInput{{RootName: teamTestRootName, RelPath: "README.md"}},
		Author:      workspacememory.AuthorAgent,
	})
	if err != nil {
		t.Fatalf("Add anchored: %v", err)
	}
	if entry.Freshness != workspacememory.FreshnessFresh {
		t.Fatalf("initial freshness = %q, want fresh", entry.Freshness)
	}
	evCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	ev, ok := sub.Next(evCtx)
	if !ok {
		t.Fatal("expected MemoryEntryWritten event on TopicMemory")
	}
	if ev.Type != EventMemoryEntryWritten {
		t.Fatalf("event type = %q, want %q", ev.Type, EventMemoryEntryWritten)
	}
	var payload workspacememory.WrittenEvent
	if uErr := json.Unmarshal(ev.Payload, &payload); uErr != nil {
		t.Fatalf("Unmarshal event payload: %v", uErr)
	}
	if payload.EntryID != entry.ID || payload.Revision != 1 || payload.Author != workspacememory.AuthorAgent || payload.Op != workspacememory.OpAdd {
		t.Fatalf("event payload = %+v", payload)
	}
	rawPayload := string(ev.Payload)
	if strings.Contains(rawPayload, entry.Summary) || strings.Contains(rawPayload, entry.Body) {
		t.Fatalf("event payload leaked content: %s", rawPayload)
	}
	return entry
}

func assertMemoryStaleAfterEditAndDelete(
	t *testing.T,
	ctx context.Context,
	memStore *workspacememory.Store,
	wsID, entryID, anchorPath string,
) {
	t.Helper()
	if writeErr := os.WriteFile(anchorPath, []byte("updated anchor file content"), 0o600); writeErr != nil {
		t.Fatalf("WriteFile update: %v", writeErr)
	}
	edited, err := memStore.GetByID(ctx, wsID, entryID)
	if err != nil {
		t.Fatalf("GetByID after edit: %v", err)
	}
	if edited.Freshness != workspacememory.FreshnessStale {
		t.Fatalf("freshness after edit = %q, want stale", edited.Freshness)
	}
	if rmErr := os.Remove(anchorPath); rmErr != nil {
		t.Fatalf("Remove anchor: %v", rmErr)
	}
	deleted, err := memStore.GetByID(ctx, wsID, entryID)
	if err != nil {
		t.Fatalf("GetByID after remove: %v", err)
	}
	if deleted.Freshness != workspacememory.FreshnessStale {
		t.Fatalf("freshness after delete = %q, want stale", deleted.Freshness)
	}
}

func assertMemoryBinaryLargeAndNewlineAnchors(
	t *testing.T,
	ctx context.Context,
	memStore *workspacememory.Store,
	wsID, rootDir string,
) {
	t.Helper()
	cases := []struct {
		label string
		file  string
		data  []byte
	}{
		{"binary-anchor", "blob.bin", []byte{0x00, 0xff, 0x80, 'o', 'k'}},
		{"large-anchor", "large.bin", bytes.Repeat([]byte("Z"), workspace.FileReadMaxBytes+2048)},
		{"newline-anchor", "nl.txt", []byte("hello\n")},
	}
	for _, tc := range cases {
		if err := os.WriteFile(filepath.Join(rootDir, tc.file), tc.data, 0o600); err != nil {
			t.Fatalf("write %s: %v", tc.file, err)
		}
		entry, err := memStore.Add(ctx, &workspacememory.AddInput{
			WorkspaceID: wsID,
			Kind:        workspacememory.KindFact,
			Label:       tc.label,
			Summary:     "Entry anchored to raw bytes file.",
			Body:        "This entry anchors a file hashed from exact raw bytes.",
			Anchors:     []workspacememory.AnchorInput{{RootName: teamTestRootName, RelPath: tc.file}},
			Author:      workspacememory.AuthorUser,
		})
		if err != nil {
			t.Fatalf("Add %s: %v", tc.label, err)
		}
		sum := sha256.Sum256(tc.data)
		wantHash := hex.EncodeToString(sum[:])
		if len(entry.Anchors) != 1 || entry.Anchors[0].ContentHash != wantHash {
			t.Fatalf("%s hash = %+v, want %q", tc.label, entry.Anchors, wantHash)
		}
		if entry.Freshness != workspacememory.FreshnessFresh {
			t.Fatalf("%s freshness = %q, want fresh", tc.label, entry.Freshness)
		}
	}
	if err := os.WriteFile(filepath.Join(rootDir, "nl.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("rewrite nl.txt: %v", err)
	}
	got, err := memStore.GetByLabel(ctx, wsID, "newline-anchor")
	if err != nil {
		t.Fatalf("GetByLabel newline-anchor: %v", err)
	}
	if got.Freshness != workspacememory.FreshnessStale {
		t.Fatalf("newline-anchor after removing trailing newline = %q, want stale", got.Freshness)
	}
}
