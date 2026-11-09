package vendorstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/sealing"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

func TestProtectedEventsAndVaultLock(t *testing.T) {
	t.Parallel()
	s, v, path := protectedStore(t)
	db := s.db
	_, turnErr := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t", State: "running"})
	requireNoError(t, turnErr)
	marker := []byte("planted-vendor-payload-marker-9a36")
	if _, err := s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: marker}); err != nil {
		t.Fatal(err)
	}
	var id string
	if err := db.QueryRowContext(t.Context(), "SELECT payload_id FROM vendor_events WHERE session_id='s' AND payload_id IS NOT NULL").Scan(&id); err != nil {
		t.Fatal(err)
	}
	plain, err := s.OpenPayload(t.Context(), "s", id)
	if err != nil || !bytes.Equal(plain, marker) {
		t.Fatalf("open: %v", err)
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, marker) {
		t.Fatal("plaintext in database")
	}
	v.Lock()
	if _, err := s.Append(t.Context(), "s", 3, &Event{Type: UsageObserved, Payload: marker}); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("lock: %v", err)
	}
	if _, err := s.Append(t.Context(), "s", 3, &Event{Type: QuotaObserved, State: StateWaiting, WaitingReason: WaitingVaultLocked}); err != nil {
		t.Fatal(err)
	}
	assertVaultWaiting(t, s)
	scanAllVendorColumns(t, s, marker)
	if err := s.PurgeSession(t.Context(), "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Append(t.Context(), "s", 4, &Event{Type: UsageObserved}); err == nil {
		t.Fatal("purged write accepted")
	}
}

func protectedStore(tb testing.TB) (*Store, *vault.Service, string) {
	tb.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(tb.TempDir(), "test.db")
	db, err := storage.Open(tb.Context(), path)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { requireNoError(tb, db.Close()) })
	err = storage.Migrate(tb.Context(), db, clk)
	if err != nil {
		tb.Fatal(err)
	}
	fixture, err := os.ReadFile("../vault/testdata/keyring-cheap.json")
	if err != nil {
		tb.Fatal(err)
	}
	var ring crypto.Keyring
	err = json.Unmarshal(fixture, &ring)
	if err != nil {
		tb.Fatal(err)
	}
	v, err := vault.NewService(tb.Context(), vault.NewMemoryKeyringStore(&ring), ids.NewSequence("v-"))
	if err != nil {
		tb.Fatal(err)
	}
	err = v.Unlock(tb.Context(), redact.NewSecret("master-pw"))
	if err != nil {
		tb.Fatal(err)
	}
	s, err := New(tb.Context(), db, v, clk, nil)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { requireNoError(tb, s.Close()) })
	if err := s.CreateSession(tb.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}); err != nil {
		tb.Fatal(err)
	}
	return s, v, path
}

const testToolShell = "shell"

func appendPlantedItems(t *testing.T, s *Store, marker, titleMarker, pathMarker, nameMarker []byte) {
	t.Helper()
	toolContent := append(append([]byte(`{"title":"`), titleMarker...), []byte(`","input":"run"}`)...)
	fileContent := append(append([]byte(`{"path":"`), pathMarker...), []byte(`","diff":"@@ -1 +1 @@"}`)...)
	subagentContent := append(append([]byte(`{"name":"`), nameMarker...), []byte(`","task":"subtask"}`)...)

	for seq, ev := range []*Event{
		{
			Type:    TranscriptItemRecorded,
			Payload: marker,
			Item: &Item{
				ID:      "i-tool",
				TurnID:  "t",
				Kind:    KindToolCall,
				Content: toolContent,
				Raw:     marker,
				Metadata: ItemMetadata{
					Tool:   testToolShell,
					Status: ToolPending,
				},
			},
		},
		{
			Type: TranscriptItemRecorded,
			Item: &Item{
				ID:      "i-file",
				TurnID:  "t",
				Kind:    KindFileChange,
				Content: fileContent,
				Metadata: ItemMetadata{
					ChangeKind:   "modify",
					LinesAdded:   5,
					LinesRemoved: 1,
				},
			},
		},
		{
			Type: TranscriptItemRecorded,
			Item: &Item{
				ID:      "i-subagent",
				TurnID:  "t",
				Kind:    KindSubagent,
				Content: subagentContent,
				Metadata: ItemMetadata{
					Status: "running",
				},
			},
		},
	} {
		_, err := s.Append(t.Context(), "s", int64(seq+2), ev)
		requireNoError(t, err)
	}
}

