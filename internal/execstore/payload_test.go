package execstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

const testToolResultKind = "tool_result"

var errSimulatedRollback = errors.New("simulated-rollback")

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
}

func newVaultAndStore(t *testing.T) (*Store, *vault.Service, string, redact.Secret) {
	t.Helper()
	ctx := t.Context()
	dataDir := t.TempDir()
	db, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)

	clk := clock.NewFake(time.Date(2026, 11, 4, 10, 0, 0, 0, time.UTC))
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatal(migErr)
	}

	host := storage.HostIncarnation{
		LockGeneration: 1,
		IncarnationID:  "host-inc-test",
		AcquiredAt:     clk.Now(),
	}

	vStore := vault.NewSQLKeyringStore(db)
	vService, err := vault.NewService(ctx, vStore, ids.NewSequence("vault-inc-"))
	if err != nil {
		t.Fatal(err)
	}

	masterPass := redact.NewSecret("test-master-password-123")
	if _, setupErr := vService.Setup(ctx, masterPass); setupErr != nil {
		t.Fatal(setupErr)
	}

	idGen := ids.NewSequence("test-payload-id-")
	s, err := New(ctx, db, vService, host, clk, idGen, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeStore(t, s)
	})

	return s, vService, dataDir, masterPass
}

func reopenAndVerifyPayload(
	t *testing.T,
	dataDir string,
	masterPass redact.Secret,
	sessionID, payloadID string,
	expected []byte,
) {
	t.Helper()
	ctx := t.Context()
	reopenedDB, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, reopenedDB)

	reopenedVStore := vault.NewSQLKeyringStore(reopenedDB)
	reopenedVService, err := vault.NewService(ctx, reopenedVStore, ids.NewSequence("vault-inc-reopened-"))
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr := reopenedVService.Unlock(ctx, masterPass); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	clk := clock.NewFake(time.Date(2026, 11, 4, 11, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{
		LockGeneration: 2,
		IncarnationID:  "host-inc-reopened",
		AcquiredAt:     clk.Now(),
	}
	reopenedStore, err := New(ctx, reopenedDB, reopenedVService, host, clk, ids.NewSequence("reopened-id-"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeStore(t, reopenedStore)
	})

	reopenedBytes, err := reopenedStore.OpenPayload(ctx, sessionID, payloadID)
	if err != nil {
		t.Fatalf("OpenPayload after reopen failed: %v", err)
	}
	if !bytes.Equal(reopenedBytes, expected) {
		t.Fatalf("OpenPayload after reopen mismatch: got %q, want %q", string(reopenedBytes), string(expected))
	}
}

func TestProtectedPayloadSealOpenRoundTripAndReopen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, dataDir, masterPass := newVaultAndStore(t)

	sessionID := "sess-roundtrip-1"
	payloadID := "pl-1"
	kind := "command_input"
	plaintext := []byte("hello protected payload world")

	sealed, sealedKey, err := s.SealPayload(ctx, sessionID, payloadID, kind, plaintext)
	if err != nil || sealedKey == nil {
		t.Fatalf("SealPayload failed: err=%v, key=%v", err, sealedKey)
	}

	req := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", payloadID, plaintext, []SealedPayload{sealed}, sealedKey)

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("Append failed: err=%v, outcome=%v", err, outcome)
	}

	// Open payload immediately
	opened, err := s.OpenPayload(ctx, sessionID, payloadID)
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("OpenPayload mismatch: got %q, want %q (err: %v)", string(opened), string(plaintext), err)
	}

	// Reopen database with a new Store and new VaultService
	closeStore(t, s)
	reopenAndVerifyPayload(t, dataDir, masterPass, sessionID, payloadID, plaintext)
}

func verifyPayloadsReadable(t *testing.T, s *Store, sessionID string, payloads map[string][]byte) {
	t.Helper()
	ctx := t.Context()
	for pid, expected := range payloads {
		got, err := s.OpenPayload(ctx, sessionID, pid)
		if err != nil || !bytes.Equal(got, expected) {
			t.Fatalf("payload %s mismatch: got %v, want %v (err: %v)", pid, got, expected, err)
		}
	}
}

