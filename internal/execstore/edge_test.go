package execstore

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

func TestCodecEdgeCases(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-codec-edge", 1, "cmd-codec-edge", 1, now)
	accepted := makeRunAccepted(env, "sess-codec-edge", "")

	// 1. EncodeEvent with zero envelope fields (defaults applied)
	durable, err := EncodeEvent(accepted, &StorageEnvelope{
		EventID:           "evt-1",
		SessionID:         "sess-codec-edge",
		SchemaVersion:     0,
		ProtocolVersion:   0,
		CorrelationID:     "",
		WriterIncarnation: 1,
		QueueRevision:     0,
		RecordedAt:        time.Time{},
		PayloadID:         "",
	})
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}
	if durable.SchemaVersion != CurrentSchemaVersion || durable.ProtocolVersion != CurrentProtocolVersion {
		t.Fatalf("expected current schema/proto versions, got %d/%d", durable.SchemaVersion, durable.ProtocolVersion)
	}
	if !durable.RecordedAt.Equal(now) {
		t.Fatalf("expected recorded at to equal envelope timestamp, got %v", durable.RecordedAt)
	}

	testCodecDecodeAndValidateErrors(t, durable)
}

func testCodecDecodeAndValidateErrors(t *testing.T, validDurable *DurableEvent) {
	t.Helper()
	// 2. Decode unknown event type
	unknownDurable := *validDurable
	unknownDurable.EventType = "NonExistentEvent"
	if _, err := DecodeEvent(&unknownDurable); !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("expected ErrUnknownVersion, got %v", err)
	}

	// 3. Decode corrupt JSON data
	corruptDurable := *validDurable
	corruptDurable.Data = "{invalid-json"
	if _, err := DecodeEvent(&corruptDurable); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory, got %v", err)
	}

	// 4. Non-struct event via typed nil
	var nilEvt *kernel.RunAcceptedEvent
	if err := validateEventMetadata(nilEvt); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for nil event struct, got %v", err)
	}

	// 5. Bad field kind
	fnVal := reflect.ValueOf(func() {})
	if err := validateSafeMetadataValue("badFuncField", fnVal); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for func field, got %v", err)
	}

	// 6. instantiateToolEventB default branch
	if evt := instantiateToolEventB("UnknownToolEvent"); evt != nil {
		t.Fatalf("expected nil for unknown tool event, got %v", evt)
	}
}

func TestCommitEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, _ := newTestStore(ctx, t, &fakeEquality{})

	// Switch journal_mode to WAL to test unsafe pragma detection
	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL;"); err != nil {
		t.Fatalf("exec pragma: %v", err)
	}
	_, _, err := s.ResolveCommit(ctx, "cmd-any")
	if !errors.Is(err, ErrUnsafePragmas) {
		t.Fatalf("expected ErrUnsafePragmas, got %v", err)
	}

	// Nil store and nil DB checks
	var nilStore *Store
	if _, _, nilErr := nilStore.ResolveCommit(ctx, "cmd-any"); !errors.Is(nilErr, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on nil store, got %v", nilErr)
	}

	// Cancelled context check
	cancCtx, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, cancErr := s.ResolveCommit(cancCtx, "cmd-any"); !errors.Is(cancErr, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on cancelled ctx, got %v", cancErr)
	}
}

func TestAppendRecheckEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := newTestStore(ctx, t, &fakeEquality{})
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-append-edge", 1, "cmd-edge-1", 1, now)
	accepted := makeRunAccepted(env, "sess-edge", "")

	// 1. ExpectedAdmissionRevision != 1 when admission row does not exist
	req1 := &AppendRequest{
		SessionID:                 "sess-edge",
		RunID:                     "run-append-edge",
		CommandID:                 "cmd-edge-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 5,
		Events:                    []kernel.Event{accepted},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, req1); !errors.Is(err, ErrStaleRevision) {
		t.Fatalf("expected ErrStaleRevision, got %v", err)
	}

	// 2. ExpectedRunRevision != 0 when run does not exist
	req2 := *req1
	req2.ExpectedAdmissionRevision = 1
	req2.ExpectedRunRevision = 5
	if _, err := s.Append(ctx, &req2); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}

	// 3. Queue revision mismatch on existing admission
	testAppendQueueFenceMismatch(ctx, t, s, req1)
}

