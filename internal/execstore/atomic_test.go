package execstore

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/storage"
)

const cmdRBLostAck = "cmd-rb-lost-ack"

func verifyAtomicFaultPoint(ctx context.Context, t *testing.T, fault FaultPoint, now time.Time) {
	t.Helper()
	canonicalInput := []byte("payload-atomic")
	equality := &fakeEquality{
		payloads: map[string][]byte{"sess-atom/pay-atom": canonicalInput},
		err:      nil,
	}
	s, db, dataDir := newTestStore(ctx, t, equality)

	payload, key := sealedTestPayload(t, "pay-atom", canonicalInput)
	evt := makeRunAccepted(makeEnvelope("run-atom", 1, "cmd-atom", 1, now), "sess-atom", "pay-atom")

	req := &AppendRequest{
		SessionID:                 "sess-atom",
		RunID:                     "run-atom",
		CommandID:                 "cmd-atom",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-atom",
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	}

	s.SetFault(fault, errTestInjected)

	outcome, err := s.Append(ctx, req)
	if err == nil {
		t.Fatalf("expected error at fault point %s, got outcome: %+v", fault, outcome)
	}

	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	reopenedDB, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatalf("reopen DB: %v", err)
	}
	closeTestDB(t, reopenedDB)

	assertTablesEmpty(ctx, t, reopenedDB, fault)
}

func assertTablesEmpty(ctx context.Context, t *testing.T, db *sql.DB, fault FaultPoint) {
	t.Helper()
	tables := []string{
		"exec_admission", "exec_session_keys", "exec_payloads", "exec_runs", "exec_events",
		"exec_invocations", "exec_attempts", "exec_transcript", "exec_receipts",
	}

	for _, table := range tables {
		var count int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
			t.Fatalf("query %s count: %v", table, err)
		}
		if count != 0 {
			t.Fatalf("atomicity violation at %s: table %s has %d rows after rollback and reopen", fault, table, count)
		}
	}
}

func TestExecutionBatchAtomicAcrossReopen(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	faults := []FaultPoint{
		FaultBeforeSessionKeyInsert,
		FaultBeforePayloadInsert,
		FaultBeforeRunInsert,
		FaultBeforeEventAppend,
		FaultBeforeProjections,
		FaultBeforeTranscript,
		FaultBeforeReceiptInsert,
		FaultBeforeAdmissionUpdate,
		FaultBeforeCommit,
	}

	for _, fault := range faults {
		t.Run(string(fault), func(t *testing.T) {
			t.Parallel()
			verifyAtomicFaultPoint(t.Context(), t, fault, now)
		})
	}
}

func TestExecutionCrashRollsBackIncompleteBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, dataDir := newTestStore(ctx, t, nil)
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	dbPath := storage.DBPath(dataDir)

	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	cmd := exec.CommandContext(ctx, "go")
	cmd.Path = exe
	cmd.Args = []string{exe, crashHelperFlag}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}

	if startErr := cmd.Start(); startErr != nil {
		t.Fatal(startErr)
	}

	if _, writeErr := io.WriteString(stdin, dbPath+"\n"); writeErr != nil {
		t.Fatal(writeErr)
	}
	if closeErr := stdin.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	waitErr := cmd.Wait()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ExitCode() != 42 {
		t.Fatalf("expected crash subprocess exit code 42, got %v", waitErr)
	}

	reopenedDB, err := storage.Open(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen DB after crash: %v", err)
	}
	closeTestDB(t, reopenedDB)

	var count int
	if err := reopenedDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_purges WHERE session_id = 'crash-session'").Scan(&count); err != nil {
		t.Fatalf("query exec_purges after crash: %v", err)
	}
	if count != 0 {
		t.Fatalf("crash atomicity failure: exec_purges has %d rows for crash-session", count)
	}
}

func TestCommitLostAcknowledgementCommitted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	canonicalInput := []byte("payload-readback")
	equality := &fakeEquality{
		payloads: map[string][]byte{"sess-rb/pay-rb": canonicalInput},
		err:      nil,
	}
	s, _, _ := newTestStore(ctx, t, equality)

	payload, key := sealedTestPayload(t, "pay-rb", canonicalInput)
	evt := makeRunAccepted(makeEnvelope("run-rb", 1, cmdRBLostAck, 1, now), "sess-rb", "pay-rb")

	req := &AppendRequest{
		SessionID:                 "sess-rb",
		RunID:                     "run-rb",
		CommandID:                 cmdRBLostAck,
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-rb",
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	}

	s.SetFault(FaultAfterCommit, errTestLostAck)

	_, err := s.Append(ctx, req)
	if err == nil || !strings.Contains(err.Error(), "lost-ack") {
		t.Fatalf("expected lost ack error, got %v", err)
	}

	s.SetFault(FaultAfterCommit, nil)

	status, receipt, err := s.ResolveCommit(ctx, cmdRBLostAck)
	if err != nil {
		t.Fatalf("ResolveCommit error: %v", err)
	}
	if status != CommitStatusCommitted {
		t.Fatalf("expected CommitStatusCommitted, got %v", status)
	}
	if receipt.CommandID != cmdRBLostAck {
		t.Fatalf("receipt command ID mismatch: got %s", receipt.CommandID)
	}
}

func TestCommitUncommittedFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	status, _, err := s.ResolveCommit(ctx, "cmd-never-committed")
	if !errors.Is(err, ErrNotCommitted) {
		t.Fatalf("expected ErrNotCommitted, got %v", err)
	}
	if status != CommitStatusNotCommitted {
		t.Fatalf("expected CommitStatusNotCommitted, got %v", status)
	}
}

func TestCommitUnreadableDatabaseUnavailable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	status, _, err := s.ResolveCommit(ctx, "cmd-unreadable")
	if !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable, got %v", err)
	}
	if status != CommitStatusUnavailable {
		t.Fatalf("expected CommitStatusUnavailable, got %v", status)
	}
}
