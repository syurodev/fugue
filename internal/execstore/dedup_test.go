package execstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

const (
	testSessRestart = "sess-restart"
	testRunRestart  = "run-restart"
	testCmdRestart  = "cmd-restart"
	testPayRestart  = "pay-restart"

	testSessConf = "sess-conf"
	testRunConf  = "run-conf"
	testCmdConf  = "cmd-conflict"
	testPayOrig  = "pay-orig"
)

func verifyReopenedStoreReceipt(ctx context.Context, t *testing.T, dataDir string, equality ProtectedEquality, req *AppendRequest) {
	t.Helper()
	reopenedDB, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatalf("reopen DB failed: %v", err)
	}
	closeTestDB(t, reopenedDB)

	clk := clock.NewFake(time.Date(2026, 11, 2, 11, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{LockGeneration: 2, IncarnationID: "host-2", AcquiredAt: clk.Now()}
	idGen := ids.NewSequence("test-reopened")
	reopenedStore, err := New(ctx, reopenedDB, host, clk, idGen, equality)
	if err != nil {
		t.Fatalf("New reopened store: %v", err)
	}

	duplicateOutcome, err := reopenedStore.Append(ctx, req)
	if err != nil {
		t.Fatalf("duplicate Append returned error: %v", err)
	}
	if !duplicateOutcome.Duplicate {
		t.Fatal("expected Duplicate = true on re-sent command")
	}
	if duplicateOutcome.Receipt.CommandID != testCmdRestart {
		t.Fatalf("receipt command ID mismatch: got %s, want %s", duplicateOutcome.Receipt.CommandID, testCmdRestart)
	}
	if duplicateOutcome.Receipt.Result.FirstSequence != 1 || duplicateOutcome.Receipt.Result.LastSequence != 1 {
		t.Fatalf("unexpected receipt result range: %+v", duplicateOutcome.Receipt.Result)
	}

	var eventCount int
	if err := reopenedDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events WHERE run_id = '"+testRunRestart+"'").Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 {
		t.Fatalf("expected exactly 1 event after duplicate append, got %d", eventCount)
	}
}

func TestExecutionReceiptSurvivesRestart(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	canonicalInput := []byte("canonical-input-payload-bytes")
	equality := &fakeEquality{
		payloads: map[string][]byte{
			testCmdRestart + "/" + testPayRestart: canonicalInput,
			testPayRestart:                        canonicalInput,
		},
		err: nil,
	}
	s, db, dataDir := newTestStore(ctx, t, equality)

	payload, key := sealedTestPayload(t, testPayRestart, canonicalInput)
	evt := makeRunAccepted(makeEnvelope(testRunRestart, 1, testCmdRestart, 0, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), testSessRestart, testPayRestart)

	req := &AppendRequest{
		SessionID:                 testSessRestart,
		RunID:                     testRunRestart,
		CommandID:                 testCmdRestart,
		Kind:                      testKindAcceptRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          testPayRestart,
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	}

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("first Append failed: %v", err)
	}

	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	verifyReopenedStoreReceipt(ctx, t, dataDir, equality, req)
}

type conflictCase struct {
	name    string
	mutate  func(*AppendRequest)
	wantErr error
}

func runConflictSubtest(ctx context.Context, t *testing.T, s *Store, origReq *AppendRequest, tc conflictCase, countRows func() (int, int, int), origE, origR, origRec int) {
	t.Helper()
	reqCopy := *origReq
	tc.mutate(&reqCopy)

	_, err := s.Append(ctx, &reqCopy)
	if !errors.Is(err, tc.wantErr) {
		t.Fatalf("expected error %v, got %v", tc.wantErr, err)
	}

	curEvents, curRuns, curReceipts := countRows()
	if curEvents != origE || curRuns != origR || curReceipts != origRec {
		t.Fatalf("conflict wrote new rows! events: %d->%d, runs: %d->%d, receipts: %d->%d",
			origE, curEvents, origR, curRuns, origRec, curReceipts)
	}
}

func receiptConflictCases() []conflictCase {
	return []conflictCase{
		{
			name: "different kind",
			mutate: func(r *AppendRequest) {
				r.Kind = testKindAdmitRun
			},
			wantErr: ErrReceiptConflict,
		},
		{
			name: "different principal",
			mutate: func(r *AppendRequest) {
				r.Principal = "attacker-user"
			},
			wantErr: ErrReceiptConflict,
		},
		{
			name: "different target",
			mutate: func(r *AppendRequest) {
				r.Target = "other-target"
			},
			wantErr: ErrReceiptConflict,
		},
		{
			name: "different canonical input content",
			mutate: func(r *AppendRequest) {
				r.CanonicalInput = []byte("altered-canonical-bytes")
			},
			wantErr: ErrReceiptConflict,
		},
		{
			name: "different codec version",
			mutate: func(r *AppendRequest) {
				r.CodecVersion = "v2-unsupported"
			},
			wantErr: ErrCodecVersionMismatch,
		},
	}
}