func TestSessionKeyReusedAcrossMultiplePayloads(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-multi-pl"
	p1, k1, err := s.SealPayload(ctx, sessionID, "pl-1", testToolResultKind, []byte("result 1"))
	if err != nil || k1 == nil {
		t.Fatalf("first payload failed: err=%v, k1=%v", err, k1)
	}

	p2, k2, err := s.SealPayload(ctx, sessionID, "pl-2", testToolResultKind, []byte("result 2"))
	if err != nil || k2 != nil {
		t.Fatalf("second payload failed: err=%v, k2=%v", err, k2)
	}

	p3, k3, err := s.SealPayload(ctx, sessionID, "pl-3", testToolResultKind, []byte("result 3"))
	if err != nil || k3 != nil {
		t.Fatalf("third payload failed: err=%v, k3=%v", err, k3)
	}

	req := makeAcceptedReq(s, sessionID, "run-multi", "cmd-multi", "pl-1", []byte("result 1"), []SealedPayload{p1, p2, p3}, k1)

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("Append failed: err=%v, outcome=%v", err, outcome)
	}

	// Verify only 1 key row in database
	var count int
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_session_keys WHERE session_id = ?", sessionID).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 1 {
		t.Fatalf("expected 1 session key row, got %d", count)
	}

	verifyPayloadsReadable(t, s, sessionID, map[string][]byte{
		"pl-1": []byte("result 1"),
		"pl-2": []byte("result 2"),
		"pl-3": []byte("result 3"),
	})
}

func verifyRollbackAndSecondWriterCommit(
	ctx context.Context,
	t *testing.T,
	s *Store,
	sessionID string,
	p1, p2 *SealedPayload,
	activeKey *SealedSessionKey,
) {
	t.Helper()
	s.SetFault(FaultBeforeCommit, errSimulatedRollback)
	failReq := makeAcceptedReq(s, sessionID, "run-c1", "cmd-c1", "pl-c1", []byte("payload 1"), []SealedPayload{*p1}, activeKey)
	if _, err := s.Append(ctx, failReq); err == nil {
		t.Fatal("expected fault before commit")
	}

	s.SetFault(FaultBeforeCommit, nil)
	successReq := makeAcceptedReq(s, sessionID, "run-c2", "cmd-c2", "pl-c2", []byte("payload 2"), []SealedPayload{*p2}, activeKey)

	outcome, err := s.Append(ctx, successReq)
	if err != nil || !outcome.Committed {
		t.Fatalf("second append failed: %v", err)
	}

	var keyCount int
	if queryErr := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_session_keys WHERE session_id = ?", sessionID).Scan(&keyCount); queryErr != nil {
		t.Fatal(queryErr)
	}
	if keyCount != 1 {
		t.Fatalf("expected 1 session key, got %d", keyCount)
	}

	res2, err := s.OpenPayload(ctx, sessionID, "pl-c2")
	if err != nil || !bytes.Equal(res2, []byte("payload 2")) {
		t.Fatalf("p2 open mismatch: %v", err)
	}

	if _, err := s.OpenPayload(ctx, sessionID, "pl-c1"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected p1 not found, got %v", err)
	}
}

func TestConcurrentFirstWritesAndRollbackNoKeyMismatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-concurrent-first"

	var (
		p1, p2 SealedPayload
		k1, k2 *SealedSessionKey
		e1, e2 error
	)

	t1 := supervise.Go(ctx, "concurrent-seal-1", func(context.Context) error {
		p1, k1, e1 = s.SealPayload(ctx, sessionID, "pl-c1", testToolResultKind, []byte("payload 1"))
		return nil
	})
	t2 := supervise.Go(ctx, "concurrent-seal-2", func(context.Context) error {
		p2, k2, e2 = s.SealPayload(ctx, sessionID, "pl-c2", testToolResultKind, []byte("payload 2"))
		return nil
	})
	if err := t1.Wait(); err != nil {
		t.Fatal(err)
	}
	if err := t2.Wait(); err != nil {
		t.Fatal(err)
	}

	if e1 != nil || e2 != nil {
		t.Fatalf("concurrent seal failed: e1=%v, e2=%v", e1, e2)
	}

	activeKey := k1
	if activeKey == nil {
		activeKey = k2
	}
	if activeKey == nil {
		t.Fatal("expected at least one writer to get a sealed key")
	}

	verifyRollbackAndSecondWriterCommit(ctx, t, s, sessionID, &p1, &p2, activeKey)
}