func testAppendQueueFenceMismatch(ctx context.Context, t *testing.T, s *Store, baseReq *AppendRequest) {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-append-edge", 1, "cmd-edge-valid", 1, now)
	accepted := makeRunAccepted(env, "sess-edge", "")

	validReq := *baseReq
	validReq.CommandID = "cmd-edge-valid"
	validReq.ExpectedAdmissionRevision = 1
	validReq.ExpectedRunRevision = 0
	validReq.Events = []kernel.Event{accepted}
	if _, err := s.Append(ctx, &validReq); err != nil {
		t.Fatalf("valid append failed: %v", err)
	}

	// Now try an append with mismatched queue fence
	advEnv := makeEnvelope("run-append-edge", 2, "cmd-edge-stale-q", 1, now)
	runAdv := makeRunAdmitted(advEnv, 1)
	reqMismatchedQueue := AppendRequest{
		SessionID:                 "sess-edge",
		RunID:                     "run-append-edge",
		CommandID:                 "cmd-edge-stale-q",
		Kind:                      testKindAdvanceRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            nil,
		QueueFence:                &QueueFence{QueueRevision: 99},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{runAdv},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, &reqMismatchedQueue); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence on queue revision mismatch, got %v", err)
	}
}

func TestAppendPurgingAndTxReceiptConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, _ := newTestStore(ctx, t, &fakeEquality{})
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-purge-test", 1, "cmd-purge-1", 1, now)
	accepted := makeRunAccepted(env, "sess-purging", "")

	// Insert purge record into exec_purges
	_, err := db.ExecContext(
		ctx,
		`INSERT INTO exec_purges (session_id, requested_at)
		 VALUES ('sess-purging', ?)`,
		now.Format(time.RFC3339Nano),
	)
	if err != nil {
		t.Fatalf("insert purge: %v", err)
	}

	req := &AppendRequest{
		SessionID:                 "sess-purging",
		RunID:                     "run-purge-test",
		CommandID:                 "cmd-purge-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{accepted},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, appendErr := s.Append(ctx, req); !errors.Is(appendErr, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging, got %v", appendErr)
	}

	testTxReceiptConflict(ctx, t, s, db)
}

func testTxReceiptConflict(ctx context.Context, t *testing.T, s *Store, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-tx-race", 1, "cmd-tx-race", 1, now)
	accepted := makeRunAccepted(env, "sess-tx-race", "")

	req := &AppendRequest{
		SessionID:                 "sess-tx-race",
		RunID:                     "run-tx-race",
		CommandID:                 "cmd-tx-race",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{accepted},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("acquire conn: %v", err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			return
		}
	}()

	// Insert receipt inside connection
	if insErr := insertReceipt(ctx, conn, req, now); insErr != nil {
		t.Fatalf("insertReceipt failed: %v", insErr)
	}

	outcome, err := s.executeAppendTx(ctx, conn, req)
	if err != nil {
		t.Fatalf("executeAppendTx failed: %v", err)
	}
	if !outcome.Duplicate {
		t.Fatalf("expected Duplicate to be true")
	}
}

func TestRebuildRunCorruptHistoryErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, _ := newTestStore(ctx, t, &fakeEquality{})
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env := makeEnvelope("run-rebuild-corrupt", 1, "cmd-rebuild-1", 1, now)
	accepted := makeRunAccepted(env, "sess-rebuild", "")

	req := &AppendRequest{
		SessionID:                 "sess-rebuild",
		RunID:                     "run-rebuild-corrupt",
		CommandID:                 "cmd-rebuild-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{accepted},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatalf("append failed: %v", err)
	}

	testRebuildMismatchErrors(ctx, t, s, db, "run-rebuild-corrupt")
}

