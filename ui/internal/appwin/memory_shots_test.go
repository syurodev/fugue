package appwin

import (
	"encoding/json"
	"image"
	"testing"
	"time"

	"gioui.org/gpu/headless"

	"github.com/syurodev/fugue/pkg/client"

	"github.com/syurodev/fugue/ui/internal/glwin"
	"github.com/syurodev/fugue/ui/internal/hostconn"
	"github.com/syurodev/fugue/ui/internal/shottest"
	"github.com/syurodev/fugue/ui/internal/vault"
)

const (
	shotMemoryList       = "memory-list"
	shotMemoryDetail     = "memory-detail"
	shotMemoryImportDiff = "memory-import-diff"
)

func memoryShotSpecs() []shotSpec {
	scenes := []string{shotMemoryList, shotMemoryDetail, shotMemoryImportDiff}
	modes := []struct {
		label   string
		light   bool
		uiScale float32
	}{
		{"light-ui100", true, 1.0},
		{"dark-ui100", false, 1.0},
		{"light-ui125", true, 1.25},
		{"dark-ui125", false, 1.25},
	}
	specs := make([]shotSpec, 0, len(scenes)*len(modes))
	for _, scene := range scenes {
		for _, m := range modes {
			specs = append(specs, shotSpec{
				name:    scene + "-" + m.label,
				scene:   scene,
				light:   m.light,
				uiScale: m.uiScale,
			})
		}
	}
	return specs
}

func sampleMemoryEntries() []client.MemoryEntry {
	t0 := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	return []client.MemoryEntry{
		{
			ID:          "mem-1",
			WorkspaceID: testSettingsWSID,
			Kind:        client.MemoryKindFact,
			Label:       "vault-argon2-params",
			Summary:     "Host vault uses Argon2id and XChaCha20-Poly1305.",
			Body:        "Master Password unlocks the host vault via Argon2id key derivation and XChaCha20-Poly1305.",
			Keywords:    []string{"vault", "argon2id", "encryption"},
			AnchorStatuses: []client.MemoryAnchorStatus{
				{RootName: "main", RelPath: "internal/vault/vault.go", ContentHash: "abc123", Freshness: client.MemoryFreshnessFresh},
			},
			Author:    client.MemoryAuthorUser,
			Verified:  true,
			Validity:  client.MemoryValidityValid,
			Revision:  3,
			UpdatedAt: t0,
			Freshness: client.MemoryFreshnessFresh,
		},
		{
			ID:          "mem-2",
			WorkspaceID: testSettingsWSID,
			Kind:        client.MemoryKindConvention,
			Label:       "ui-test-fuguegate",
			Summary:     "Wrap ui tests in fuguegate exclusive, never bare go test.",
			Body:        "UI tests require go run ./cmd/fuguegate exclusive -- go -C ui test ...",
			Keywords:    []string{"tests", "fuguegate"},
			AnchorStatuses: []client.MemoryAnchorStatus{
				{RootName: "main", RelPath: "AGENTS.md", ContentHash: "def456", Freshness: client.MemoryFreshnessStale},
			},
			Author:    client.MemoryAuthorAgent,
			Verified:  false,
			Validity:  client.MemoryValidityValid,
			Revision:  1,
			UpdatedAt: t0,
			Freshness: client.MemoryFreshnessStale,
		},
	}
}

func buildMemoryShotScreens(t *testing.T, scene string) *screens {
	t.Helper()
	entries := sampleMemoryEntries()
	fc := &fakeAppMemoryClient{
		entries: entries,
		history: []client.MemoryHistoryEntry{
			{Revision: 1, Op: client.MemoryOpAdd, Author: client.MemoryAuthorAgent, Summary: "Initial note", WrittenAt: entries[0].UpdatedAt.Add(-time.Hour)},
			{Revision: 2, Op: client.MemoryOpVerify, Author: client.MemoryAuthorUser, Summary: "Verified by user", WrittenAt: entries[0].UpdatedAt.Add(-30 * time.Minute)},
			{Revision: 3, Op: client.MemoryOpUpdate, Author: client.MemoryAuthorUser, Summary: entries[0].Summary, WrittenAt: entries[0].UpdatedAt},
		},
	}
	sc := buildTestMemoryScreens(t, fc)
	sc.applyVaultDelivery(mustVaultSnapshot(t, vault.Status{State: client.VaultStateUnlocked, Revision: 1, Incarnation: "inc-mem-shot"}))
	snap, err := json.Marshal(client.MemoryTopicSnapshot{Unverified: map[string]int{testSettingsWSID: 1}})
	if err != nil {
		t.Fatal(err)
	}
	sc.applyMemoryDelivery(hostconn.Update{Topic: topicMemory, Snapshot: &client.Snapshot{State: snap}})

	mv := sc.memoryViewFor(testSettingsWSID)
	mv.loaded = true
	mv.entries = entries

	switch scene {
	case shotMemoryDetail:
		e := entries[1]
		mv.selected = &e
		mv.history = fc.history
		mv.mode = memModeDetail
	case shotMemoryImportDiff:
		local := entries[0]
		imported := entries[0]
		imported.Summary = "Host vault uses Argon2id (64 MiB) and XChaCha20-Poly1305."
		imported.Revision = 5
		mv.loadPreview(&client.MemoryImportPreview{
			PreviewID:     "prev-shot-1",
			WorkspaceName: "alpha",
			Items: []client.MemoryImportPreviewItem{
				{
					ItemID:        "item-1",
					EntryID:       local.ID,
					Label:         local.Label,
					Class:         client.MemoryImportClassConflict,
					DefaultChoice: client.MemoryImportChoiceTakeImported,
					Local:         &local,
					Imported:      &imported,
				},
				{
					ItemID:        "item-2",
					EntryID:       "mem-3",
					Label:         "new-convention",
					Class:         client.MemoryImportClassAdd,
					DefaultChoice: client.MemoryImportChoiceTakeImported,
				},
			},
		}, []byte("raw"), "alpha-memory.md")
	}

	openVisibleSlot(sc, SlotRef{Kind: KindMemory, ID: testSettingsWSID})
	return sc
}

func renderMemoryShot(t *testing.T, win *headless.Window, spec shotSpec) *image.RGBA {
	t.Helper()
	return renderAppShot(t, win, spec, buildMemoryShotScreens(t, spec.scene))
}

func TestMemorySlotShots(t *testing.T) {
	t.Parallel()
	win, err := glwin.Open(shotWindowW, shotWindowH)
	if err != nil {
		t.Fatalf("open headless window: %v", err)
	}
	rendered := map[string]*image.RGBA{}
	for _, spec := range memoryShotSpecs() {
		img := renderMemoryShot(t, win, spec)
		shottest.Compare(t, spec.name, img)
		if isUniformRGBA(img) {
			t.Fatalf("memory shot %s is blank/uniform", spec.name)
		}
		rendered[spec.name] = img
	}
	for _, scene := range []string{shotMemoryList, shotMemoryDetail, shotMemoryImportDiff} {
		if !imagesDifferRGBA(rendered[scene+"-light-ui100"], rendered[scene+"-dark-ui100"]) {
			t.Fatalf("%s light and dark shots are identical", scene)
		}
	}
}
