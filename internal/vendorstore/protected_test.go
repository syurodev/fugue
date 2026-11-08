package vendorstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
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
	marker := []byte("planted-vendor-payload-marker-9a36")
	if _, operationErr5 := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: marker}); operationErr5 != nil {
		t.Fatal(operationErr5)
	}
	var id string
	if operationErr6 := db.QueryRowContext(t.Context(), "SELECT payload_id FROM vendor_events WHERE session_id='s' AND payload_id IS NOT NULL").Scan(&id); operationErr6 != nil {
		t.Fatal(operationErr6)
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
	if _, operationErr7 := s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: marker}); !errors.Is(operationErr7, vault.ErrLocked) {
		t.Fatalf("lock: %v", operationErr7)
	}
	if _, operationErr8 := s.Append(t.Context(), "s", 2, &Event{Type: QuotaObserved, State: StateWaiting, WaitingReason: "vault_locked"}); operationErr8 != nil {
		t.Fatal(operationErr8)
	}
	if operationErr9 := s.PurgeSession(t.Context(), "s"); operationErr9 != nil {
		t.Fatal(operationErr9)
	}
	if _, operationErr10 := s.Append(t.Context(), "s", 3, &Event{Type: UsageObserved}); operationErr10 == nil {
		t.Fatal("purged write accepted")
	}
}

func protectedStore(t *testing.T) (*Store, *vault.Service, string) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC))
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := storage.Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, db.Close()) })
	if operationErr1 := storage.Migrate(t.Context(), db, clk); operationErr1 != nil {
		t.Fatal(operationErr1)
	}
	fixture, err := os.ReadFile("../vault/testdata/keyring-cheap.json")
	if err != nil {
		t.Fatal(err)
	}
	var ring crypto.Keyring
	if operationErr2 := json.Unmarshal(fixture, &ring); operationErr2 != nil {
		t.Fatal(operationErr2)
	}
	v, err := vault.NewService(t.Context(), vault.NewMemoryKeyringStore(&ring), ids.NewSequence("v-"))
	if err != nil {
		t.Fatal(err)
	}
	if operationErr3 := v.Unlock(t.Context(), redact.NewSecret("master-pw")); operationErr3 != nil {
		t.Fatal(operationErr3)
	}
	s, err := New(t.Context(), db, v, clk, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { requireNoError(t, s.Close()) })
	if operationErr4 := s.CreateSession(t.Context(), &Session{ID: "s", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}); operationErr4 != nil {
		t.Fatal(operationErr4)
	}
	return s, v, path
}

func TestTranscriptAndRawPayloadNeverAppearInVendorColumns(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	marker := []byte("vendor-confidential-marker-c83d")
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: TranscriptItemRecorded, Payload: marker, Item: &Item{ID: "i", TurnID: "t", Kind: "tool_call", Content: marker, Raw: marker}})
	requireNoError(t, err)
	for _, table := range []string{"SELECT * FROM vendor_sessions", "SELECT * FROM vendor_turns", "SELECT * FROM vendor_events", "SELECT * FROM vendor_transcript_items", "SELECT * FROM vendor_payloads", "SELECT * FROM vendor_session_keys", "SELECT * FROM vendor_purges"} {
		scanVendorColumns(t, s, table, marker)
	}
}

func scanVendorColumns(t *testing.T, s *Store, table string, marker []byte) {
	t.Helper()
	rows, err := s.db.QueryContext(t.Context(), table)
	requireNoError(t, err)
	defer closeRows(rows)
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
	_, err = s.db.ExecContext(t.Context(), `INSERT INTO vendor_payloads(payload_id,session_id,kind,key_id,size,envelope) SELECT 'limit',session_id,kind,key_id,?,envelope FROM vendor_payloads LIMIT 1`, sealing.MaxSessionPayloadTotal)
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
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("protected")})
	requireNoError(t, err)
	events, err := s.EventsSince(t.Context(), "s", 1, 10)
	requireNoError(t, err)
	s.SetCommitFault(ErrUnavailable)
	if err = s.PurgeSession(t.Context(), "s"); !errors.Is(err, ErrUnavailable) {
		t.Fatal("purge fault not returned")
	}
	s.SetCommitFault(nil)
	_, err = s.OpenPayload(t.Context(), "s", events[0].PayloadID)
	requireNoError(t, err)
	requireNoError(t, s.PurgeSession(t.Context(), "s"))
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
	if _, err := s.Recover(t.Context()); !errors.Is(err, ErrPurged) {
		t.Fatal("recovery bypassed purge barrier")
	}
}
