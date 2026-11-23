package vendorstore

import (
	"bytes"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/sealing"
)

// Stage A storage evidence on the vendor_* tables (storage-security §10:
// S-E01, S-E04, S-E05, S-E09). Faults are real SQLite failures raised by
// test-only triggers, so the production write path is exercised unchanged.

type vendorCounts struct {
	events, payloads, items, keys, turns int
	lastSequence, payloadBytes           int64
}

func countVendor(t *testing.T, db *sql.DB, session string) vendorCounts {
	t.Helper()
	var c vendorCounts
	requireNoError(t, db.QueryRowContext(t.Context(), `SELECT
		(SELECT COUNT(*) FROM vendor_events WHERE session_id=?1),
		(SELECT COUNT(*) FROM vendor_payloads WHERE session_id=?1),
		(SELECT COUNT(*) FROM vendor_transcript_items WHERE session_id=?1),
		(SELECT COUNT(*) FROM vendor_session_keys WHERE session_id=?1),
		(SELECT COUNT(*) FROM vendor_turns WHERE session_id=?1),
		COALESCE((SELECT last_sequence FROM vendor_sessions WHERE session_id=?1),0),
		COALESCE((SELECT payload_bytes FROM vendor_sessions WHERE session_id=?1),0)`, session).
		Scan(&c.events, &c.payloads, &c.items, &c.keys, &c.turns, &c.lastSequence, &c.payloadBytes))
	return c
}

// requireNoOrphans checks the cross-table invariants a half-written
// transaction would break.
func requireNoOrphans(t *testing.T, db *sql.DB) {
	t.Helper()
	var orphans int
	requireNoError(t, db.QueryRowContext(t.Context(), `SELECT
		(SELECT COUNT(*) FROM vendor_events e WHERE e.payload_id IS NOT NULL
			AND NOT EXISTS (SELECT 1 FROM vendor_payloads p WHERE p.payload_id=e.payload_id))
		+ (SELECT COUNT(*) FROM vendor_payloads p WHERE NOT EXISTS (
			SELECT 1 FROM vendor_events e WHERE e.payload_id=p.payload_id)
			AND NOT EXISTS (SELECT 1 FROM vendor_transcript_items i
				WHERE i.payload_id=p.payload_id OR i.raw_payload_id=p.payload_id))
		+ (SELECT COUNT(*) FROM vendor_transcript_items i WHERE NOT EXISTS (
			SELECT 1 FROM vendor_events e WHERE e.session_id=i.session_id AND e.sequence=i.sequence))`).Scan(&orphans))
	if orphans != 0 {
		t.Fatalf("%d orphaned payload, event or projection rows", orphans)
	}
}

func execDDL(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), statement)
	requireNoError(t, err)
}

func itemEvent(id string) *Event {
	return &Event{Type: TranscriptItemRecorded, Item: &Item{
		ID: id, TurnID: "t", Kind: testAssistantKind, Content: []byte("evidence-content-" + id),
	}}
}

