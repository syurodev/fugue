package execstore

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/syurodev/fugue/internal/storage"
)

var errSimulatedPurgeCrash = errors.New("simulated-purge-crash")

func readDatabaseFile(t *testing.T, dataDir string) []byte {
	t.Helper()
	cleanPath := filepath.Clean(storage.DBPath(dataDir))
	raw, err := os.ReadFile(cleanPath)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func verifyKeysPresentInDB(t *testing.T, rawDB []byte, k1, k2 *SealedSessionKey) {
	t.Helper()
	if !bytes.Contains(rawDB, k1.WrappedKey) {
		t.Fatal("expected s1 wrapped key bytes to be present in database file before purge")
	}
	if !bytes.Contains(rawDB, k2.WrappedKey) {
		t.Fatal("expected s2 wrapped key bytes to be present in database file before purge")
	}
}

func verifyPostPurgeKeysInDB(t *testing.T, rawDB []byte, k1, k2 *SealedSessionKey) {
	t.Helper()
	if bytes.Contains(rawDB, k1.WrappedKey) {
		t.Fatal("crypto-shred failed: s1 wrapped key bytes are still present in database file after purge!")
	}
	if !bytes.Contains(rawDB, k2.WrappedKey) {
		t.Fatal("s2 wrapped key bytes were erroneously removed!")
	}
}

func TestPurgeSessionCryptoShredAndDirectDBByteInspection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, dataDir, _ := newVaultAndStore(t)

	s1 := "sess-purge-1"
	s2 := "sess-purge-2"

	// Create and commit payloads for both sessions
	p1, k1, err := s.SealPayload(ctx, s1, "pl-1", "tool_result", []byte("s1-payload-secret"))
	if err != nil {
		t.Fatal(err)
	}
	p2, k2, err := s.SealPayload(ctx, s2, "pl-2", "tool_result", []byte("s2-payload-secret"))
	if err != nil {
		t.Fatal(err)
	}

	req1 := makeAcceptedReq(s, s1, "run-p1", "cmd-p1", "pl-1", []byte("s1-payload-secret"), []SealedPayload{p1}, k1)
	appendPersisted(ctx, t, s, req1)

	req2 := makeAcceptedReq(s, s2, "run-p2", "cmd-p2", "pl-2", []byte("s2-payload-secret"), []SealedPayload{p2}, k2)
	appendPersisted(ctx, t, s, req2)

	rawDBBefore := readDatabaseFile(t, dataDir)
	verifyKeysPresentInDB(t, rawDBBefore, k1, k2)

	if purgeErr := s.PurgeSession(ctx, s1); purgeErr != nil {
		t.Fatalf("PurgeSession failed: %v", purgeErr)
	}

	rawDBAfter := readDatabaseFile(t, dataDir)
	verifyPostPurgeKeysInDB(t, rawDBAfter, k1, k2)

	// S1 payload cannot be opened even with vault unlocked
	if _, openErr := s.OpenPayload(ctx, s1, "pl-1"); openErr == nil {
		t.Fatal("expected OpenPayload on purged session to fail")
	}

	// S2 payload opens successfully
	opened2, openErr := s.OpenPayload(ctx, s2, "pl-2")
	if openErr != nil || !bytes.Equal(opened2, []byte("s2-payload-secret")) {
		t.Fatalf("s2 open mismatch: %v", openErr)
	}
}

func TestPurgeSessionIsolationAndSafeRetry(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-retry-purge"
	p, k, err := s.SealPayload(ctx, sessionID, "pl-1", "tool_result", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", "pl-1", []byte("data"), []SealedPayload{p}, k)
	appendPersisted(ctx, t, s, req)

	// First purge
	if purgeErr := s.PurgeSession(ctx, sessionID); purgeErr != nil {
		t.Fatalf("first purge failed: %v", purgeErr)
	}

	// Second purge (safe idempotent retry)
	if purgeErr := s.PurgeSession(ctx, sessionID); purgeErr != nil {
		t.Fatalf("second purge retry failed: %v", purgeErr)
	}

	// Verify tombstone is present
	var purgeCount int
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", sessionID).Scan(&purgeCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if purgeCount != 1 {
		t.Fatalf("expected 1 tombstone row, got %d", purgeCount)
	}
}