func testRebuildMismatchErrors(ctx context.Context, t *testing.T, s *Store, db *sql.DB, runID string) {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	// 1. Rebuild non-existent run
	if _, err := s.RebuildRun(ctx, "run-nonexistent"); !errors.Is(err, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound, got %v", err)
	}

	// 2. Corrupt stored last_sequence in exec_runs
	if _, err := db.ExecContext(ctx, "UPDATE exec_runs SET revision = revision + 1, last_sequence = 999 WHERE run_id = ?", runID); err != nil {
		t.Fatalf("update exec_runs last_sequence: %v", err)
	}
	if _, err := s.RebuildRun(ctx, runID); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on sequence mismatch, got %v", err)
	}

	// 3. Test lifecycle mismatch on a second run
	env2 := makeEnvelope("run-rebuild-lc", 1, "cmd-rebuild-lc", 1, now)
	acc2 := makeRunAccepted(env2, "sess-rebuild-lc", "")
	req2 := &AppendRequest{
		SessionID:                 "sess-rebuild-lc",
		RunID:                     "run-rebuild-lc",
		CommandID:                 "cmd-rebuild-lc",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{acc2},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, req2); err != nil {
		t.Fatalf("append req2: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE exec_runs SET revision = revision + 1, lifecycle = 'finished' WHERE run_id = 'run-rebuild-lc'"); err != nil {
		t.Fatalf("update exec_runs lifecycle: %v", err)
	}
	if _, err := s.RebuildRun(ctx, "run-rebuild-lc"); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on lifecycle mismatch, got %v", err)
	}

	// 4. Corrupt timestamp in exec_events via raw insert for sequence 2
	if _, err := db.ExecContext(ctx, `INSERT INTO exec_events (
		run_id, sequence, event_id, session_id, event_type,
		schema_version, protocol_version, command_id, causation_id, correlation_id,
		origin, writer_incarnation, admission_token, queue_revision,
		recorded_at, data
	) VALUES (
		'run-rebuild-corrupt', 2, 'evt-bad-date', 'sess-rebuild', 'RunAccepted',
		1, 1, 'cmd-bd', 'cause-bd', 'corr-1', 'user', 1, 1, 0, 'not-a-valid-date', '{}'
	)`); err != nil {
		t.Fatalf("insert bad event: %v", err)
	}
	if _, err := s.ReadEvents(ctx, "run-rebuild-corrupt", 1, 10); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on invalid timestamp, got %v", err)
	}
}