func TestStorageEvidenceSE01FaultBetweenEveryWriteStep(t *testing.T) {
	t.Parallel()
	steps := []struct {
		name  string
		arm   func(t *testing.T, s *Store)
		clear func(t *testing.T, s *Store)
	}{
		{"sequence advance", armTrigger("BEFORE UPDATE ON vendor_sessions WHEN NEW.last_sequence=3"), clearTrigger},
		{"payload insert", armTrigger("BEFORE INSERT ON vendor_payloads"), clearTrigger},
		{"event insert", armTrigger("BEFORE INSERT ON vendor_events WHEN NEW.sequence=3"), clearTrigger},
		{"projection insert", armTrigger("BEFORE INSERT ON vendor_transcript_items"), clearTrigger},
		{"commit", func(_ *testing.T, s *Store) { s.SetCommitFault(ErrUnavailable) }, func(_ *testing.T, s *Store) { s.SetCommitFault(nil) }},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			t.Parallel()
			s, v, _ := protectedStore(t)
			_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
			requireNoError(t, err)
			before := countVendor(t, s.db, "s")
			step.arm(t, s)
			if _, err = s.Append(t.Context(), "s", 2, itemEvent("x")); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("faulted append = %v, want ErrUnavailable", err)
			}
			step.clear(t, s)
			if after := countVendor(t, s.db, "s"); after != before {
				t.Fatalf("partial state after fault: before %+v after %+v", before, after)
			}
			requireNoOrphans(t, s.db)
			assertPayloadTotal(t, s)

			reopened, err := New(t.Context(), s.db, v, s.clock, nil)
			requireNoError(t, err)
			t.Cleanup(func() { requireNoError(t, reopened.Close()) })
			if _, err = reopened.Append(t.Context(), "s", 2, itemEvent("x")); err != nil {
				t.Fatalf("retry after fault: %v", err)
			}
			final := countVendor(t, s.db, "s")
			if final.events != before.events+1 || final.items != before.items+1 || final.payloads != before.payloads+1 || final.lastSequence != 3 {
				t.Fatalf("retry did not commit exactly one write: before %+v final %+v", before, final)
			}
			requireNoOrphans(t, s.db)
			assertPayloadTotal(t, reopened)
		})
	}
}

func TestStorageEvidenceSE01BatchFaultLeavesNoEvent(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	before := countVendor(t, s.db, "s")
	// The first event of the batch is fully written before the second fails.
	armTrigger("BEFORE INSERT ON vendor_events WHEN NEW.sequence=4")(t, s)
	batch := []*Event{itemEvent("a"), itemEvent("b")}
	if _, err = s.AppendBatch(t.Context(), "s", 2, batch); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("faulted batch = %v, want ErrUnavailable", err)
	}
	clearTrigger(t, s)
	if after := countVendor(t, s.db, "s"); after != before {
		t.Fatalf("partial batch: before %+v after %+v", before, after)
	}
	requireNoOrphans(t, s.db)
	if _, err = s.AppendBatch(t.Context(), "s", 2, batch); err != nil {
		t.Fatalf("batch retry: %v", err)
	}
	if final := countVendor(t, s.db, "s"); final.events != before.events+2 || final.items != before.items+2 {
		t.Fatalf("batch retry counts: %+v", final)
	}
}

const faultTriggerName = "zz_fault_injection"

func armTrigger(clause string) func(t *testing.T, s *Store) {
	return func(t *testing.T, s *Store) {
		t.Helper()
		execDDL(t, s.db, "CREATE TRIGGER "+faultTriggerName+" "+clause+" BEGIN SELECT RAISE(ABORT,'injected fault'); END")
	}
}

func clearTrigger(t *testing.T, s *Store) {
	t.Helper()
	execDDL(t, s.db, "DROP TRIGGER "+faultTriggerName)
}

