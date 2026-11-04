package execstore

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/ids"
)

const (
	testRunReg  = "run-reg"
	testSessReg = "sess-reg"
)

var errStrictSessionMismatch = errors.New("strict equality: session mismatch")

// reviewReq creates an AppendRequest with standard defaults for regression tests.
func reviewReq(cmd string, seq, rev, admRev uint64, events ...kernel.Event) *AppendRequest {
	return &AppendRequest{
		SessionID:                 testSessReg,
		RunID:                     testRunReg,
		CommandID:                 cmd,
		Kind:                      testKindAcceptRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte(`{}`),
		CanonicalInput:            []byte(`{}`),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       rev,
		ExpectedAdmissionRevision: admRev,
		Events:                    events,
		Result:                    codec.ReceiptResult{FirstSequence: seq, LastSequence: seq, RejectionCode: "", Message: ""},
		CorrelationID:             cmd,
	}
}

func runR1AcceptAndAdmit(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	now := s.clk.Now()

	// 1. Accept run in tx 1 (queued)
	accEvt := makeRunAccepted(makeEnvelope(testRunReg, 1, "c-acc", 0, now), testSessReg, "")
	req1 := reviewReq("c-acc", 1, 0, 1, accEvt)
	if _, err := s.Append(ctx, req1); err != nil {
		t.Fatalf("append accept: %v", err)
	}
	r1, err := s.ReadRun(ctx, testRunReg)
	if err != nil || r1.Lifecycle != kernel.LifecycleQueued {
		t.Fatalf("expected Queued, got %s, err: %v", r1.Lifecycle, err)
	}

	// 2. Admit run in tx 2 (running)
	admEvt := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-adm", 1, now), 1)
	req2 := reviewReq("c-adm", 2, 1, 1, admEvt)
	req2.Kind = testKindAdmitRun
	req2.QueueFence = nil
	req2.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, appendErr := s.Append(ctx, req2); appendErr != nil {
		t.Fatalf("append admit: %v", appendErr)
	}
	r2, err := s.ReadRun(ctx, testRunReg)
	if err != nil || r2.Lifecycle != kernel.LifecycleRunning {
		t.Fatalf("expected Running after admit, got %s, err: %v", r2.Lifecycle, err)
	}
}

func runR1StepAndModel(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()
	now := s.clk.Now()

	// 3. Step started in tx 3
	stepEvt := makeStepStarted(makeEnvelope(testRunReg, 3, "c-step", 1, now), 1)
	req3 := reviewReq("c-step", 3, 2, 2, stepEvt)
	req3.Kind = testKindAdvanceRun
	req3.QueueFence = nil
	req3.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := s.Append(ctx, req3); err != nil {
		t.Fatalf("append step: %v", err)
	}
	r3, err := s.ReadRun(ctx, testRunReg)
	if err != nil || r3.StepIndex != 1 || r3.Lifecycle != kernel.LifecycleRunning {
		t.Fatalf("expected step 1 running, got step %d %s, err: %v", r3.StepIndex, r3.Lifecycle, err)
	}

	// 4. Model planned and response recorded in tx 4
	mPlan := makeModelRequestPlanned(makeEnvelope(testRunReg, 4, "c-mplan", 1, now), "m-inv-1", "att-1")
	mResp := makeModelResponseRecorded(makeEnvelope(testRunReg, 5, "c-mresp", 1, now), "m-inv-1", "att-1", "")
	req4 := reviewReq("c-model", 4, 3, 3, mPlan, mResp)
	req4.Kind = testKindAdvanceRun
	req4.QueueFence = nil
	req4.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	req4.Result = codec.ReceiptResult{FirstSequence: 4, LastSequence: 5, RejectionCode: "", Message: ""}
	if _, appendErr := s.Append(ctx, req4); appendErr != nil {
		t.Fatalf("append model: %v", appendErr)
	}

	// Verify replay matches projections after model progression
	rebuilt, err := s.RebuildRun(ctx, testRunReg)
	if err != nil {
		t.Fatalf("rebuild run after model progression: %v", err)
	}
	if rebuilt.LastSequence != 5 {
		t.Fatalf("rebuilt sequence = %d, want 5", rebuilt.LastSequence)
	}
}