func TestTranscriptAndFailedProjections(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, _ := newTestStore(ctx, t, &fakeEquality{})
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	env1 := makeEnvelope("run-proj-fail", 1, "cmd-pf-1", 1, now)
	accepted := makeRunAccepted(env1, "sess-proj-fail", "p-input-1")

	// Append run accepted with InputRef non-empty
	p, k := sealedTestPayload(t, "p-input-1", []byte("user-prompt-1"))
	req1 := &AppendRequest{
		SessionID:                 "sess-proj-fail",
		RunID:                     "run-proj-fail",
		CommandID:                 "cmd-pf-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "p-input-1",
		MetadataInput:             nil,
		CanonicalInput:            []byte("user-prompt-1"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{accepted},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{p},
		SealedSessionKey:          &k,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, req1); err != nil {
		t.Fatalf("append req1 failed: %v", err)
	}

	testFailedInvocationsAndTranscriptEntries(ctx, t, s, db)
}

func testFailedInvocationsAndTranscriptEntries(ctx context.Context, t *testing.T, s *Store, db *sql.DB) {
	t.Helper()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	pResp, _ := sealedTestPayload(t, "p-resp-1", []byte("assistant response"))
	pTool, _ := sealedTestPayload(t, "p-tool-1", []byte("tool result"))

	// Model response event with ResponseRef
	env2 := makeEnvelope("run-proj-fail", 2, "cmd-pf-2", 1, now)
	modelResp := makeModelResponseRecorded(env2, "model-inv-1", "attempt-1", "p-resp-1")

	// Tool result event with ResultRef
	env3 := makeEnvelope("run-proj-fail", 3, "cmd-pf-2", 1, now)
	toolRes := makeToolResultRecorded(env3, "call-1", "attempt-1", kernel.ToolResultOutcome("failed"))
	toolRes.ResultRef = "p-tool-1"
	toolRes.SourcePosition = 1

	req2 := &AppendRequest{
		SessionID:                 "sess-proj-fail",
		RunID:                     "run-proj-fail",
		CommandID:                 "cmd-pf-2",
		Kind:                      testKindAdvanceRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 1, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{modelResp, toolRes},
		Result:                    codec.ReceiptResult{FirstSequence: 2, LastSequence: 3, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{pResp, pTool},
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	if _, err := s.Append(ctx, req2); err != nil {
		t.Fatalf("append req2 failed: %v", err)
	}

	verifyTranscriptEntries(ctx, t, db)
}

func verifyTranscriptEntries(ctx context.Context, t *testing.T, db *sql.DB) {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_transcript WHERE session_id = 'sess-proj-fail'").Scan(&count); err != nil {
		t.Fatalf("scan transcript count: %v", err)
	}
	if count != 3 {
		t.Fatalf("expected 3 transcript rows (input, assistant, tool_result), got %d", count)
	}

	rows, err := db.QueryContext(ctx, "SELECT role, payload_id FROM exec_transcript WHERE session_id = 'sess-proj-fail' ORDER BY position ASC")
	if err != nil {
		t.Fatalf("query transcript: %v", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			return
		}
	}()

	var roles, pIDs []string
	for rows.Next() {
		var role, pid sql.NullString
		if scanErr := rows.Scan(&role, &pid); scanErr != nil {
			t.Fatalf("scan transcript: %v", scanErr)
		}
		roles = append(roles, role.String)
		pIDs = append(pIDs, pid.String)
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		t.Fatalf("iterate transcript: %v", rowsErr)
	}

	joinedRoles := strings.Join(roles, ",")
	joinedPIDs := strings.Join(pIDs, ",")
	if joinedRoles != "input,assistant,tool_result" {
		t.Fatalf("unexpected roles: %s", joinedRoles)
	}
	if joinedPIDs != "p-input-1,p-resp-1,p-tool-1" {
		t.Fatalf("unexpected pIDs: %s", joinedPIDs)
	}
}

func TestDirectAdmissionAndFoldEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := newTestStore(ctx, t, &fakeEquality{})
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	testDirectAdmissionFences(t)
	testDirectQueueFences(t)
	testDirectComputeUpdatedAdmission(t)
	testDirectFoldBatchEvents(t, s, now)

	if err := validateMapMetadata("badMap", reflect.ValueOf(map[int]string{1: "val"})); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for int map key, got %v", err)
	}
	if err := s.updateAdmissionRow(ctx, nil, nil, nil, nil); err != nil {
		t.Fatalf("expected nil for nil storedAdmission, got %v", err)
	}
}

func testDirectAdmissionFences(t *testing.T) {
	t.Helper()
	admActive := &storedAdmission{
		activeRunID:      sql.NullString{String: "run-act", Valid: true},
		ownerIncarnation: 1,
		admissionToken:   10,
		revision:         1,
		queueRevision:    0,
		contextEpoch:     0,
		contextRevision:  0,
		requirement:      testRequirementNone,
		startRunID:       sql.NullString{Valid: false},
		causeRunID:       sql.NullString{Valid: false},
		decidedEventID:   sql.NullString{Valid: false},
	}
	if err := validateAdmissionFence(nil, admActive, "run-act"); err != nil {
		t.Fatalf("expected nil for nil fence, got %v", err)
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 10, OwnerIncarnation: 1}, admActive, "run-diff"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence on active run ID mismatch, got %v", err)
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 9, OwnerIncarnation: 1}, admActive, "run-act"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence on token mismatch, got %v", err)
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 10, OwnerIncarnation: 2}, admActive, "run-act"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence on incarnation mismatch, got %v", err)
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 10, OwnerIncarnation: 1}, admActive, "run-act"); err != nil {
		t.Fatalf("expected nil for matching active fence, got %v", err)
	}

	admInactive := &storedAdmission{
		activeRunID:      sql.NullString{Valid: false},
		ownerIncarnation: 1,
		admissionToken:   10,
		revision:         1,
		queueRevision:    0,
		contextEpoch:     0,
		contextRevision:  0,
		requirement:      testRequirementNone,
		startRunID:       sql.NullString{Valid: false},
		causeRunID:       sql.NullString{Valid: false},
		decidedEventID:   sql.NullString{Valid: false},
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 10, OwnerIncarnation: 1}, admInactive, "run-new"); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence when token <= stored token, got %v", err)
	}
	if err := validateAdmissionFence(&AdmissionFence{Token: 11, OwnerIncarnation: 1}, admInactive, "run-new"); err != nil {
		t.Fatalf("expected nil when token > stored token, got %v", err)
	}
}

