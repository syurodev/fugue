package execstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func TestRegressionFollowupA_NestedMetadataCannotCarryPrompt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	const secret = "REVIEW_SYNTHETIC_PRIVATE_CONTENT"
	s, db, _ := newTestStore(ctx, t, nil)
	req := reviewReq("review-accept-a", 1, 0, 1, reviewAccepted(s))
	req.MetadataInput = []byte(`{"InputRef":{"Prompt":"` + secret + `"}}`)
	req.CanonicalInput = req.MetadataInput
	_, err := s.Append(ctx, req)
	if err == nil {
		t.Fatal("expected Append to reject nested metadata payload")
	}
	var count int
	if queryErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_receipts WHERE command_id='review-accept-a'`).Scan(&count); queryErr != nil {
		t.Fatal(queryErr)
	}
	if count != 0 {
		t.Errorf("expected 0 receipts, got %d", count)
	}
}

type dbReadingEquality struct {
	db     *sql.DB
	input  []byte
	called bool
}

var errDBReadingFailed = errors.New("db reading failed")

func (e *dbReadingEquality) OpenPayload(ctx context.Context, _, _ string) ([]byte, error) {
	e.called = true
	var count int
	if err := e.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_payloads`).Scan(&count); err != nil {
		return nil, fmt.Errorf("%w: %w", errDBReadingFailed, err)
	}
	return e.input, nil
}

func TestRegressionFollowupB_ProtectedRaceReleasesConnection(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)
	input := []byte(`{"Prompt":"protected"}`)
	p, key := sealedTestPayload(t, "review-payload-b", input)
	req := reviewReq("review-accept-b", 1, 0, 1, makeRunAccepted(makeEnvelope("review-run-b", 1, "review-accept-b", 0, s.clk.Now()), "review-session-b", "review-payload-b"))
	req.RunID = "review-run-b"
	req.SessionID = "review-session-b"
	req.MetadataInput = nil
	req.CanonicalInput = input
	req.ProtectedInputID = p.PayloadID
	req.SealedPayloads = []SealedPayload{p}
	req.SealedSessionKey = &key
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}
	eq := &dbReadingEquality{db: db, input: input, called: false}
	s.equality = eq
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, execErr := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); execErr != nil {
		t.Fatal(execErr)
	}
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := s.executeAppendTx(callCtx, conn, req)
	if closeErr := conn.Close(); closeErr != nil && !errors.Is(closeErr, sql.ErrConnDone) {
		t.Fatal(closeErr)
	}
	if !eq.called {
		t.Fatal("opener not exercised")
	}
	if err != nil || !out.Duplicate {
		t.Errorf("opener blocked by retained connection lease: duplicate=%v err=%v", out.Duplicate, err)
	}
}

func TestRegressionFollowupC_TranscriptUsesAcceptedInputRef(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)
	cmd, key := sealedTestPayload(t, "review-command-input-c", []byte(`{"InputRef":"review-user-input-c"}`))
	user, _ := sealedTestPayload(t, "review-user-input-c", []byte("actual user prompt"))
	acc := makeRunAccepted(makeEnvelope("review-run-c", 1, "review-accept-c", 0, s.clk.Now()), "review-session-c", user.PayloadID)
	req := reviewReq("review-accept-c", 1, 0, 1, acc)
	req.RunID = "review-run-c"
	req.SessionID = "review-session-c"
	req.MetadataInput = nil
	req.CanonicalInput = []byte(`{"InputRef":"review-user-input-c"}`)
	req.ProtectedInputID = cmd.PayloadID
	req.SealedPayloads = []SealedPayload{cmd, user}
	req.SealedSessionKey = &key
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}
	admit := makeRunAdmitted(makeEnvelope("review-run-c", 2, "review-admit-c", 1, s.clk.Now()), 1)
	admit.ContextEpoch = 1
	req = reviewReq("review-admit-c", 2, 1, 1, admit)
	req.RunID = "review-run-c"
	req.SessionID = "review-session-c"
	req.Kind = "AdmitRun"
	req.QueueFence = nil
	req.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}
	var inputRef string
	if err := db.QueryRowContext(ctx, `SELECT payload_id FROM exec_transcript WHERE role='input' AND context_epoch=1 AND session_id='review-session-c'`).Scan(&inputRef); err != nil {
		t.Fatal(err)
	}
	if inputRef != user.PayloadID {
		t.Errorf("transcript points to %q, want user input ref %q", inputRef, user.PayloadID)
	}
}