func runR1NoopAndRejection(t *testing.T, ctx context.Context, s *Store) {
	t.Helper()

	// 5. Append a no-op command in tx 5 (len(Events) == 0)
	reqNoop := reviewReq("c-noop", 0, 4, 4)
	reqNoop.Kind = testKindAdvanceRun
	reqNoop.QueueFence = nil
	reqNoop.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	reqNoop.Events = nil
	reqNoop.Result = codec.ReceiptResult{FirstSequence: 0, LastSequence: 0, RejectionCode: "", Message: ""}
	if _, err := s.Append(ctx, reqNoop); err != nil {
		t.Fatalf("append noop: %v", err)
	}

	// 6. Append a typed rejection in tx 6 (len(Events) == 0)
	reqRej := reviewReq("c-rej", 0, 4, 5)
	reqRej.Kind = testKindAdvanceRun
	reqRej.QueueFence = nil
	reqRej.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	reqRej.Events = nil
	reqRej.Result = codec.ReceiptResult{FirstSequence: 0, LastSequence: 0, RejectionCode: "rate_limited", Message: ""}
	if _, err := s.Append(ctx, reqRej); err != nil {
		t.Fatalf("append rejection: %v", err)
	}

	// Facts must NOT be reset by no-op or rejection
	rAfter, err := s.ReadRun(ctx, testRunReg)
	if err != nil {
		t.Fatalf("read run after noop/rejection: %v", err)
	}
	if rAfter.LastSequence != 5 {
		t.Errorf("cursor reset: got sequence %d, want 5", rAfter.LastSequence)
	}
	if rAfter.Lifecycle != kernel.LifecycleRunning {
		t.Errorf("lifecycle reset: got %s, want %s", rAfter.Lifecycle, kernel.LifecycleRunning)
	}
	if rAfter.StepIndex != 1 {
		t.Errorf("step index reset: got %d, want 1", rAfter.StepIndex)
	}

	// Replay must still match projections exactly
	if _, err := s.RebuildRun(ctx, testRunReg); err != nil {
		t.Fatalf("rebuild run after noop and rejection: %v", err)
	}
}

// R1 — Incremental state/projections:
// Multi-transaction progression: accept -> admit -> step -> model/tool progression.
// Replay must match projections. No-op and typed rejection must not reset cursor,
// lifecycle, context, blocker or attempt state.
func TestRegressionR1_IncrementalStateAndProjections(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	runR1AcceptAndAdmit(t, ctx, s)
	runR1StepAndModel(t, ctx, s)
	runR1NoopAndRejection(t, ctx, s)
}

// cancelGenerator cancels ctx during ID generation.
type cancelGenerator struct {
	cancel context.CancelFunc
}

func (g *cancelGenerator) NewID() (string, error) {
	g.cancel()
	return "evt-canceled-id", nil
}

