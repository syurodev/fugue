package execstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

const (
	testFenceRun1  = "run-1"
	testFenceSess  = "sess-fence"
	testReplayRun  = "run-replay"
	testReplaySess = "sess-replay"
	testSharedSess = "sess-shared"
	testActiveRun  = "run-active"
	testQueuedRun  = "run-queued"
	testRepCmd1    = "cmd-rep-1"
	testRepCmd2    = "cmd-rep-2"
)

func setupFenceRun(ctx context.Context, t *testing.T, now time.Time) *Store {
	t.Helper()
	s, _, _ := newTestStore(ctx, t, nil)

	evt1 := makeRunAccepted(makeEnvelope(testFenceRun1, 1, "cmd-1", 0, now), testFenceSess, "pay-1")
	payload, key := sealedTestPayload(t, "pay-1", []byte("in"))
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 testFenceSess,
		RunID:                     testFenceRun1,
		CommandID:                 "cmd-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-1",
		CanonicalInput:            []byte("in"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt1},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	})
	if err != nil {
		t.Fatalf("setup Append 1 failed: %v", err)
	}

	evt2 := makeRunAdmitted(makeEnvelope(testFenceRun1, 2, "cmd-2", 5, now), 5)
	_, err = s.Append(ctx, &AppendRequest{
		SessionID:                 testFenceSess,
		RunID:                     testFenceRun1,
		CommandID:                 "cmd-2",
		Kind:                      testKindAdmitRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte(`{"token":5}`),
		CanonicalInput:            []byte(`{"token":5}`),
		AdmissionFence:            &AdmissionFence{Token: 5, OwnerIncarnation: 1},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt2},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("setup Append 2 (admit) failed: %v", err)
	}
	return s
}

type staleFenceCase struct {
	name      string
	admFence  *AdmissionFence
	expAdmRev uint64
	expRunRev uint64
	token     uint64
	wantErr   error
}

func staleFenceCases() []staleFenceCase {
	return []staleFenceCase{
		{
			name:      "stale admission token is rejected",
			admFence:  &AdmissionFence{Token: 4, OwnerIncarnation: 1},
			expAdmRev: 2,
			expRunRev: 2,
			token:     4,
			wantErr:   ErrStaleFence,
		},
		{
			name:      "wrong owner incarnation is rejected",
			admFence:  &AdmissionFence{Token: 5, OwnerIncarnation: 999},
			expAdmRev: 2,
			expRunRev: 2,
			token:     5,
			wantErr:   ErrStaleFence,
		},
		{
			name:      "stale admission revision is rejected",
			admFence:  &AdmissionFence{Token: 5, OwnerIncarnation: 1},
			expAdmRev: 1,
			expRunRev: 2,
			token:     5,
			wantErr:   ErrStaleRevision,
		},
		{
			name:      "stale run revision is rejected",
			admFence:  &AdmissionFence{Token: 5, OwnerIncarnation: 1},
			expAdmRev: 2,
			expRunRev: 1,
			token:     5,
			wantErr:   ErrStaleRevision,
		},
	}
}

func TestExecutionRejectsStaleFenceAndRevision(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := setupFenceRun(ctx, t, now)

	for _, tc := range staleFenceCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			evt := makeStepStarted(makeEnvelope(testFenceRun1, 3, "cmd-tc", tc.token, now), 1)
			_, appendErr := s.Append(ctx, &AppendRequest{
				SessionID:                 testFenceSess,
				RunID:                     testFenceRun1,
				CommandID:                 "cmd-tc",
				Kind:                      testKindAdvanceRun,
				Principal:                 testOriginUser,
				Target:                    testTargetSession,
				CodecVersion:              codec.CodecV1,
				MetadataInput:             []byte(`{}`),
				CanonicalInput:            []byte(`{}`),
				AdmissionFence:            tc.admFence,
				ExpectedRunRevision:       tc.expRunRev,
				ExpectedAdmissionRevision: tc.expAdmRev,
				Events:                    []kernel.Event{evt},
				Result:                    codec.ReceiptResult{FirstSequence: 3, LastSequence: 3, RejectionCode: "", Message: ""},
			})
			if !errors.Is(appendErr, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, appendErr)
			}
		})
	}
}

func admitActiveRun(ctx context.Context, t *testing.T, s *Store, now time.Time) {
	t.Helper()
	payload1, key1 := sealedTestPayload(t, "pay-act", []byte("act"))
	evt1 := makeRunAccepted(makeEnvelope(testActiveRun, 1, "cmd-act-1", 0, now), testSharedSess, "pay-act")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 testSharedSess,
		RunID:                     testActiveRun,
		CommandID:                 "cmd-act-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-act",
		CanonicalInput:            []byte("act"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt1},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload1},
		SealedSessionKey:          &key1,
	})
	if err != nil {
		t.Fatalf("setup run-active failed: %v", err)
	}

	evt2 := makeRunAdmitted(makeEnvelope(testActiveRun, 2, "cmd-act-2", 10, now), 10)
	_, err = s.Append(ctx, &AppendRequest{
		SessionID:                 testSharedSess,
		RunID:                     testActiveRun,
		CommandID:                 "cmd-act-2",
		Kind:                      testKindAdmitRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte(`{"token":10}`),
		CanonicalInput:            []byte(`{"token":10}`),
		AdmissionFence:            &AdmissionFence{Token: 10, OwnerIncarnation: 1},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt2},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("admit run-active failed: %v", err)
	}
}