func assertPlantedContentSecrets(t *testing.T, s *Store, titleMarker, pathMarker, nameMarker []byte) {
	t.Helper()
	checks := []struct {
		id     string
		marker []byte
	}{
		{"i-tool", titleMarker},
		{"i-file", pathMarker},
		{"i-subagent", nameMarker},
	}
	for _, check := range checks {
		_, content, err := s.ReadItem(t.Context(), "s", check.id)
		requireNoError(t, err)
		if !bytes.Contains(content, check.marker) {
			t.Fatalf("planted marker missing from decrypted content of %s", check.id)
		}
	}
}

func TestTranscriptAndRawPayloadNeverAppearInVendorColumns(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)

	marker := []byte("vendor-confidential-marker-c83d")
	titleMarker := []byte("confidential-planted-title-c83d")
	pathMarker := []byte("confidential-planted-path-e41a")
	nameMarker := []byte("confidential-planted-subagent-92bf")

	appendPlantedItems(t, s, marker, titleMarker, pathMarker, nameMarker)

	for _, m := range [][]byte{marker, titleMarker, pathMarker, nameMarker} {
		scanAllVendorColumns(t, s, m)
	}
	assertPlantedContentSecrets(t, s, titleMarker, pathMarker, nameMarker)
}

func assertItemTruncationRecord(t *testing.T, s *Store, meta *ItemMetadata) {
	t.Helper()
	item, content, err := s.ReadItem(t.Context(), "s", "item-trunc")
	requireNoError(t, err)
	if item.Revision != 1 || string(content) != "truncated-search-output-head-tail" {
		t.Fatalf("unexpected record state: rev=%d, content=%s", item.Revision, string(content))
	}
	if diff := cmp.Diff(*meta, item.Metadata); diff != "" {
		t.Fatalf("recorded metadata diff (-want +got):\n%s", diff)
	}
	transcript, err := s.Transcript(t.Context(), "s", 0, 10)
	requireNoError(t, err)
	if len(transcript) != 1 {
		t.Fatalf("expected 1 transcript item, got %d", len(transcript))
	}
	if diff := cmp.Diff(*meta, transcript[0].Metadata); diff != "" {
		t.Fatalf("transcript page metadata diff (-want +got):\n%s", diff)
	}
}

func assertItemTruncationUpdate(t *testing.T, s *Store, meta *ItemMetadata) {
	t.Helper()
	item, content, err := s.ReadItem(t.Context(), "s", "item-trunc")
	requireNoError(t, err)
	if item.Revision != 2 || string(content) != "updated-truncated-head-tail" {
		t.Fatalf("unexpected update state: rev=%d, content=%s", item.Revision, string(content))
	}
	if diff := cmp.Diff(*meta, item.Metadata); diff != "" {
		t.Fatalf("updated metadata diff (-want +got):\n%s", diff)
	}
	raw, err := s.OpenPayload(t.Context(), "s", item.RawPayloadID)
	requireNoError(t, err)
	if string(raw) != "updated-raw-truncated-payload" {
		t.Fatalf("unexpected raw payload: %s", string(raw))
	}
}