func testDirectQueueFences(t *testing.T) {
	t.Helper()
	adm := &storedAdmission{
		activeRunID:      sql.NullString{Valid: false},
		ownerIncarnation: 1,
		admissionToken:   1,
		revision:         1,
		queueRevision:    5,
		contextEpoch:     0,
		contextRevision:  0,
		requirement:      testRequirementNone,
		startRunID:       sql.NullString{Valid: false},
		causeRunID:       sql.NullString{Valid: false},
		decidedEventID:   sql.NullString{Valid: false},
	}
	if err := validateQueueFence(nil, adm); err != nil {
		t.Fatalf("expected nil for nil queue fence, got %v", err)
	}
	if err := validateQueueFence(&QueueFence{QueueRevision: 4}, adm); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("expected ErrStaleFence for queue revision mismatch, got %v", err)
	}
	if err := validateQueueFence(&QueueFence{QueueRevision: 5}, adm); err != nil {
		t.Fatalf("expected nil for matching queue revision, got %v", err)
	}
}

func testDirectComputeUpdatedAdmission(t *testing.T) {
	t.Helper()
	adm := &storedAdmission{
		activeRunID:      sql.NullString{String: "run-old", Valid: true},
		ownerIncarnation: 1,
		admissionToken:   1,
		revision:         1,
		queueRevision:    1,
		contextEpoch:     1,
		contextRevision:  1,
		requirement:      testRequirementNone,
		startRunID:       sql.NullString{Valid: false},
		causeRunID:       sql.NullString{Valid: false},
		decidedEventID:   sql.NullString{Valid: false},
	}

	testDirectComputeUpdatedAdmissionState(t, adm)
	testDirectComputeUpdatedAdmissionQueue(t, adm)
}

func testDirectComputeUpdatedAdmissionState(t *testing.T, adm *storedAdmission) {
	t.Helper()
	stateFinished := kernel.NewState()
	stateFinished.Finished = true
	stateFinished.ContextEpoch = 2
	stateFinished.ContextRevision = 3
	stateFinished.ContextRequirement = kernel.ContextRequirementFreshRequired

	reqAdm := &AppendRequest{
		SessionID:                 testSess,
		RunID:                     "run-new",
		CommandID:                 "c",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            &AdmissionFence{Token: 2, OwnerIncarnation: 1},
		QueueFence:                nil,
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    nil,
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	upAdm := computeUpdatedAdmission(reqAdm, stateFinished, adm)
	if upAdm.activeRunID.Valid {
		t.Fatalf("expected activeRunID to be invalid when state.Finished = true")
	}
	if upAdm.contextEpoch != 2 || upAdm.contextRevision != 3 || upAdm.requirement != "fresh_required" {
		t.Fatalf("unexpected context fields: %+v", upAdm)
	}
}

func testDirectComputeUpdatedAdmissionQueue(t *testing.T, adm *storedAdmission) {
	t.Helper()
	reqQ := &AppendRequest{
		SessionID:                 testSess,
		RunID:                     "run-new",
		CommandID:                 "c",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "",
		MetadataInput:             []byte("{}"),
		CanonicalInput:            []byte("{}"),
		AdmissionFence:            nil,
		QueueFence:                &QueueFence{QueueRevision: 1},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    nil,
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            nil,
		SealedSessionKey:          nil,
		CorrelationID:             "",
	}
	upQ := computeUpdatedAdmission(reqQ, kernel.NewState(), adm)
	if upQ.queueRevision != 2 {
		t.Fatalf("expected queueRevision = 2, got %d", upQ.queueRevision)
	}
}

func testDirectFoldBatchEvents(t *testing.T, s *Store, now time.Time) {
	t.Helper()
	if _, err := s.foldBatchEvents(0, []kernel.Event{nil}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch on nil event, got %v", err)
	}

	env := makeEnvelope("run-fold", 2, "cmd-fold", 1, now)
	evt := makeRunAccepted(env, "sess-fold", "")
	if _, err := s.foldBatchEvents(0, []kernel.Event{evt}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch on sequence gap, got %v", err)
	}
}