func verifyTamperedCiphertextRejected(
	ctx context.Context,
	t *testing.T,
	s *Store,
	sessionID, payloadID, kind string,
) {
	t.Helper()
	var envBytes []byte
	if queryErr := s.db.QueryRowContext(ctx, "SELECT envelope FROM exec_payloads WHERE payload_id = ?", payloadID).Scan(&envBytes); queryErr != nil {
		t.Fatal(queryErr)
	}
	var env crypto.Envelope
	if unmarshalErr := json.Unmarshal(envBytes, &env); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}

	tamperedCipher := append([]byte(nil), env.Ciphertext...)
	tamperedCipher[0] ^= 0xff
	env.Ciphertext = tamperedCipher

	rawKey, _, keyErr := s.getOrCreateSessionKey(ctx, sessionID)
	if keyErr != nil {
		t.Fatal(keyErr)
	}
	ad := payloadAssociatedData(sessionID, payloadID, kind, SessionKeyDefaultKeyID)
	if _, openErr := crypto.OpenWithAD(rawKey, &env, ad); openErr == nil {
		t.Fatal("expected AEAD open to fail on tampered ciphertext")
	} else if errcode.CodeOf(openErr) != errcode.PermissionDenied {
		t.Fatalf("expected PermissionDenied, got %v", errcode.CodeOf(openErr))
	}

	wrongKindAD := payloadAssociatedData(sessionID, payloadID, "other_kind", SessionKeyDefaultKeyID)
	if _, openErr := crypto.OpenWithAD(rawKey, &env, wrongKindAD); openErr == nil {
		t.Fatal("expected AEAD open to fail on wrong kind")
	}

	wrongKeyAD := payloadAssociatedData(sessionID, payloadID, kind, "v2")
	if _, openErr := crypto.OpenWithAD(rawKey, &env, wrongKeyAD); openErr == nil {
		t.Fatal("expected AEAD open to fail on wrong key ID")
	}
}

func TestTamperingAndSubstitutionRejections(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-tamper-1"
	payloadID := "pl-tamper-1"
	kind := testToolResultKind
	plaintext := []byte("confidential-payload-data")

	sealed, sealedKey, err := s.SealPayload(ctx, sessionID, payloadID, kind, plaintext)
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-t1", "cmd-t1", payloadID, plaintext, []SealedPayload{sealed}, sealedKey)

	if _, appendErr := s.Append(ctx, req); appendErr != nil {
		t.Fatalf("initial append failed: %v", appendErr)
	}

	// Substitution: wrong session ID
	if _, openErr := s.OpenPayload(ctx, "wrong-session", payloadID); openErr == nil {
		t.Fatal("expected error opening with wrong session ID")
	}

	// Substitution: wrong payload ID
	if _, openErr := s.OpenPayload(ctx, sessionID, "wrong-payload"); openErr == nil {
		t.Fatal("expected error opening with wrong payload ID")
	}

	verifyTamperedCiphertextRejected(ctx, t, s, sessionID, payloadID, kind)
}

func reopenStoreForDedup(
	ctx context.Context,
	t *testing.T,
	dataDir string,
	masterPass redact.Secret,
) *Store {
	t.Helper()
	reopenedDB, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, reopenedDB)

	vStore := vault.NewSQLKeyringStore(reopenedDB)
	vService, err := vault.NewService(ctx, vStore, ids.NewSequence("v-reopened-"))
	if err != nil {
		t.Fatal(err)
	}
	if unlockErr := vService.Unlock(ctx, masterPass); unlockErr != nil {
		t.Fatal(unlockErr)
	}

	clk := clock.NewFake(time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{LockGeneration: 2, IncarnationID: "host-2", AcquiredAt: clk.Now()}
	reopenedStore, err := New(ctx, reopenedDB, vService, host, clk, ids.NewSequence("id-2-"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		closeStore(t, reopenedStore)
	})
	return reopenedStore
}