func setupActiveAndQueuedRuns(ctx context.Context, t *testing.T, now time.Time) (*Store, *sql.DB) {
	t.Helper()
	s, db, _ := newTestStore(ctx, t, nil)
	admitActiveRun(ctx, t, s, now)

	payload2, _ := sealedTestPayload(t, "pay-queued", []byte("q"))
	evtQ1 := makeRunAccepted(makeEnvelope(testQueuedRun, 1, "cmd-q-1", 0, now), testSharedSess, "pay-queued")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 testSharedSess,
		RunID:                     testQueuedRun,
		CommandID:                 "cmd-q-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-queued",
		CanonicalInput:            []byte("q"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 2,
		Events:                    []kernel.Event{evtQ1},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload2},
		SealedSessionKey:          nil,
	})
	if err != nil {
		t.Fatalf("accept run-queued failed: %v", err)
	}
	return s, db
}

func TestQueuedFinishKeepsOtherRunAdmission(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db := setupActiveAndQueuedRuns(ctx, t, now)

	// Cancel run-queued with a queued command (using QueueFence)
	evtQ2 := makeRunFinished(makeEnvelope(testQueuedRun, 2, "cmd-q-cancel", 0, now), kernel.RunOutcomeCancelled)
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 testSharedSess,
		RunID:                     testQueuedRun,
		CommandID:                 "cmd-q-cancel",
		Kind:                      "RequestStop",
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte(`{"stop":"cancel"}`),
		CanonicalInput:            []byte(`{"stop":"cancel"}`),
		QueueFence:                &QueueFence{QueueRevision: 1},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 3,
		Events:                    []kernel.Event{evtQ2},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
	})
	if err != nil {
		t.Fatalf("cancel run-queued failed: %v", err)
	}

	var (
		activeRunID sql.NullString
		admToken    uint64
	)
	err = db.QueryRowContext(ctx, "SELECT active_run_id, admission_token FROM exec_admission WHERE session_id = '"+testSharedSess+"'").Scan(&activeRunID, &admToken)
	if err != nil {
		t.Fatalf("query exec_admission: %v", err)
	}

	if !activeRunID.Valid || activeRunID.String != testActiveRun {
		t.Fatalf("queued cancel cleared active run! got %v, want %s", activeRunID, testActiveRun)
	}
	if admToken != 10 {
		t.Fatalf("queued cancel changed admission token! got %d, want 10", admToken)
	}
}

func setupReplayRun(ctx context.Context, t *testing.T, now time.Time) *Store {
	t.Helper()
	s, _, _ := newTestStore(ctx, t, nil)

	payload, key := sealedTestPayload(t, "pay-rep", []byte("replay-input"))
	evt1 := makeRunAccepted(makeEnvelope(testReplayRun, 1, testRepCmd1, 0, now), testReplaySess, "pay-rep")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 testReplaySess,
		RunID:                     testReplayRun,
		CommandID:                 testRepCmd1,
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-rep",
		CanonicalInput:            []byte("replay-input"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt1},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	})
	if err != nil {
		t.Fatalf("step 1 failed: %v", err)
	}

	evt2 := makeRunAdmitted(makeEnvelope(testReplayRun, 2, testRepCmd2, 7, now), 7)
	evt3 := makeStepStarted(makeEnvelope(testReplayRun, 3, testRepCmd2, 7, now), 1)
	evt4 := makeModelRequestPlanned(makeEnvelope(testReplayRun, 4, testRepCmd2, 7, now), "m1", "a1")
	_, err = s.Append(ctx, &AppendRequest{
		SessionID:                 testReplaySess,
		RunID:                     testReplayRun,
		CommandID:                 testRepCmd2,
		Kind:                      testKindAdmitRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		MetadataInput:             []byte(`{"token":7}`),
		CanonicalInput:            []byte(`{"token":7}`),
		AdmissionFence:            &AdmissionFence{Token: 7, OwnerIncarnation: 1},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt2, evt3, evt4},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 4, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
	})
	if err != nil {
		t.Fatalf("step 2 failed: %v", err)
	}
	return s
}

func TestExecutionReplayMatchesProjections(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s := setupReplayRun(ctx, t, now)

	rebuiltState, err := s.RebuildRun(ctx, testReplayRun)
	if err != nil {
		t.Fatalf("RebuildRun failed: %v", err)
	}

	if rebuiltState.LastSequence != 4 || rebuiltState.CurrentStepIndex != 1 {
		t.Fatalf("rebuilt sequence/step mismatch: seq=%d step=%d", rebuiltState.LastSequence, rebuiltState.CurrentStepIndex)
	}
	if kernel.DeriveLifecycle(rebuiltState) != kernel.LifecycleRunning {
		t.Fatalf("rebuilt Lifecycle = %v, want running", kernel.DeriveLifecycle(rebuiltState))
	}
	if kernel.DerivePhase(rebuiltState) != kernel.PhaseCallingModel {
		t.Fatalf("rebuilt Phase = %v, want calling_model", kernel.DerivePhase(rebuiltState))
	}

	runRec, err := s.ReadRun(ctx, testReplayRun)
	if err != nil {
		t.Fatalf("ReadRun failed: %v", err)
	}
	if runRec.LastSequence != rebuiltState.LastSequence || runRec.Lifecycle != kernel.DeriveLifecycle(rebuiltState) {
		t.Fatalf("stored projection mismatch: seq=%d life=%v", runRec.LastSequence, runRec.Lifecycle)
	}
}