// twoSessionStore returns a store holding sessions "s" and "o", each with a
// turn, an event payload and a transcript item payload.
func twoSessionStore(t *testing.T) (*Store, string) {
	t.Helper()
	s, _, path := protectedStore(t)
	requireNoError(t, s.CreateSession(t.Context(), &Session{ID: "o", Vendor: "v", AccountID: "a", Role: "r", State: StateStarting}))
	for _, id := range []string{"s", "o"} {
		_, err := s.Append(t.Context(), id, 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
		requireNoError(t, err)
		_, err = s.Append(t.Context(), id, 2, &Event{Type: UsageObserved, Payload: []byte("usage-" + id)})
		requireNoError(t, err)
		_, err = s.Append(t.Context(), id, 3, itemEvent("i-"+id))
		requireNoError(t, err)
	}
	return s, path
}

func requireTriggerBlocked(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.ExecContext(t.Context(), statement)
	if err == nil {
		t.Fatalf("statement outside a purge succeeded: %s", statement)
	}
	if !strings.Contains(err.Error(), "append-only") || !strings.Contains(err.Error(), "constraint failed") {
		t.Fatalf("not an append-only constraint failure: %v", err)
	}
}

func TestStorageEvidenceSE04TriggersBlockMutationOutsidePurge(t *testing.T) {
	t.Parallel()
	s, _ := twoSessionStore(t)
	before := countVendor(t, s.db, "s")
	for _, statement := range []string{
		`UPDATE vendor_events SET event_type='x' WHERE session_id='s' AND sequence=1`,
		`DELETE FROM vendor_events WHERE session_id='s' AND sequence=1`,
		`DELETE FROM vendor_events`,
		`UPDATE vendor_payloads SET size=size+1 WHERE session_id='s'`,
		`DELETE FROM vendor_payloads WHERE session_id='s'`,
		`DELETE FROM vendor_payloads`,
	} {
		requireTriggerBlocked(t, s.db, statement)
	}
	if after := countVendor(t, s.db, "s"); after != before {
		t.Fatalf("blocked statements changed rows: before %+v after %+v", before, after)
	}
	requireNoOrphans(t, s.db)
}

func TestStorageEvidenceSE04PurgeRemovesExactlyOneSession(t *testing.T) {
	t.Parallel()
	s, _ := twoSessionStore(t)
	other := countVendor(t, s.db, "o")
	if other.events == 0 || other.payloads == 0 || other.items == 0 || other.keys == 0 || other.turns == 0 {
		t.Fatalf("fixture incomplete: %+v", other)
	}
	var otherPayload string
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT payload_id FROM vendor_events WHERE session_id='o' AND payload_id IS NOT NULL`).Scan(&otherPayload))

	requireNoError(t, s.PurgeSession(t.Context(), "s"))

	if gone := countVendor(t, s.db, "s"); gone != (vendorCounts{}) {
		t.Fatalf("purged session left rows: %+v", gone)
	}
	if kept := countVendor(t, s.db, "o"); kept != other {
		t.Fatalf("purge touched another session: before %+v after %+v", other, kept)
	}
	var purges int
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM vendor_purges WHERE session_id='s'`).Scan(&purges))
	if purges != 1 {
		t.Fatalf("purge markers = %d, want 1", purges)
	}
	if _, err := s.OpenPayload(t.Context(), "o", otherPayload); err != nil {
		t.Fatalf("other session payload unreadable after purge: %v", err)
	}
	requireNoOrphans(t, s.db)
}

func TestStorageEvidenceSE05PurgedWrappedKeyAbsentFromDatabaseFiles(t *testing.T) {
	t.Parallel()
	s, path := twoSessionStore(t)
	var purgedKey, keptKey []byte
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT wrapped_key FROM vendor_session_keys WHERE session_id='s'`).Scan(&purgedKey))
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT wrapped_key FROM vendor_session_keys WHERE session_id='o'`).Scan(&keptKey))
	var payloadID string
	requireNoError(t, s.db.QueryRowContext(t.Context(), `SELECT payload_id FROM vendor_events WHERE session_id='s' AND payload_id IS NOT NULL`).Scan(&payloadID))
	if len(purgedKey) < 16 || bytes.Equal(purgedKey, keptKey) {
		t.Fatal("fixture keys unusable")
	}
	// Control: the scan finds both keys before the purge, so absence later is meaningful.
	before := readDatabaseFiles(t, path)
	if !bytes.Contains(before, purgedKey) || !bytes.Contains(before, keptKey) {
		t.Fatal("wrapped keys not found in database file before purge; scan is not trustworthy")
	}

	requireNoError(t, s.PurgeSession(t.Context(), "s"))
	// Journal mode is DELETE so there is no WAL; the checkpoint is a no-op
	// that keeps this test correct if the mode ever changes.
	_, err := s.db.ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	requireNoError(t, err)

	after := readDatabaseFiles(t, path)
	if bytes.Contains(after, purgedKey) {
		t.Fatal("purged session wrapped key bytes remain in the database files")
	}
	if !bytes.Contains(after, keptKey) {
		t.Fatal("other session key lost: purge removed more than one session")
	}
	if _, err = s.OpenPayload(t.Context(), "s", payloadID); err == nil {
		t.Fatal("purged payload opened while the vault is unlocked")
	}
}