func TestItemTruncationMetadataRoundTrips(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)

	recordMeta := ItemMetadata{
		Tool:                 "search",
		Status:               ToolPending,
		ContentTruncated:     true,
		RawTruncated:         false,
		OriginalContentBytes: 131072,
		OriginalRawBytes:     0,
	}
	_, err = s.Append(t.Context(), "s", 2, &Event{
		Type: TranscriptItemRecorded,
		Item: &Item{
			ID:       "item-trunc",
			TurnID:   "t",
			Kind:     KindToolCall,
			Metadata: recordMeta,
			Content:  []byte("truncated-search-output-head-tail"),
		},
	})
	requireNoError(t, err)
	assertItemTruncationRecord(t, s, &recordMeta)

	updatedMeta := ItemMetadata{
		Tool:                 "search",
		Status:               ToolSucceeded,
		ContentTruncated:     true,
		RawTruncated:         true,
		OriginalContentBytes: 131072,
		OriginalRawBytes:     262144,
	}
	_, err = s.Append(t.Context(), "s", 3, &Event{
		Type: TranscriptItemUpdated,
		Item: &Item{
			ID:       "item-trunc",
			TurnID:   "t",
			Kind:     KindToolCall,
			Revision: 1,
			Metadata: updatedMeta,
			Content:  []byte("updated-truncated-head-tail"),
			Raw:      []byte("updated-raw-truncated-payload"),
		},
	})
	requireNoError(t, err)
	assertItemTruncationUpdate(t, s, &updatedMeta)

	events, err := s.EventsSince(t.Context(), "s", 2, 10)
	requireNoError(t, err)
	if len(events) != 2 {
		t.Fatalf("expected 2 recorded events, got %d", len(events))
	}
	if diff := cmp.Diff(&recordMeta, events[0].Metadata.ItemMetadata); diff != "" {
		t.Fatalf("recorded event metadata diff (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff(&updatedMeta, events[1].Metadata.ItemMetadata); diff != "" {
		t.Fatalf("updated event metadata diff (-want +got):\n%s", diff)
	}
}

func TestItemMetadataRejectsNegativeOriginalBytes(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)

	_, err = s.Append(t.Context(), "s", 2, &Event{
		Type: TranscriptItemRecorded,
		Item: &Item{
			ID:     "i-bad-content",
			TurnID: "t",
			Kind:   KindToolCall,
			Metadata: ItemMetadata{
				Tool:                 testToolShell,
				OriginalContentBytes: -1,
			},
		},
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for negative original content bytes, got %v", err)
	}

	_, err = s.Append(t.Context(), "s", 2, &Event{
		Type: TranscriptItemRecorded,
		Item: &Item{
			ID:     "i-bad-raw",
			TurnID: "t",
			Kind:   KindToolCall,
			Metadata: ItemMetadata{
				Tool:             testToolShell,
				OriginalRawBytes: -1,
			},
		},
	})
	if errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for negative original raw bytes, got %v", err)
	}
}

func TestItemTruncationFlagsMatchOriginalSizes(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)

	cases := []struct {
		id   string
		name string
		meta ItemMetadata
	}{
		{
			id:   "i-mismatch-content-flag",
			name: "content flag true with zero bytes",
			meta: ItemMetadata{
				Tool:             testToolShell,
				ContentTruncated: true,
			},
		},
		{
			id:   "i-mismatch-content-size",
			name: "content flag false with positive bytes",
			meta: ItemMetadata{
				Tool:                 testToolShell,
				OriginalContentBytes: 100,
			},
		},
		{
			id:   "i-mismatch-raw-flag",
			name: "raw flag true with zero bytes",
			meta: ItemMetadata{
				Tool:         testToolShell,
				RawTruncated: true,
			},
		},
		{
			id:   "i-mismatch-raw-size",
			name: "raw flag false with positive bytes",
			meta: ItemMetadata{
				Tool:             testToolShell,
				OriginalRawBytes: 100,
			},
		},
	}
	for _, tc := range cases {
		_, err = s.Append(t.Context(), "s", 2, &Event{
			Type: TranscriptItemRecorded,
			Item: &Item{
				ID:       tc.id,
				TurnID:   "t",
				Kind:     KindToolCall,
				Metadata: tc.meta,
			},
		})
		if errcode.CodeOf(err) != errcode.InvalidArgument {
			t.Fatalf("%s: expected InvalidArgument, got %v", tc.name, err)
		}
	}
}

func scanVendorColumns(t *testing.T, s *Store, table string, marker []byte) {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), table)
	requireNoError(t, err)
	defer closeRows(t.Context(), rows)
	columns, err := rows.Columns()
	requireNoError(t, err)
	for rows.Next() {
		values := make([]any, len(columns))
		targets := make([]any, len(columns))
		for i := range values {
			targets[i] = &values[i]
		}
		requireNoError(t, rows.Scan(targets...))
		for _, value := range values {
			assertNoMarker(t, value, marker)
		}
	}
	requireNoError(t, rows.Err())
}