// R2 — Rollback when context is cancelled:
// Cleanup must use an active, bounded context to execute ROLLBACK.
// Discard physical connection from pool if transactional state cannot be confirmed clean.
// Shared sql.DB must not be closed.
func TestRegressionR2_CanceledAppendRollsBack(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	s, db, _ := newTestStore(ctx, t, nil)
	s.ids = &cancelGenerator{cancel: cancel}

	req := reviewReq("c-cancel-test", 1, 0, 1, reviewAccepted(s))
	if _, err := s.Append(ctx, req); err == nil {
		t.Fatal("expected cancellation error")
	}

	// Verify no uncommitted rows remain on the pooled connection
	var n int
	if countErr := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM exec_runs`).Scan(&n); countErr != nil {
		t.Fatalf("query exec_runs count: %v", countErr)
	}
	if n != 0 {
		t.Errorf("canceled append left %d uncommitted run rows visible in pool", n)
	}

	// Subsequent append with a fresh context on the same store must succeed cleanly
	s.ids = ids.NewSequence("fresh-id")
	reqSuccess := reviewReq("c-after-cancel", 1, 0, 1, reviewAccepted(s))
	outcome, appendErr := s.Append(t.Context(), reqSuccess)
	if appendErr != nil || !outcome.Committed {
		t.Fatalf("subsequent append failed on shared store: committed=%v, err=%v", outcome.Committed, appendErr)
	}
}

func testR3QueueRejectionAndValidAdmit(t *testing.T, ctx context.Context, s *Store, db *sql.DB) {
	t.Helper()
	// Step 1: Accept queued run 1
	if _, err := s.Append(ctx, reviewReq("c-q1", 1, 0, 1, reviewAccepted(s))); err != nil {
		t.Fatal(err)
	}

	// Step 2: Attempt to admit run 1 under QueueFence (without AdmissionFence)
	admitEvt := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-bad-admit", 999, s.clk.Now()), 999)
	reqBad := reviewReq("c-bad-admit", 2, 1, 1, admitEvt)
	reqBad.Kind = testKindAdmitRun
	result, err := s.Append(ctx, reqBad)
	if err == nil {
		var active any
		if scanErr := db.QueryRowContext(ctx, `SELECT active_run_id FROM exec_admission WHERE session_id='sess-reg'`).Scan(&active); scanErr != nil {
			t.Fatal(scanErr)
		}
		t.Fatalf("queue-only request committed admission: committed=%v, active_run=%v", result.Committed, active)
	}
	if !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence, got %v", err)
	}

	// Step 3: Now properly admit run 1 with AdmissionFence
	reqAdmit := reviewReq("c-good-admit", 2, 1, 1, admitEvt)
	reqAdmit.Kind = testKindAdmitRun
	reqAdmit.QueueFence = nil
	reqAdmit.AdmissionFence = &AdmissionFence{Token: 999, OwnerIncarnation: 1}
	if _, err := s.Append(ctx, reqAdmit); err != nil {
		t.Fatalf("valid admission failed: %v", err)
	}

	// Verify run 1 is the active run
	var activeRun string
	if err := db.QueryRowContext(ctx, `SELECT active_run_id FROM exec_admission WHERE session_id='sess-reg'`).Scan(&activeRun); err != nil {
		t.Fatal(err)
	}
	if activeRun != testRunReg {
		t.Fatalf("expected active_run_id %q, got %q", testRunReg, activeRun)
	}
}

func testR3QueuedFinishRetainsActiveRun(t *testing.T, ctx context.Context, s *Store, db *sql.DB) {
	t.Helper()
	// Step 4: Accept a separate queued run 2
	env2 := makeEnvelope("run-2", 1, "c-q2", 0, s.clk.Now())
	reqQ2 := reviewReq("c-q2", 1, 0, 2, makeRunAccepted(env2, testSessReg, ""))
	reqQ2.RunID = "run-2"
	reqQ2.ExpectedAdmissionRevision = 2
	reqQ2.QueueFence = &QueueFence{QueueRevision: 0}
	if _, err := s.Append(ctx, reqQ2); err != nil {
		t.Fatalf("append queued run 2: %v", err)
	}

	// Step 5: Queued run 2 finishes while queued. It must NOT clear run 1's active admission!
	env2Fin := makeEnvelope("run-2", 2, "c-q2-fin", 0, s.clk.Now())
	stopEvt := makeRunStopRequested(env2Fin, kernel.StopReasonUserCancel, kernel.StopModeTerminal, "u", "run-2", s.clk.Now())
	reqQ2Fin := reviewReq("c-q2-fin", 2, 1, 3, stopEvt)
	reqQ2Fin.RunID = "run-2"
	reqQ2Fin.Kind = cmdRequestStop
	reqQ2Fin.MetadataInput = []byte(`{"Reason":"user_cancel"}`)
	reqQ2Fin.CanonicalInput = reqQ2Fin.MetadataInput
	reqQ2Fin.ExpectedAdmissionRevision = 3
	reqQ2Fin.QueueFence = &QueueFence{QueueRevision: 1}
	if _, err := s.Append(ctx, reqQ2Fin); err != nil {
		t.Fatalf("queued finish: %v", err)
	}

	// Verify run 1's admission was kept
	var activeRun string
	if err := db.QueryRowContext(ctx, `SELECT active_run_id FROM exec_admission WHERE session_id='sess-reg'`).Scan(&activeRun); err != nil {
		t.Fatal(err)
	}
	if activeRun != testRunReg {
		t.Fatalf("queued operation altered active run: got %q, want %q", activeRun, testRunReg)
	}
}

// R3 — Queue/admission authority:
// QueueFence must not permit committing RunAdmitted or acquiring admission.
// Queued operations must not clear or steal another run's admission row.
func TestRegressionR3_QueueAuthorityCannotAdmit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	testR3QueueRejectionAndValidAdmit(t, ctx, s, db)
	testR3QueuedFinishRetainsActiveRun(t, ctx, s, db)
}

// R4 — Metadata-only plaintext boundary:
// Arbitrary prompt/args/output in MetadataInput must be rejected and fail closed.
func TestRegressionR4_MetadataOnlyPlaintextBoundary(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	const secret = "TOP_SECRET_PROMPT_CONTENT"
	req := reviewReq("c-meta-secret", 1, 0, 1, reviewAccepted(s))
	req.MetadataInput = []byte(`{"Prompt":"` + secret + `"}`)
	req.CanonicalInput = req.MetadataInput

	if _, err := s.Append(ctx, req); err == nil {
		var raw []byte
		if scanErr := db.QueryRowContext(ctx, `SELECT metadata_input FROM exec_receipts WHERE command_id='c-meta-secret'`).Scan(&raw); scanErr != nil {
			t.Fatal(scanErr)
		}
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatal("arbitrary protected prompt persisted as plaintext metadata")
		}
		t.Fatal("expected failure on unallowed metadata field Prompt")
	}

	// Mismatched CanonicalInput and MetadataInput must also be rejected
	reqMismatch := reviewReq("c-mismatch", 1, 0, 1, reviewAccepted(s))
	reqMismatch.MetadataInput = []byte(`{}`)
	reqMismatch.CanonicalInput = []byte(`{"extra":true}`)
	if _, err := s.Append(ctx, reqMismatch); err == nil {
		t.Fatal("expected failure when CanonicalInput != MetadataInput")
	}
}

// R5 — Duplicate appeared after preflight:
// Deterministically verify that duplicate found on executeAppendTx rolls back
// the transaction, does not leak a live transaction to the pool, and does not deadlock.
func TestRegressionR5_DuplicateAfterPreflightNoLeak(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	req := reviewReq("c-dup-tx", 1, 0, 1, reviewAccepted(s))
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}

	// Simulate an interleaved duplicate inside an active transaction
	conn, connErr := db.Conn(ctx)
	if connErr != nil {
		t.Fatal(connErr)
	}
	if _, execErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); execErr != nil {
		t.Fatal(execErr)
	}

	out, txErr := s.executeAppendTx(ctx, conn, req)
	if txErr != nil || !out.Duplicate {
		t.Fatalf("expected duplicate outcome: duplicate=%v, err=%v", out.Duplicate, txErr)
	}
	if closeErr := conn.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// Next BEGIN on pooled connection must succeed without 'cannot start a transaction within a transaction'
	if _, execErr := db.ExecContext(ctx, "BEGIN IMMEDIATE; ROLLBACK;"); execErr != nil {
		t.Fatalf("duplicate branch returned a live transaction to pool: %v", execErr)
	}
}

// R6 — Canonical transcript:
// Queued run must NOT insert canonical input into transcript prematurely.
// Materialization must happen at RunAdmitted according to admission order and context epoch.
func TestRegressionR6_CanonicalTranscriptAdmissionOrder(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	// Step 1: Accept queued run 1
	if _, err := s.Append(ctx, reviewReq("c-q-order", 1, 0, 1, reviewAccepted(s))); err != nil {
		t.Fatal(err)
	}

	// Must NOT be in exec_transcript yet
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_transcript WHERE session_id='sess-reg'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("queued input already in canonical transcript: %d rows", n)
	}

	// Step 2: Admit run 1 in context epoch 2
	admit := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-admit-epoch", 1, s.clk.Now()), 1)
	admit.ContextEpoch = 2
	reqAdmit := reviewReq("c-admit-epoch", 2, 1, 1, admit)
	reqAdmit.Kind = testKindAdmitRun
	reqAdmit.QueueFence = nil
	reqAdmit.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := s.Append(ctx, reqAdmit); err != nil {
		t.Fatal(err)
	}

	// Verify input materialized in epoch 2, not epoch 0
	var countEpoch0, countEpoch2 int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_transcript WHERE session_id='sess-reg' AND context_epoch=0`).Scan(&countEpoch0); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_transcript WHERE session_id='sess-reg' AND context_epoch=2`).Scan(&countEpoch2); err != nil {
		t.Fatal(err)
	}

	if countEpoch0 != 0 {
		t.Errorf("transcript has %d rows in epoch 0, want 0", countEpoch0)
	}
	if countEpoch2 != 1 {
		t.Errorf("transcript has %d rows in epoch 2, want 1", countEpoch2)
	}
}

// strictSessionEquality verifies that OpenPayload strictly checks session ID.
type strictSessionEquality struct {
	expectedSession string
	input           []byte
}

func (e *strictSessionEquality) OpenPayload(_ context.Context, session, _ string) ([]byte, error) {
	if session != e.expectedSession {
		return nil, errStrictSessionMismatch
	}
	return e.input, nil
}

// R7 — Session binding of protected equality:
// OpenPayload must receive the verified session ID, not command ID.
func TestRegressionR7_ProtectedDedupUsesSessionBinding(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	input := []byte(`{"protected":"content"}`)
	eq := &strictSessionEquality{expectedSession: "sess-strict", input: input}
	s, _, _ := newTestStore(ctx, t, eq)

	payload, key := sealedTestPayload(t, "pay-strict", input)
	acc := makeRunAccepted(makeEnvelope("run-strict", 1, "c-strict", 0, s.clk.Now()), "sess-strict", "pay-strict")
	req := reviewReq("c-strict", 1, 0, 1, acc)
	req.SessionID = "sess-strict"
	req.RunID = "run-strict"
	req.MetadataInput = nil
	req.CanonicalInput = input
	req.ProtectedInputID = "pay-strict"
	req.SealedPayloads = []SealedPayload{payload}
	req.SealedSessionKey = &key

	if _, err := s.Append(ctx, req); err != nil {
		t.Fatalf("first append failed: %v", err)
	}

	// Duplicate append must pass session to OpenPayload and succeed
	dupOutcome, err := s.Append(ctx, req)
	if err != nil || !dupOutcome.Duplicate {
		t.Fatalf("protected duplicate failed with session-aware opener: dup=%v, err=%v", dupOutcome.Duplicate, err)
	}
}

func corruptAndAssertUnavailable(t *testing.T, ctx context.Context, s *Store, db *sql.DB, pragmaBad, pragmaGood string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, pragmaBad); err != nil {
		t.Fatal(err)
	}
	status, _, err := s.ResolveCommit(ctx, "c-sync-test")
	if err == nil || status != CommitStatusUnavailable {
		t.Errorf("unsafe %s accepted: status=%s, err=%v", pragmaBad, status, err)
	}
	if _, err := db.ExecContext(ctx, pragmaGood); err != nil {
		t.Fatal(err)
	}
}

func testR8Pragmas(t *testing.T, ctx context.Context, s *Store, db *sql.DB) {
	t.Helper()
	corruptAndAssertUnavailable(t, ctx, s, db, "PRAGMA synchronous=OFF", "PRAGMA synchronous=FULL")
	corruptAndAssertUnavailable(t, ctx, s, db, "PRAGMA journal_mode=WAL", "PRAGMA journal_mode=DELETE")
	corruptAndAssertUnavailable(t, ctx, s, db, "PRAGMA foreign_keys=OFF", "PRAGMA foreign_keys=ON")
}

func testR8UncleanConn(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	conn, connErr := db.Conn(ctx)
	if connErr != nil {
		t.Fatal(connErr)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Log(closeErr)
		}
	}()
	if _, execErr := conn.ExecContext(ctx, "BEGIN IMMEDIATE;"); execErr != nil {
		t.Fatal(execErr)
	}
	if verifyErr := verifyRecoveredConnection(ctx, conn); verifyErr == nil {
		t.Error("expected verifyRecoveredConnection to fail on uncommitted transaction")
	}
	if _, execErr := conn.ExecContext(ctx, "ROLLBACK;"); execErr != nil {
		t.Fatal(execErr)
	}
}

// R8 — Read-back connection verification:
// ResolveCommit must verify FULL/DELETE/foreign_keys and clean autocommit mode.
func TestRegressionR8_ResolveCommitConnectionVerification(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

	req := reviewReq("c-sync-test", 1, 0, 1, reviewAccepted(s))
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}

	testR8Pragmas(t, ctx, s, db)
	testR8UncleanConn(t, ctx, db)
}

const (
	testFieldBad           = "bad"
	cmdCloseWithoutPayload = "CloseRunWithoutPayload"
	cmdSelectRunContext    = "SelectRunContext"
)

func TestSafeMetadataFieldAllowlist(t *testing.T) {
	t.Parallel()
	cases := []struct {
		kind  string
		field string
		want  bool
	}{
		{"AcceptRun", "InputRef", true},
		{"AcceptRun", "Prompt", false},
		{cmdAdmitRun, "token", true},
		{cmdAdmitRun, "AdmissionToken", true},
		{cmdAdmitRun, "ContextEpoch", true},
		{cmdAdmitRun, testFieldBad, false},
		{"AcquireRunOwnership", "OwnerIncarnation", true},
		{"AcquireRunOwnership", testFieldBad, false},
		{"AdvanceRun", "StepIndex", true},
		{"AdvanceRun", testFieldBad, false},
		{cmdRequestStop, "stop", true},
		{cmdRequestStop, "Reason", true},
		{cmdRequestStop, "Mode", true},
		{cmdRequestStop, testFieldBad, false},
		{cmdCloseWithoutPayload, "RunID", true},
		{cmdCloseWithoutPayload, fieldRunRevision, true},
		{cmdCloseWithoutPayload, "ExactAttemptIDs", true},
		{cmdCloseWithoutPayload, "CauseIDs", true},
		{cmdCloseWithoutPayload, "Actor", true},
		{cmdCloseWithoutPayload, "UnavailabilityReason", true},
		{cmdCloseWithoutPayload, testFieldBad, false},
		{cmdSelectRunContext, "OldContextRevision", true},
		{cmdSelectRunContext, "NewContextRevision", true},
		{cmdSelectRunContext, "Epoch", true},
		{cmdSelectRunContext, "StartRunID", true},
		{cmdSelectRunContext, "Mode", true},
		{cmdSelectRunContext, "Actor", true},
		{cmdSelectRunContext, "ValidationEvidence", true},
		{cmdSelectRunContext, testFieldBad, false},
		{"ResumeRun", fieldRunRevision, true},
		{"ResumeRun", testFieldBad, false},
		{"RecoverRun", fieldRunRevision, true},
		{"RecoverRun", testFieldBad, false},
		{"UnknownCommand", "field", false},
	}
	for _, tc := range cases {
		got := isSafeMetadataField(tc.kind, tc.field)
		if got != tc.want {
			t.Errorf("isSafeMetadataField(%q, %q) = %v, want %v", tc.kind, tc.field, got, tc.want)
		}
	}
	if isMetadataOnlyPermitted("UnknownCommand") {
		t.Error("expected isMetadataOnlyPermitted to return false for UnknownCommand")
	}
}

func TestVerifyRebuiltProjectionsMismatches(t *testing.T) {
	t.Parallel()
	baseState := kernel.NewState()
	baseState.Accepted = true
	baseState.Admitted = true
	baseState.LastSequence = 1
	baseState.CurrentStepIndex = 1
	baseState.ContextEpoch = 1
	baseRec := RunRecord{
		LastSequence: 1,
		Lifecycle:    kernel.LifecycleRunning,
		Phase:        kernel.DerivePhase(baseState),
		StepIndex:    1,
		ContextEpoch: 1,
	}

	tests := []struct {
		name   string
		mutate func(s *kernel.State, r *RunRecord)
	}{
		{"sequence mismatch", func(_ *kernel.State, r *RunRecord) { r.LastSequence = 99 }},
		{"lifecycle mismatch", func(_ *kernel.State, r *RunRecord) { r.Lifecycle = kernel.LifecycleFinished }},
		{"phase mismatch", func(_ *kernel.State, r *RunRecord) { r.Phase = kernel.PhaseFinalizing }},
		{"outcome mismatch", func(s *kernel.State, _ *RunRecord) { s.Outcome = kernel.RunOutcomeCompleted }},
		{"wait reasons mismatch", func(_ *kernel.State, r *RunRecord) { r.WaitReasons = []kernel.WaitReason{kernel.WaitReasonApproval} }},
		{"step index mismatch", func(_ *kernel.State, r *RunRecord) { r.StepIndex = 99 }},
		{"context epoch mismatch", func(_ *kernel.State, r *RunRecord) { r.ContextEpoch = 99 }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			st := *baseState
			rec := baseRec
			tc.mutate(&st, &rec)
			if err := verifyRebuiltProjections(&st, &rec); err == nil {
				t.Errorf("expected error for %s", tc.name)
			}
		})
	}
}

func TestVerifyAdmissionFencesMismatches(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)
	now := s.clk.Now()

	admEvt := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-adm", 1, now), 1)

	// Host lock generation mismatch
	reqHostMismatch := reviewReq("c-adm", 2, 1, 1, admEvt)
	reqHostMismatch.Kind = testKindAdmitRun
	reqHostMismatch.QueueFence = nil
	reqHostMismatch.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 999}
	if err := s.verifyAdmissionFences(reqHostMismatch); err == nil {
		t.Error("expected error on host lock generation mismatch")
	}

	// Token mismatch
	reqTokenMismatch := reviewReq("c-adm", 2, 1, 1, admEvt)
	reqTokenMismatch.Kind = testKindAdmitRun
	reqTokenMismatch.QueueFence = nil
	reqTokenMismatch.AdmissionFence = &AdmissionFence{Token: 999, OwnerIncarnation: s.host.LockGeneration}
	if err := s.verifyAdmissionFences(reqTokenMismatch); err == nil {
		t.Error("expected error on token mismatch")
	}

	// Event incarnation mismatch
	admEvtWrongInc := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-adm", 1, now), 1)
	admEvtWrongInc.OwnerIncarnation = 999
	reqIncMismatch := reviewReq("c-adm", 2, 1, 1, admEvtWrongInc)
	reqIncMismatch.Kind = testKindAdmitRun
	reqIncMismatch.QueueFence = nil
	reqIncMismatch.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: s.host.LockGeneration}
	if err := s.verifyAdmissionFences(reqIncMismatch); err == nil {
		t.Error("expected error on event incarnation mismatch")
	}
}

func TestRollbackAndVerifyCleanClosedConn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	var clean bool
	rollbackAndVerifyClean(ctx, conn, &clean)
	if clean {
		t.Error("expected clean=false on closed conn")
	}
}

func TestDiscardConn(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	discardConn(conn)
}

func TestLoadCommittedRunStateMissingEvents(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Log(closeErr)
		}
	}()
	if _, err := loadCommittedRunState(ctx, conn, "missing-run", 5); err == nil {
		t.Error("expected ErrCorruptHistory on missing sequence")
	}
}

func reviewAccepted(s *Store) *kernel.RunAcceptedEvent {
	return makeRunAccepted(makeEnvelope(testRunReg, 1, "c-acc", 0, s.clk.Now()), testSessReg, "")
}