// readDatabaseFiles returns the database file plus any WAL and journal
// sidecar, concatenated.
func readDatabaseFiles(t *testing.T, path string) []byte {
	t.Helper()
	var all []byte
	for _, suffix := range []string{"", "-wal", "-journal"} {
		data, err := os.ReadFile(filepath.Clean(path + suffix))
		if errors.Is(err, os.ErrNotExist) && suffix != "" {
			continue
		}
		requireNoError(t, err)
		all = append(all, data...)
	}
	return all
}

func TestStorageEvidenceSE09PayloadLimitFailsTyped(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	at := bytes.Repeat([]byte{'a'}, sealing.MaxPayloadPlaintext)
	over := bytes.Repeat([]byte{'a'}, sealing.MaxPayloadPlaintext+1)
	events := map[string]*Event{
		"event payload": {Type: UsageObserved, Payload: over},
		"item content":  {Type: TranscriptItemRecorded, Item: &Item{ID: "c", TurnID: "t", Kind: testAssistantKind, Content: over}},
		"item raw":      {Type: TranscriptItemRecorded, Item: &Item{ID: "r", TurnID: "t", Kind: testAssistantKind, Raw: over}},
	}
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: VendorTurnStarted, TurnID: "t"})
	requireNoError(t, err)
	before := countVendor(t, s.db, "s")
	for name, event := range events {
		_, err = s.Append(t.Context(), "s", 2, event)
		if code := errcode.CodeOf(err); code != errcode.InvalidArgument {
			t.Fatalf("%s over 1 MiB: code %q, want %q (%v)", name, code, errcode.InvalidArgument, err)
		}
	}
	if after := countVendor(t, s.db, "s"); after != before {
		t.Fatalf("rejected payloads changed state: before %+v after %+v", before, after)
	}
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: at}); err != nil {
		t.Fatalf("payload of exactly 1 MiB rejected: %v", err)
	}
}

func TestStorageEvidenceSE09SessionLimitFailsTyped(t *testing.T) {
	t.Parallel()
	s, _, _ := protectedStore(t)
	_, err := s.Append(t.Context(), "s", 1, &Event{Type: UsageObserved, Payload: []byte("a")})
	requireNoError(t, err)
	// Leave exactly one byte of the 1 GiB budget without writing a gigabyte.
	_, err = s.db.ExecContext(t.Context(), `UPDATE vendor_sessions SET payload_bytes=? WHERE session_id='s'`, sealing.MaxSessionPayloadTotal-1)
	requireNoError(t, err)
	before := countVendor(t, s.db, "s")
	_, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: []byte("xy")})
	if code := errcode.CodeOf(err); code != errcode.FailedPrecondition {
		t.Fatalf("session over 1 GiB: code %q, want %q (%v)", code, errcode.FailedPrecondition, err)
	}
	if after := countVendor(t, s.db, "s"); after != before {
		t.Fatalf("rejected append changed state: before %+v after %+v", before, after)
	}
	if _, err = s.Append(t.Context(), "s", 2, &Event{Type: UsageObserved, Payload: []byte("x")}); err != nil {
		t.Fatalf("append that exactly fills the budget rejected: %v", err)
	}
	_, err = s.Append(t.Context(), "s", 3, &Event{Type: UsageObserved, Payload: []byte("x")})
	if code := errcode.CodeOf(err); code != errcode.FailedPrecondition {
		t.Fatalf("full session: code %q, want %q (%v)", code, errcode.FailedPrecondition, err)
	}
}