func TestPayloadAndSessionLimits(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	if _, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: make([]byte, sealing.MaxPayloadPlaintext+1)}); err == nil {
		t.Fatal("oversize payload accepted")
	}
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("small")})
	requireNoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) SELECT 'limit',session_id,kind,key_id,?,envelope FROM vendor_payloads LIMIT 1`, sealing.MaxSessionPayloadTotal-5)
	requireNoError(t, err)
	_, err = s.db.ExecContext(t.Context(), `UPDATE vendor_sessions SET payload_bytes=? WHERE session_id='s'`, sealing.MaxSessionPayloadTotal)
	requireNoError(t, err)
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: []byte("extra")}); err == nil {
		t.Fatal("session limit exceeded")
	}
}

func assertNoMarker(t *testing.T, value any, marker []byte) {
	t.Helper()
	var data []byte
	switch v := value.(type) {
	case string:
		data = []byte(v)
	case []byte:
		data = v
	}
	if bytes.Contains(data, marker) {
		t.Fatal("plaintext found in vendor column")
	}
}

func TestPurgeRollbackPreservesPayloadAndPermanentPurgeRevokesIt(t *testing.T) {
	t.Parallel()
	s, v, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("protected")})
	requireNoError(t, err)
	events, err := s.EventsSince(t.Context(), "s", 1, 10)
	requireNoError(t, err)
	s.SetCommitFault(ErrUnavailable)
	if err = s.PurgeSession(t.Context(), "s"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("purge fault not returned")
	}
	s.SetCommitFault(nil)
	assertPayloadTotal(t, s)
	_, err = s.OpenPayload(t.Context(), "s", events[0].PayloadID)
	requireNoError(t, err)
	requireNoError(t, s.PurgeSession(t.Context(), "s"))
	var remaining int
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT (SELECT COUNT(*) FROM vendor_sessions WHERE session_id='s') + (SELECT COUNT(*) FROM vendor_payloads WHERE session_id='s')`).Scan(&remaining))
	if remaining != 0 {
		t.Fatal("purge retained session or payload")
	}
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: []byte("x")}); !errors.Is(err, ErrPurged) {
		t.Fatal("purged session resurrected")
	}
	reopened, err := New(t.Context(), s.db, v, s.clock, nil)
	requireNoError(t, err)
	t.Cleanup(func() { requireNoError(t, reopened.Close()) })
	if _, err = reopened.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: []byte("x")}); !errors.Is(err, ErrPurged) {
		t.Fatalf("append after restart: %v", err)
	}
	err = reopened.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting})
	if !errors.Is(err, ErrPurged) {
		t.Fatalf("create after restart: %v", err)
	}
	if _, err = s.OpenPayload(t.Context(), "s", events[0].PayloadID); err == nil {
		t.Fatal("purged payload opened")
	}
}

func TestAmbiguousPurgeBarrierBlocksEveryWrite(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	s.cache.Purge("s")
	if _, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved}); !errors.Is(err, ErrPurged) {
		t.Fatal("metadata append bypassed purge barrier")
	}
	if _, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("protected")}); !errors.Is(err, ErrPurged) {
		t.Fatal("payload append bypassed purge barrier")
	}
	if err := s.UpdatePolicy(t.Context(), "s", 0, ApprovalAuto); !errors.Is(err, ErrPurged) {
		t.Fatal("policy bypassed purge barrier")
	}
	if err := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}); !errors.Is(err, ErrPurged) {
		t.Fatal("creation bypassed purge barrier")
	}
}

func scanAllVendorColumns(t *testing.T, s *Store, marker []byte) {
	t.Helper()
	for _, table := range []string{"SELECT * FROM vendor_sessions", "SELECT * FROM vendor_turns", "SELECT * FROM vendor_events", "SELECT * FROM vendor_transcript_items", "SELECT * FROM vendor_payloads", "SELECT * FROM vendor_session_keys", "SELECT * FROM vendor_purges"} {
		scanVendorColumns(t, s, table, marker)
	}
}

func assertVaultWaiting(t *testing.T, s *Store) {
	t.Helper()
	snapshot, err := s.Snapshot(t.Context(), "s")
	requireNoError(t, err)
	if snapshot.State != StateWaiting || snapshot.WaitingReason != WaitingVaultLocked {
		t.Fatal("vault waiting state not recorded")
	}
}