func TestRegressionFollowupD_QueuedDispatchRejected(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)
	if _, err := s.Append(ctx, reviewReq("review-accept-d", 1, 0, 1, reviewAccepted(s))); err != nil {
		t.Fatal(err)
	}
	evt := &kernel.ModelDispatchStartedEvent{
		Envelope:            *makeEnvelope(testRunReg, 2, "review-dispatch-d", 999, s.clk.Now()),
		InvocationID:        "review-model-d",
		AttemptID:           "review-attempt-d",
		Token:               0,
		ExecutorFence:       999,
		ReservationTokens:   0,
		ProviderSettingsRef: "",
		AccountID:           "",
		CatalogVersion:      "",
	}
	req := reviewReq("review-dispatch-d", 2, 1, 1, evt)
	result, err := s.Append(ctx, req)
	if err == nil {
		var n int
		if scanErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_events WHERE event_type='ModelDispatchStarted'`).Scan(&n); scanErr != nil {
			t.Fatal(scanErr)
		}
		t.Errorf("queue-only batch committed dispatch: committed=%v events=%d", result.Committed, n)
	}
}

func TestRegressionFollowupE_RecoveredPoolStillEnforcesPragmas(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)
	if _, err := db.ExecContext(ctx, `PRAGMA synchronous=OFF`); err != nil {
		t.Fatal(err)
	}
	status, _, err := s.ResolveCommit(ctx, "review-missing-e")
	if err == nil || status != CommitStatusUnavailable {
		t.Fatal("expected unsafe recovered connection to be discarded")
	}
	req := reviewReq("review-accept-e", 1, 0, 1, reviewAccepted(s))
	out, err := s.Append(ctx, req)
	if err != nil {
		t.Fatalf("append on replacement connection failed: %v", err)
	}
	if !out.Committed {
		t.Fatalf("expected append to commit, got duplicate=%v", out.Duplicate)
	}
	var fk int
	if queryErr := db.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&fk); queryErr != nil {
		t.Fatal(queryErr)
	}
	if fk != 1 {
		t.Errorf("replacement pooled connection committed with foreign_keys=%d", fk)
	}
}

func TestRegressionFollowupF_RetryCanonicalizationIsStable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)
	req := reviewReq("review-accept-f", 1, 0, 1, reviewAccepted(s))
	req.MetadataInput = []byte(`{"InputRef": ""}`)
	req.CanonicalInput = req.MetadataInput
	retry := *req
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatal(err)
	}
	out, err := s.Append(ctx, &retry)
	if err != nil || !out.Duplicate {
		t.Errorf("identical reconstructed retry falsely conflicts: duplicate=%v err=%v", out.Duplicate, err)
	}
}

func TestMetadataValidationMatrixCoverage(t *testing.T) {
	t.Parallel()
	validCases := []struct {
		kind string
		json string
	}{
		{cmdAcceptRun, `{"InputRef":"ref"}`},
		{cmdAdmitRun, `{"token":1}`},
		{cmdAcquireRunOwnership, `{"OwnerIncarnation":2}`},
		{cmdAdvanceRun, `{"StepIndex":3}`},
		{cmdRequestStop, `{"stop":"user_cancel","Reason":"user_cancel","Mode":"terminal"}`},
		{cmdRequestStop, `{"Reason":"fatal_error","Mode":"suspend"}`},
		{cmdRequestStop, `{"Reason":"budget"}`},
		{cmdRequestStop, `{"Reason":"parent_cancel"}`},
		{cmdRequestStop, `{"Reason":"shutdown"}`},
		{cmdCloseRunWithoutPayload, `{"RunID":"r1"}`},
		{cmdSelectRunContext, `{"Epoch":1}`},
		{cmdResumeRun, `{"RunRevision":1}`},
		{cmdRecoverRun, `{"RunRevision":1}`},
	}
	for _, tc := range validCases {
		if _, err := parseAndValidateMetadata(tc.kind, []byte(tc.json)); err != nil {
			t.Errorf("expected valid metadata for %s %s, got: %v", tc.kind, tc.json, err)
		}
	}

	invalidCases := []struct {
		kind string
		json string
	}{
		{"UnknownCommand", `{}`},
		{cmdRequestStop, `{"Reason":"invalid_reason"}`},
		{cmdRequestStop, `{"Mode":"invalid_mode"}`},
		{cmdAcceptRun, `invalid_json`},
		{cmdAcceptRun, `"not_an_object"`},
	}
	for _, tc := range invalidCases {
		if _, err := parseAndValidateMetadata(tc.kind, []byte(tc.json)); err == nil {
			t.Errorf("expected error for %s %s, got nil", tc.kind, tc.json)
		}
	}
}

func TestQueueFenceEnvelopesAndAuthorityCoverage(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	// 1. AdmitRun under QueueFence rejected
	reqAdmit := reviewReq("c-cov-admit", 1, 0, 1, reviewAccepted(s))
	reqAdmit.Kind = cmdAdmitRun
	if _, err := s.Append(ctx, reqAdmit); !errors.Is(err, ErrStaleFence) {
		t.Errorf("expected ErrStaleFence for AdmitRun under QueueFence, got %v", err)
	}

	// 2. AcceptRun with non-accepted event rejected
	evtOther := &kernel.RunFinishedEvent{
		Envelope:               *makeEnvelope(testRunReg, 1, "c-cov-wrong", 0, s.clk.Now()),
		Outcome:                "",
		Truncated:              false,
		PrimaryStopRef:         "",
		SecondaryStopRefs:      nil,
		UnresolvedExposureRefs: nil,
	}
	reqWrongEvt := reviewReq("c-cov-wrong", 1, 0, 1, evtOther)
	reqWrongEvt.Kind = cmdAcceptRun
	if _, err := s.Append(ctx, reqWrongEvt); !errors.Is(err, ErrStaleFence) {
		t.Errorf("expected ErrStaleFence for AcceptRun with non-accepted event, got %v", err)
	}

	// 3. Nil event rejected
	reqNilEvt := reviewReq("c-cov-nil", 1, 0, 1)
	reqNilEvt.Events = []kernel.Event{nil}
	if _, err := s.Append(ctx, reqNilEvt); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for nil event, got %v", err)
	}

	// 4. Mismatched RunID in envelope rejected
	evtDiffRun := makeRunAccepted(makeEnvelope("diff-run", 1, "c-cov-diff", 0, s.clk.Now()), testSessReg, "")
	reqDiffRun := reviewReq("c-cov-diff", 1, 0, 1, evtDiffRun)
	if _, err := s.Append(ctx, reqDiffRun); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for mismatched envelope RunID, got %v", err)
	}
}

func TestMetadataPermittedMatrix(t *testing.T) {
	t.Parallel()
	kinds := []string{
		cmdAcceptRun, cmdAdmitRun, cmdAcquireRunOwnership, cmdAdvanceRun,
		cmdRequestStop, cmdCloseRunWithoutPayload, cmdSelectRunContext,
		cmdResumeRun, cmdRecoverRun,
	}
	for _, k := range kinds {
		if !isMetadataOnlyPermitted(k) {
			t.Errorf("expected %s to be metadata-only permitted", k)
		}
	}
}

func TestValidationFencesEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	// Neither protected nor metadata provided
	reqNoInput := reviewReq("c-cov-noinput", 1, 0, 1, reviewAccepted(s))
	reqNoInput.MetadataInput = nil
	reqNoInput.ProtectedInputID = ""
	if _, err := s.Append(ctx, reqNoInput); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for no input, got %v", err)
	}

	// Both AdmissionFence and QueueFence provided
	reqBothFences := reviewReq("c-cov-bothfences", 1, 0, 1, reviewAccepted(s))
	reqBothFences.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := s.Append(ctx, reqBothFences); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for both fences, got %v", err)
	}

	// Queue-fence disallowed event on non-AcceptRun command
	reqStopWithModel := reviewReq("c-cov-stopmodel", 1, 0, 1, &kernel.ModelDispatchStartedEvent{
		Envelope:            *makeEnvelope(testRunReg, 1, "c-cov-stopmodel", 0, s.clk.Now()),
		InvocationID:        "m1",
		AttemptID:           "a1",
		Token:               0,
		ExecutorFence:       0,
		ReservationTokens:   0,
		ProviderSettingsRef: "",
		AccountID:           "",
		CatalogVersion:      "",
	})
	reqStopWithModel.Kind = cmdRequestStop
	if _, err := s.Append(ctx, reqStopWithModel); !errors.Is(err, ErrStaleFence) {
		t.Errorf("expected ErrStaleFence, got %v", err)
	}
}

func TestValidationIdentityEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	reqMissing := reviewReq("c-cov-miss", 1, 0, 1, reviewAccepted(s))
	reqMissing.Principal = ""
	if _, err := s.Append(ctx, reqMissing); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for empty principal, got %v", err)
	}
	reqMissing = reviewReq("c-cov-miss2", 1, 0, 1, reviewAccepted(s))
	reqMissing.Target = ""
	if _, err := s.Append(ctx, reqMissing); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for empty target, got %v", err)
	}
	reqMissing = reviewReq("c-cov-miss3", 1, 0, 1, reviewAccepted(s))
	reqMissing.CodecVersion = ""
	if _, err := s.Append(ctx, reqMissing); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for empty codec version, got %v", err)
	}
}

func TestInBatchAcceptedAndAdmitted(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)

	user, key := sealedTestPayload(t, "user-inbatch", []byte("in-batch prompt"))
	acc := makeRunAccepted(makeEnvelope("run-inbatch", 1, "c-inbatch", 1, s.clk.Now()), "sess-inbatch", user.PayloadID)
	adm := makeRunAdmitted(makeEnvelope("run-inbatch", 2, "c-inbatch", 1, s.clk.Now()), 1)
	reqInBatch := &AppendRequest{
		CommandID:                 "c-inbatch",
		SessionID:                 "sess-inbatch",
		RunID:                     "run-inbatch",
		Kind:                      cmdAdmitRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              "v1",
		Events:                    []kernel.Event{acc, adm},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 2, RejectionCode: "", Message: ""},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		ProtectedInputID:          user.PayloadID,
		CanonicalInput:            []byte("in-batch prompt"),
		SealedPayloads:            []SealedPayload{user},
		SealedSessionKey:          &key,
	}
	if _, err := s.Append(ctx, reqInBatch); err != nil {
		t.Fatalf("same batch accept+admit failed: %v", err)
	}
}

func TestNilStoreReceiverEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sNoEq, _, _ := newTestStore(ctx, t, nil)
	p, key2 := sealedTestPayload(t, "pay-noeq", []byte("noeq"))
	reqProt := reviewReq("c-cov-prot", 1, 0, 1, makeRunAccepted(makeEnvelope("run-noeq", 1, "c-cov-prot", 0, sNoEq.clk.Now()), "sess-noeq", p.PayloadID))
	reqProt.RunID = "run-noeq"
	reqProt.SessionID = "sess-noeq"
	reqProt.MetadataInput = nil
	reqProt.CanonicalInput = []byte("noeq")
	reqProt.ProtectedInputID = p.PayloadID
	reqProt.SealedPayloads = []SealedPayload{p}
	reqProt.SealedSessionKey = &key2
	if _, err := sNoEq.Append(ctx, reqProt); err != nil {
		t.Fatal(err)
	}
	retryProt := *reqProt
	if _, err := sNoEq.Append(ctx, &retryProt); !errors.Is(err, ErrProtectedInputUnavailable) {
		t.Errorf("expected ErrProtectedInputUnavailable when equality nil, got %v", err)
	}

	eqDiff := &fakeEquality{payloads: map[string][]byte{"sess-noeq/" + p.PayloadID: []byte("diff-content")}}
	sNoEq.equality = eqDiff
	if _, err := sNoEq.Append(ctx, &retryProt); !errors.Is(err, ErrReceiptConflict) {
		t.Errorf("expected ErrReceiptConflict when protected content mismatches, got %v", err)
	}

	if _, err := (*Store)(nil).Append(ctx, reqProt); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable for nil store, got %v", err)
	}
	if _, err := sNoEq.Append(ctx, nil); !errors.Is(err, ErrInvalidBatch) {
		t.Errorf("expected ErrInvalidBatch for nil request, got %v", err)
	}
	if _, _, err := (*Store)(nil).LookupReceipt(ctx, "c"); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable for nil LookupReceipt, got %v", err)
	}
	if _, err := (*Store)(nil).ReadRun(ctx, "r"); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable for nil ReadRun, got %v", err)
	}
	if _, err := (*Store)(nil).ReadEvents(ctx, "r", 1, 10); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable for nil ReadEvents, got %v", err)
	}
	if _, err := (*Store)(nil).RebuildRun(ctx, "r"); !errors.Is(err, ErrStoreUnavailable) {
		t.Errorf("expected ErrStoreUnavailable for nil RebuildRun, got %v", err)
	}
}

func TestAdmissionFenceMismatchEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	sAdm, _, _ := newTestStore(ctx, t, nil)
	if _, err := sAdm.Append(ctx, reviewReq("c-adm-base", 1, 0, 1, reviewAccepted(sAdm))); err != nil {
		t.Fatal(err)
	}

	admEvtBadToken := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-bad-tok", 1, sAdm.clk.Now()), 999)
	reqBadTok := reviewReq("c-bad-tok", 2, 1, 1, admEvtBadToken)
	reqBadTok.Kind = cmdAdmitRun
	reqBadTok.QueueFence = nil
	reqBadTok.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := sAdm.Append(ctx, reqBadTok); !errors.Is(err, ErrStaleFence) {
		t.Errorf("expected ErrStaleFence for admission token mismatch, got %v", err)
	}

	admEvtBadInc := makeRunAdmitted(makeEnvelope(testRunReg, 2, "c-bad-inc", 1, sAdm.clk.Now()), 1)
	admEvtBadInc.OwnerIncarnation = 999
	reqBadInc := reviewReq("c-bad-inc", 2, 1, 1, admEvtBadInc)
	reqBadInc.Kind = cmdAdmitRun
	reqBadInc.QueueFence = nil
	reqBadInc.AdmissionFence = &AdmissionFence{Token: 1, OwnerIncarnation: 1}
	if _, err := sAdm.Append(ctx, reqBadInc); !errors.Is(err, ErrStaleFence) {
		t.Errorf("expected ErrStaleFence for owner incarnation mismatch, got %v", err)
	}
}