func TestProtectedReceiptEqualityDedupAcrossRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, dataDir, masterPass := newVaultAndStore(t)

	sessionID := "sess-dedup-1"
	input1 := []byte(`{"arg":"value-1"}`)

	sealed1, key1, err := s.SealPayload(ctx, sessionID, "pl-in1", "command_input", input1)
	if err != nil {
		t.Fatal(err)
	}

	req := makeAcceptedReq(s, sessionID, "run-d1", "cmd-dedup-1", "pl-in1", input1, []SealedPayload{sealed1}, key1)

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("first append failed: %v", err)
	}

	closeStore(t, s)
	reopenedStore := reopenStoreForDedup(ctx, t, dataDir, masterPass)

	// Retry exact same command with same CanonicalInput (even if fresh payload has different nonce)
	retryReq := makeAcceptedReq(s, sessionID, "run-d1", "cmd-dedup-1", "pl-in1", input1, nil, nil)
	retryReq.Events = nil

	retryOutcome, err := reopenedStore.Append(ctx, retryReq)
	if err != nil || !retryOutcome.Duplicate {
		t.Fatalf("expected Duplicate = true on receipt retry, err=%v, outcome=%v", err, retryOutcome)
	}

	// Retry with different input fails with ErrReceiptConflict
	conflictReq := makeAcceptedReq(s, sessionID, "run-d1", "cmd-dedup-1", "pl-in1", []byte(`{"arg":"different-value"}`), nil, nil)
	conflictReq.Events = nil

	_, err = reopenedStore.Append(ctx, conflictReq)
	if !errors.Is(err, ErrReceiptConflict) {
		t.Fatalf("expected ErrReceiptConflict, got %v", err)
	}
}

