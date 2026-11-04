package execstore

import (
	"errors"
	"testing"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func TestRegressionSameBatchInputExactlyOnce(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, db, _ := newTestStore(ctx, t, nil)

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
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM exec_transcript WHERE session_id='sess-inbatch' AND role='input'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("input transcript rows = %d, want 1", count)
	}
}

func TestRegressionInitialAdmissionRejectsForeignHost(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name                                 string
		token, owner, eventToken, eventOwner uint64
	}{
		{"foreign_host", 1, 999, 1, 999},
		{"event_token_mismatch", 1, 1, 999, 1},
		{"event_owner_mismatch", 1, 1, 1, 999},
		{"zero_token", 0, 1, 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s, db, _ := newTestStore(ctx, t, nil)
			admitted := makeRunAdmitted(makeEnvelope(testRunReg, 2, "first-admit", tc.eventToken, s.clk.Now()), tc.eventToken)
			admitted.OwnerIncarnation = tc.eventOwner
			req := reviewReq("first-admit", 1, 0, 1, reviewAccepted(s), admitted)
			req.Kind = cmdAdmitRun
			req.QueueFence = nil
			req.AdmissionFence = &AdmissionFence{Token: tc.token, OwnerIncarnation: tc.owner}
			if _, err := s.Append(ctx, req); !errors.Is(err, ErrStaleFence) {
				t.Fatalf("initial admission must reject stale authority: %v", err)
			}
			for _, table := range []string{"exec_admission", "exec_events", "exec_receipts"} {
				var count int
				if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 0 {
					t.Fatalf("rejected admission wrote %s", table)
				}
			}
		})
	}
}

func TestRegressionQueueCannotStartStep(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)
	if _, err := s.Append(ctx, reviewReq("queue-accept", 1, 0, 1, reviewAccepted(s))); err != nil {
		t.Fatal(err)
	}
	evt := makeStepStarted(makeEnvelope(testRunReg, 2, "queue-step", 999, s.clk.Now()), 2)
	req := reviewReq("queue-step", 2, 1, 1, evt)
	req.Kind = cmdAdvanceRun
	if out, err := s.Append(ctx, req); !errors.Is(err, ErrStaleFence) {
		t.Errorf("queue-only StepStarted committed without admission: %v", out.Committed)
	}
}

func TestRegressionQueueAuthorityRejectsExecution(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{cmdAcceptRun, cmdRequestStop, cmdCloseRunWithoutPayload, cmdAdvanceRun, cmdAcquireRunOwnership, cmdRecoverRun} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			s, db, _ := newTestStore(ctx, t, nil)
			if _, err := s.Append(ctx, reviewReq("q-accept", 1, 0, 1, reviewAccepted(s))); err != nil {
				t.Fatal(err)
			}
			evt := makeStepStarted(makeEnvelope(testRunReg, 2, "q-execution", 0, s.clk.Now()), 1)
			req := reviewReq("q-execution", 2, 1, 1, evt)
			req.Kind = kind
			if _, err := s.Append(ctx, req); !errors.Is(err, ErrStaleFence) {
				t.Fatalf("execution through queue authority: %v", err)
			}
			var count int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_events").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 1 {
				t.Fatal("rejected queued execution changed history")
			}
		})
	}
}

func TestRegressionQueueCannotWriteAdmittedRun(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)
	runR1AcceptAndAdmit(t, ctx, s)
	evt := makeRunFinished(makeEnvelope(testRunReg, 3, "q-stop-active", 0, s.clk.Now()), kernel.RunOutcomeCancelled)
	req := reviewReq("q-stop-active", 3, 2, 2, evt)
	req.Kind = cmdRequestStop
	if _, err := s.Append(ctx, req); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("admitted run modified with queue authority: %v", err)
	}
}

func TestRegressionQueueRejectsNonzeroEventToken(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	s, _, _ := newTestStore(ctx, t, nil)
	evt := reviewAccepted(s)
	evt.Envelope.Token = 999
	if _, err := s.Append(ctx, reviewReq("q-nonzero", 1, 0, 1, evt)); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("queued accept with admission token: %v", err)
	}
}