func TestPurgeSessionPreventsResurrection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-no-resurrect"
	p, k, err := s.SealPayload(ctx, sessionID, "pl-1", "tool_result", []byte("initial"))
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", "pl-1", []byte("initial"), []SealedPayload{p}, k)
	appendPersisted(ctx, t, s, req)

	if purgeErr := s.PurgeSession(ctx, sessionID); purgeErr != nil {
		t.Fatal(purgeErr)
	}

	// Attempting to write a new payload or event to purged session must fail closed
	_, _, err = s.SealPayload(ctx, sessionID, "pl-new", "tool_result", []byte("resurrected"))
	if !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging from SealPayload, got %v", err)
	}

	// Direct attempt to insert event via Append fails
	resurrectReq := makeAcceptedReq(s, sessionID, "run-resurrect", "cmd-resurrect", "", nil, nil, nil)
	resurrectReq.ProtectedInputID = ""
	resurrectReq.MetadataInput = []byte(`{"InputRef":"r"}`)
	resurrectReq.CanonicalInput = resurrectReq.MetadataInput

	_, err = s.Append(ctx, resurrectReq)
	if !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging from Append, got %v", err)
	}

	// Attempt to delete tombstone directly in SQLite is blocked by trigger
	_, delErr := s.db.ExecContext(ctx, "DELETE FROM exec_purges WHERE session_id = ?", sessionID)
	if delErr == nil {
		t.Fatal("expected trigger to block DELETE on exec_purges")
	}

	// Attempt to update tombstone directly in SQLite is blocked by trigger
	_, updateErr := s.db.ExecContext(ctx, "UPDATE exec_purges SET requested_at = 'fake' WHERE session_id = ?", sessionID)
	if updateErr == nil {
		t.Fatal("expected trigger to block UPDATE on exec_purges")
	}
}

func TestPurgeSessionRollbackPreservesSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-purge-rollback"
	p, k, err := s.SealPayload(ctx, sessionID, "pl-1", "tool_result", []byte("keep-me"))
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", "pl-1", []byte("keep-me"), []SealedPayload{p}, k)
	appendPersisted(ctx, t, s, req)

	// Inject fault before purge commit
	s.SetFault(FaultBeforePurgeCommit, errSimulatedPurgeCrash)
	err = s.PurgeSession(ctx, sessionID)
	if err == nil {
		t.Fatal("expected fault before purge commit")
	}

	// Clear fault
	s.SetFault(FaultBeforePurgeCommit, nil)

	// Session data must be intact in DB
	var eventCount int
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events WHERE session_id = ?", sessionID).Scan(&eventCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if eventCount != 1 {
		t.Fatalf("expected 1 event preserved after rollback, got %d", eventCount)
	}

	// Tombstone was rolled back
	var purgeCount int
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = ?", sessionID).Scan(&purgeCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if purgeCount != 0 {
		t.Fatalf("expected 0 purges after rollback, got %d", purgeCount)
	}

	// Now purge again without fault
	if purgeErr := s.PurgeSession(ctx, sessionID); purgeErr != nil {
		t.Fatalf("second purge failed: %v", purgeErr)
	}

	// Now it is gone
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events WHERE session_id = ?", sessionID).Scan(&eventCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if eventCount != 0 {
		t.Fatalf("expected 0 events after successful purge, got %d", eventCount)
	}
}

func TestPurgeValidationEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	var nilStore *Store
	if err := nilStore.PurgeSession(ctx, "s"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable, got %v", err)
	}
	if err := s.PurgeSession(ctx, ""); err == nil {
		t.Fatal("expected InvalidArgument for empty session ID")
	}
	if err := s.PurgeSession(ctx, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err == nil {
		t.Fatal("expected InvalidArgument for too long session ID")
	}
}