func verifyRunLimitOverflow(ctx context.Context, t *testing.T, s *Store, sessionID string) {
	t.Helper()
	batchOverRun := make([]SealedPayload, 65)
	for i := range 65 {
		batchOverRun[i] = SealedPayload{
			PayloadID: fmt.Sprintf("pl-over-%d", i),
			Kind:      testToolResultKind,
			KeyID:     SessionKeyDefaultKeyID,
			Size:      MaxPayloadPlaintext,
			Envelope:  []byte(`{"version":2,"algorithm":"xchacha20-poly1305","key_id":"v1","nonce":"aaaaaaaaaaaaaaaaaaaaaaaa","ciphertext":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
		}
	}

	overRunReq := makeAcceptedReq(s, sessionID, "run-lim-2", "cmd-lim-over-run", "pl-over-0", nil, batchOverRun, nil)
	overRunReq.ExpectedAdmissionRevision = 2

	if _, err := s.Append(ctx, overRunReq); !errors.Is(err, ErrRunPayloadLimitExceeded) {
		t.Fatalf("expected ErrRunPayloadLimitExceeded, got %v", err)
	}
}

func verifySessionLimitOverflow(ctx context.Context, t *testing.T, s *Store, sessionID string) {
	t.Helper()
	_, seedErr := s.db.ExecContext(
		ctx,
		"INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope) VALUES (?, ?, ?, ?, ?, ?)",
		"pl-seed-session-max", sessionID, testToolResultKind, SessionKeyDefaultKeyID, MaxSessionPayloadTotal, []byte("{}"),
	)
	if seedErr != nil {
		t.Fatalf("seed session max payload: %v", seedErr)
	}

	pSmall := SealedPayload{
		PayloadID: "pl-small",
		Kind:      testToolResultKind,
		KeyID:     SessionKeyDefaultKeyID,
		Size:      1,
		Envelope:  []byte(`{"version":2,"algorithm":"xchacha20-poly1305","key_id":"v1","nonce":"aaaaaaaaaaaaaaaaaaaaaaaa","ciphertext":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`),
	}
	overSessReq := makeAcceptedReq(s, sessionID, "run-lim-3", "cmd-lim-over-sess", "pl-small", nil, []SealedPayload{pSmall}, nil)
	overSessReq.ExpectedAdmissionRevision = 2

	if _, err := s.Append(ctx, overSessReq); !errors.Is(err, ErrSessionPayloadLimitExceeded) {
		t.Fatalf("expected ErrSessionPayloadLimitExceeded, got %v", err)
	}
}

func TestPayloadSizeLimitsAndBoundaries(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-limits-1"

	// 1. Single payload: exactly 1 MiB is allowed
	maxBytes := make([]byte, MaxPayloadPlaintext)
	pExact, k1, err := s.SealPayload(ctx, sessionID, "pl-exact-1mib", testToolResultKind, maxBytes)
	if err != nil || k1 == nil {
		t.Fatalf("expected exact 1 MiB to succeed with key: err=%v, k1=%v", err, k1)
	}

	// 2. Single payload: 1 MiB + 1 byte is rejected
	overBytes := make([]byte, MaxPayloadPlaintext+1)
	if _, _, sealErr := s.SealPayload(ctx, sessionID, "pl-over-1mib", testToolResultKind, overBytes); !errors.Is(sealErr, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge, got %v", sealErr)
	}

	// 3. Commit the 1 MiB payload
	req := makeAcceptedReq(s, sessionID, "run-lim-1", "cmd-lim-1", "pl-exact-1mib", maxBytes, []SealedPayload{pExact}, k1)

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("append 1 MiB payload failed: %v", err)
	}

	// 4. Run limit overflow test
	verifyRunLimitOverflow(ctx, t, s, sessionID)

	// 5. Session limit overflow test
	verifySessionLimitOverflow(ctx, t, s, sessionID)

	// 6. Integer overflow check: payload with math.MaxInt64 size
	overflowPayload := SealedPayload{
		PayloadID: "pl-overflow",
		Kind:      testToolResultKind,
		KeyID:     SessionKeyDefaultKeyID,
		Size:      math.MaxInt64,
		Envelope:  []byte(`{"dummy":1}`),
	}
	overflowReq := makeAcceptedReq(s, sessionID, "run-lim-4", "cmd-lim-overflow", "pl-overflow", nil, []SealedPayload{overflowPayload}, nil)
	overflowReq.ExpectedAdmissionRevision = 2
	if _, err := s.Append(ctx, overflowReq); !errors.Is(err, ErrPayloadTooLarge) {
		t.Fatalf("expected ErrPayloadTooLarge for math.MaxInt64 size, got %v", err)
	}
}

func TestNoSensitiveDataInDiagnostics(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	// Single payload too large: error message must not leak content
	overBytes := make([]byte, MaxPayloadPlaintext+1)
	copy(overBytes, "super-secret-text-12345")
	_, _, err := s.SealPayload(ctx, "sess-sec", "pl-sec", testToolResultKind, overBytes)
	if err == nil {
		t.Fatal("expected error")
	}
	errMsg := err.Error()
	if strings.Contains(errMsg, "super-secret") {
		t.Fatalf("error leaked secret plaintext: %s", errMsg)
	}

	// Invalid kind error must not leak secret
	_, _, err = s.SealPayload(ctx, "sess-sec", "pl-sec", "INVALID_KIND", []byte("secret-body"))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("error leaked secret body: %s", err.Error())
	}
}

func TestStoreVaultGetter(t *testing.T) {
	t.Parallel()
	s, v, _, _ := newVaultAndStore(t)
	if s.Vault() != v {
		t.Fatalf("expected vault %p, got %p", v, s.Vault())
	}
	var nilStore *Store
	if nilStore.Vault() != nil {
		t.Fatal("expected nil vault for nil store")
	}
	if nilStore.Host().IncarnationID != "" {
		t.Fatal("expected empty host for nil store")
	}
	if nilStore.Close() != nil {
		t.Fatal("expected nil error on nil store Close")
	}
	if nilStore.DB() != nil {
		t.Fatal("expected nil db on nil store DB")
	}
}

func TestSessionKeyConflictAndMatching(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-key-conflict"
	p1, k1, err := s.SealPayload(ctx, sessionID, "pl-1", testToolResultKind, []byte("data 1"))
	if err != nil {
		t.Fatal(err)
	}
	req1 := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", "pl-1", []byte("data 1"), []SealedPayload{p1}, k1)
	if _, appendErr := s.Append(ctx, req1); appendErr != nil {
		t.Fatal(appendErr)
	}

	conn, err := s.acquireConfiguredConn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Fatal(closeErr)
		}
	}()

	// Matching key succeeds (idempotent key insert)
	if matchErr := insertSessionKey(ctx, conn, sessionID, k1, s.clk.Now()); matchErr != nil {
		t.Fatalf("matching key insert failed: %v", matchErr)
	}

	// Conflicting key fails with ErrSessionKeyConflict
	conflictKey := &SealedSessionKey{
		KeyID:      k1.KeyID,
		WrappedKey: bytes.Repeat([]byte{0x42}, len(k1.WrappedKey)),
	}
	if conflictErr := insertSessionKey(ctx, conn, sessionID, conflictKey, s.clk.Now()); !errors.Is(conflictErr, ErrSessionKeyConflict) {
		t.Fatalf("expected ErrSessionKeyConflict, got %v", conflictErr)
	}
}

func TestPayloadValidationEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	// Empty and long session ID
	if _, _, err := s.SealPayload(ctx, "", "pl", testToolResultKind, []byte("x")); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	longSessionID := strings.Repeat("s", 129)
	if _, _, err := s.SealPayload(ctx, longSessionID, "pl", testToolResultKind, []byte("x")); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	// Long payload ID
	longPayloadID := strings.Repeat("p", 129)
	if _, _, err := s.SealPayload(ctx, "sess-edge", longPayloadID, testToolResultKind, []byte("x")); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	// Auto-generate payload ID when empty
	sealed, _, err := s.SealPayload(ctx, "sess-edge", "", testToolResultKind, []byte("x"))
	if err != nil || sealed.PayloadID == "" {
		t.Fatalf("expected auto-generated payload ID, got %v (err: %v)", sealed.PayloadID, err)
	}
}

func TestPayloadOpenValidationEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	// Open validation edge cases
	if _, err := s.OpenPayload(ctx, "", "pl"); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
	if _, err := s.OpenPayload(ctx, "sess", ""); errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	// Vault nil store check
	storeNoVault := &Store{db: s.db}
	if _, err := storeNoVault.OpenPayload(ctx, "sess", "pl"); !errors.Is(err, vault.ErrLocked) {
		t.Fatalf("expected vault.ErrLocked for store with nil vault, got %v", err)
	}

	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.OpenPayload(canceledCtx, "sess-edge", "pl"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable for canceled context, got %v", err)
	}
}

func TestKeyCacheOperations(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	if _, _, err := s.SealPayload(ctx, "sess-edge", "pl", testToolResultKind, []byte("x")); err != nil {
		t.Fatal(err)
	}

	// Purged session check on SealPayload and keyCache
	if key, found := s.keyCache.get("non-existent"); found || key != nil {
		t.Fatal("expected false for non-existent key")
	}
	if key, found := s.keyCache.get("sess-edge"); !found || len(key) == 0 {
		t.Fatal("expected key from cache for sess-edge")
	}
	s.keyCache.purge("sess-purged")
	if key, found := s.keyCache.get("sess-purged"); found || key != nil {
		t.Fatal("expected nil, false from cache for purged session")
	}
	if _, _, err := s.SealPayload(ctx, "sess-purged", "pl", testToolResultKind, []byte("x")); !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging from SealPayload, got %v", err)
	}
	if _, err := s.OpenPayload(ctx, "sess-purged", "pl"); !errors.Is(err, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging from OpenPayload, got %v", err)
	}
}

func TestOpenPayloadCorruptedEnvelope(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _, _ := newVaultAndStore(t)

	sessionID := "sess-corrupt-env"
	p, k, err := s.SealPayload(ctx, sessionID, "pl-1", testToolResultKind, []byte("valid"))
	if err != nil {
		t.Fatal(err)
	}
	req := makeAcceptedReq(s, sessionID, "run-1", "cmd-1", "pl-1", []byte("valid"), []SealedPayload{p}, k)
	if _, appendErr := s.Append(ctx, req); appendErr != nil {
		t.Fatal(appendErr)
	}

	// Insert corrupted envelope JSON in DB
	if _, execErr := s.db.ExecContext(
		ctx,
		"INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope) VALUES (?, ?, ?, ?, ?, ?)",
		"pl-bad-json", sessionID, testToolResultKind, SessionKeyDefaultKeyID, 10, []byte("{bad-json"),
	); execErr != nil {
		t.Fatal(execErr)
	}
	if _, openErr := s.OpenPayload(ctx, sessionID, "pl-bad-json"); errcode.CodeOf(openErr) != errcode.InvalidArgument {
		t.Fatalf("expected InvalidArgument for bad envelope JSON, got %v", openErr)
	}

	// Insert corrupted wrapped key in DB for a separate session
	badSessionID := "sess-corrupt-key"
	if _, execErr := s.db.ExecContext(ctx, "INSERT INTO exec_admission (session_id, owner_incarnation, admission_token, revision, queue_revision, context_epoch, context_revision, context_requirement) VALUES (?, 0, 0, 1, 0, 0, 0, 'none')", badSessionID); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := s.db.ExecContext(ctx, "INSERT INTO exec_session_keys (session_id, key_id, wrapped_key, created_at) VALUES (?, ?, ?, 'now')", badSessionID, SessionKeyDefaultKeyID, []byte("garbage")); execErr != nil {
		t.Fatal(execErr)
	}
	if _, execErr := s.db.ExecContext(ctx, "INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope) VALUES ('pl-bad-key', ?, ?, ?, 10, ?)", badSessionID, testToolResultKind, SessionKeyDefaultKeyID, []byte("{}")); execErr != nil {
		t.Fatal(execErr)
	}
	if _, openErr := s.OpenPayload(ctx, badSessionID, "pl-bad-key"); !errors.Is(openErr, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable for corrupted wrapped key, got %v", openErr)
	}
}