func setupReceiptConflictTest(ctx context.Context, t *testing.T) (*Store, *sql.DB, *AppendRequest, func() (int, int, int), int, int, int) {
	t.Helper()
	canonicalInput := []byte("original-input-bytes")
	equality := &fakeEquality{
		payloads: map[string][]byte{
			testCmdConf + "/" + testPayOrig: canonicalInput,
			testPayOrig:                     canonicalInput,
		},
		err: nil,
	}
	s, db, _ := newTestStore(ctx, t, equality)

	payload, key := sealedTestPayload(t, testPayOrig, canonicalInput)
	evt := makeRunAccepted(makeEnvelope(testRunConf, 1, testCmdConf, 0, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), testSessConf, testPayOrig)

	origReq := &AppendRequest{
		SessionID:                 testSessConf,
		RunID:                     testRunConf,
		CommandID:                 testCmdConf,
		Kind:                      testKindAcceptRun,
		Principal:                 "user-orig",
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          testPayOrig,
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	}

	outcome, err := s.Append(ctx, origReq)
	if err != nil || !outcome.Committed {
		t.Fatalf("initial Append failed: %v", err)
	}

	countRows := func() (int, int, int) {
		var events, runs, receipts int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events").Scan(&events); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_runs").Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_receipts").Scan(&receipts); err != nil {
			t.Fatal(err)
		}
		return events, runs, receipts
	}

	origEvents, origRuns, origReceipts := countRows()
	return s, db, origReq, countRows, origEvents, origRuns, origReceipts
}

func TestExecutionReceiptConflictWritesNothing(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, origReq, countRows, origE, origR, origRec := setupReceiptConflictTest(ctx, t)

	for _, tc := range receiptConflictCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runConflictSubtest(ctx, t, s, origReq, tc, countRows, origE, origR, origRec)
		})
	}
}

func TestExecutionMetadataOnlyDedupNeedsNoPayloadOpen(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	metaInput := []byte(`{"Reason":"user_cancel"}`)
	evt := makeRunStopRequested(makeEnvelope("run-meta-dedup", 1, "cmd-meta-dedup", 0, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), kernel.StopReasonUserCancel, kernel.StopModeTerminal, testUser1, "run-meta-dedup", time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))

	req := &AppendRequest{
		SessionID:                 "sess-meta-dedup",
		RunID:                     "run-meta-dedup",
		CommandID:                 "cmd-meta-dedup",
		Kind:                      "RequestStop",
		Principal:                 testUser1,
		Target:                    "run-meta-dedup",
		CodecVersion:              codec.CodecV1,
		MetadataInput:             metaInput,
		CanonicalInput:            metaInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	}

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("initial metadata-only Append failed: %v", err)
	}

	dupOutcome, err := s.Append(ctx, req)
	if err != nil {
		t.Fatalf("duplicate metadata-only Append failed: %v", err)
	}
	if !dupOutcome.Duplicate {
		t.Fatal("expected Duplicate = true for identical metadata-only command")
	}
}

func TestExecutionProtectedDedupFailsClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	canonicalInput := []byte("protected-bytes")
	equality := &fakeEquality{
		payloads: map[string][]byte{
			"pay-prot": canonicalInput,
		},
		err: nil,
	}
	s, _, _ := newTestStore(ctx, t, equality)

	payload, key := sealedTestPayload(t, "pay-prot", canonicalInput)
	evt := makeRunAccepted(makeEnvelope("run-prot", 1, "cmd-prot", 0, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), "sess-prot", "pay-prot")

	req := &AppendRequest{
		SessionID:                 "sess-prot",
		RunID:                     "run-prot",
		CommandID:                 "cmd-prot",
		Kind:                      testKindAcceptRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-prot",
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	}

	outcome, err := s.Append(ctx, req)
	if err != nil || !outcome.Committed {
		t.Fatalf("initial Append failed: %v", err)
	}

	// 1. When equality port is nil: fail closed
	s.equality = nil
	_, errNilEq := s.Append(ctx, req)
	if !errors.Is(errNilEq, ErrProtectedInputUnavailable) {
		t.Fatalf("expected ErrProtectedInputUnavailable when equality port is nil, got %v", errNilEq)
	}

	// 2. When equality port returns error (e.g. vault locked): fail closed
	s.equality = &fakeEquality{payloads: nil, err: errTestLostAck}
	_, errVaultLocked := s.Append(ctx, req)
	if !errors.Is(errVaultLocked, ErrProtectedInputUnavailable) {
		t.Fatalf("expected ErrProtectedInputUnavailable when vault is locked, got %v", errVaultLocked)
	}
}
