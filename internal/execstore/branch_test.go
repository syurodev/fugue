package execstore

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

const (
	testBranchRunRev     = "r-rev"
	testBranchRunFin     = "r-fin"
	testBranchCmdFin1    = "c-fin-1"
	testBranchRunAct     = "r-act"
	testBranchCmdAct1    = "c-act-1"
	testBranchRunMComp   = "r-m-comp"
	testBranchRunMClose  = "r-m-close"
	testBranchCmdClose1  = "c-cls-1"
	testBranchRunToolCls = "r-tool-cls"
	testBranchRunCAS     = "r-cas"
)

func TestStoreNilStoreMethods(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	var nilStore *Store
	if _, err := nilStore.ReadRun(ctx, "r"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on nil Store.ReadRun, got %v", err)
	}
	if _, err := nilStore.ReadEvents(ctx, "r", 1, 10); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on nil Store.ReadEvents, got %v", err)
	}
	if _, _, err := nilStore.LookupReceipt(ctx, "c"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on nil Store.LookupReceipt, got %v", err)
	}
	if _, _, err := nilStore.ResolveCommit(ctx, "c"); !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on nil Store.ResolveCommit, got %v", err)
	}
}

func TestStoreReadEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	// toSequence < fromSequence
	if _, err := s.ReadEvents(ctx, "r", 10, 5); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch when toSequence < fromSequence, got %v", err)
	}

	// Insert run and sequence 1 event
	evt1 := makeRunAccepted(makeEnvelope("r-gap", 1, "c-gap-1", 0, now), "sess-gap", "")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-gap", RunID: "r-gap", CommandID: "c-gap-1", Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 0}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evt1}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append seq 1 failed: %v", err)
	}

	// ReadEvents gap detection: querying from sequence 0 when events start at 1
	_, errGap := s.ReadEvents(ctx, "r-gap", 0, 1)
	if !errors.Is(errGap, ErrSequenceGap) {
		t.Fatalf("expected ErrSequenceGap when seq 0 is missing, got %v", errGap)
	}

	// ReadRun corrupt wait_reasons JSON (valid JSON array of numbers, but unmarshal into struct fails)
	_, execErr := db.ExecContext(ctx, "UPDATE exec_runs SET revision = revision + 1, wait_reasons = '[123]' WHERE run_id = 'r-gap'")
	if execErr != nil {
		t.Fatalf("corrupt wait_reasons update failed: %v", execErr)
	}
	_, errCorruptRun := s.ReadRun(ctx, "r-gap")
	if !errors.Is(errCorruptRun, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on corrupt wait_reasons, got %v", errCorruptRun)
	}

	// Missing run and receipt lookups
	if _, errMissing := s.RebuildRun(ctx, "non-existent-run"); !errors.Is(errMissing, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound for RebuildRun on missing run, got %v", errMissing)
	}
	if _, errMissing := s.ReadRun(ctx, "non-existent-run"); !errors.Is(errMissing, ErrRunNotFound) {
		t.Fatalf("expected ErrRunNotFound for ReadRun on missing run, got %v", errMissing)
	}
	if _, found, errLookup := s.LookupReceipt(ctx, "non-existent-cmd"); errLookup != nil || found {
		t.Fatalf("expected not found for missing receipt, got found=%v err=%v", found, errLookup)
	}
}

func TestAppendPurgeFailsSession(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	_, err := db.ExecContext(ctx, `
		INSERT INTO exec_purges (session_id, requested_at)
		VALUES ('sess-purged', '2026-11-02T10:00:00Z')
	`)
	if err != nil {
		t.Fatalf("insert exec_purges failed: %v", err)
	}

	evtPurge := makeRunAccepted(makeEnvelope("r-purged", 1, "c-purged", 0, now), "sess-purged", "")
	_, errPurgeAppend := s.Append(ctx, &AppendRequest{
		SessionID: "sess-purged", RunID: "r-purged", CommandID: "c-purged", Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 0}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evtPurge}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	})
	if !errors.Is(errPurgeAppend, ErrSessionPurging) {
		t.Fatalf("expected ErrSessionPurging on purged session, got %v", errPurgeAppend)
	}
}

func TestAppendRunRevisionMismatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, _, _ := newTestStore(ctx, t, nil)

	evtRun1 := makeRunAccepted(makeEnvelope(testBranchRunRev, 1, "c-rev-1", 0, now), "sess-rev", "")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-rev", RunID: testBranchRunRev, CommandID: "c-rev-1", Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 0}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evtRun1}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append run 1 failed: %v", err)
	}

	evtRun2 := makeRunFinished(makeEnvelope(testBranchRunRev, 2, "c-rev-2", 0, now), kernel.RunOutcomeCancelled)
	_, errMismatch := s.Append(ctx, &AppendRequest{
		SessionID: "sess-rev", RunID: testBranchRunRev, CommandID: "c-rev-2", Kind: cmdRequestStop, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 1}, ExpectedAdmissionRevision: 2, ExpectedRunRevision: 99,
		Events: []kernel.Event{evtRun2}, Result: codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
	})
	if !errors.Is(errMismatch, ErrStaleRevision) {
		t.Fatalf("expected ErrStaleRevision for run revision mismatch, got %v", errMismatch)
	}
}

func TestAppendRunFinishClearsActiveRun(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	evtFin1 := makeRunAccepted(makeEnvelope(testBranchRunFin, 1, testBranchCmdFin1, 1, now), "sess-fin", "")
	evtFin2 := makeRunAdmitted(makeEnvelope(testBranchRunFin, 2, testBranchCmdFin1, 1, now), 1)
	evtFin3 := makeRunFinished(makeEnvelope(testBranchRunFin, 3, testBranchCmdFin1, 1, now), kernel.RunOutcomeCompleted)

	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-fin", RunID: testBranchRunFin, CommandID: testBranchCmdFin1, Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evtFin1, evtFin2, evtFin3},
		Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 3, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append run finished failed: %v", err)
	}

	var activeRun *string
	err = db.QueryRowContext(ctx, "SELECT active_run_id FROM exec_admission WHERE session_id = 'sess-fin'").Scan(&activeRun)
	if err != nil {
		t.Fatalf("query admission failed: %v", err)
	}
	if activeRun != nil {
		t.Fatalf("expected active_run_id to be NULL after finish, got %v", *activeRun)
	}
}

func TestAppendRunFinishOnExistingAdmission(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	evtAct1 := makeRunAccepted(makeEnvelope(testBranchRunAct, 1, testBranchCmdAct1, 1, now), "sess-act", "")
	evtAct2 := makeRunAdmitted(makeEnvelope(testBranchRunAct, 2, testBranchCmdAct1, 1, now), 1)
	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-act", RunID: testBranchRunAct, CommandID: testBranchCmdAct1, Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evtAct1, evtAct2}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 2, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("initial admit failed: %v", err)
	}

	evtActFin := makeRunFinished(makeEnvelope(testBranchRunAct, 3, "c-act-2", 1, now), kernel.RunOutcomeCompleted)
	_, err = s.Append(ctx, &AppendRequest{
		SessionID: "sess-act", RunID: testBranchRunAct, CommandID: "c-act-2", Kind: testKindAdvanceRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1, ExpectedRunRevision: 1,
		Events: []kernel.Event{evtActFin}, Result: codec.ReceiptResult{FirstSequence: 3, LastSequence: 3, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("finish on existing admission failed: %v", err)
	}

	var activeRunAfterFin *string
	err = db.QueryRowContext(ctx, "SELECT active_run_id FROM exec_admission WHERE session_id = 'sess-act'").Scan(&activeRunAfterFin)
	if err != nil {
		t.Fatalf("query admission failed: %v", err)
	}
	if activeRunAfterFin != nil {
		t.Fatalf("expected active_run_id to be NULL after finish, got %v", *activeRunAfterFin)
	}
}

func TestModelInvocationCompletedAndClosed(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	// Step 1: Open and complete model invocation
	evt1 := makeRunAccepted(makeEnvelope(testBranchRunMComp, 1, "c1", 1, now), "sess-m-comp", "")
	evt2 := makeRunAdmitted(makeEnvelope(testBranchRunMComp, 2, "c1", 1, now), 1)
	evt3 := makeStepStarted(makeEnvelope(testBranchRunMComp, 3, "c1", 1, now), 1)
	evt4 := makeModelRequestPlanned(makeEnvelope(testBranchRunMComp, 4, "c1", 1, now), "m-inv-comp", "att-1")
	evt5 := makeModelResponseRecorded(makeEnvelope(testBranchRunMComp, 5, "c1", 1, now), "m-inv-comp", "att-1", "")

	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-m-comp", RunID: testBranchRunMComp, CommandID: "c1", Kind: testKindAdvanceRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evt1, evt2, evt3, evt4, evt5},
		Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 5, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append model completed failed: %v", err)
	}

	var state string
	err = db.QueryRowContext(ctx, "SELECT state FROM exec_invocations WHERE run_id = '"+testBranchRunMComp+"' AND invocation_id = 'm-inv-comp'").Scan(&state)
	if err != nil {
		t.Fatalf("query state failed: %v", err)
	}
	if state != "completed" {
		t.Fatalf("expected state 'completed', got %q", state)
	}

	// Step 2: Open and close model invocation in a separate run
	evtC1 := makeRunAccepted(makeEnvelope(testBranchRunMClose, 1, testBranchCmdClose1, 1, now), "sess-m-close", "")
	evtC2 := makeRunAdmitted(makeEnvelope(testBranchRunMClose, 2, testBranchCmdClose1, 1, now), 1)
	evtC3 := makeStepStarted(makeEnvelope(testBranchRunMClose, 3, testBranchCmdClose1, 1, now), 1)
	evtC4 := makeModelRequestPlanned(makeEnvelope(testBranchRunMClose, 4, testBranchCmdClose1, 1, now), "m-inv-close", "att-1")
	evtC5 := makeModelAttemptAbandoned(makeEnvelope(testBranchRunMClose, 5, testBranchCmdClose1, 1, now), "m-inv-close", "att-1", kernel.NextActionClose)

	_, err = s.Append(ctx, &AppendRequest{
		SessionID: "sess-m-close", RunID: testBranchRunMClose, CommandID: testBranchCmdClose1, Kind: testKindAdvanceRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evtC1, evtC2, evtC3, evtC4, evtC5},
		Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 5, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append model closed failed: %v", err)
	}

	err = db.QueryRowContext(ctx, "SELECT state FROM exec_invocations WHERE run_id = '"+testBranchRunMClose+"' AND invocation_id = 'm-inv-close'").Scan(&state)
	if err != nil {
		t.Fatalf("query state failed: %v", err)
	}
	if state != "closed" {
		t.Fatalf("expected state 'closed', got %q", state)
	}
}

func TestToolInvocationClosedAndTranscript(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	evt1 := makeRunAccepted(makeEnvelope(testBranchRunToolCls, 1, "c1", 1, now), "sess-tool-cls", "")
	evt2 := makeRunAdmitted(makeEnvelope(testBranchRunToolCls, 2, "c1", 1, now), 1)
	evt3 := makeStepStarted(makeEnvelope(testBranchRunToolCls, 3, "c1", 1, now), 1)
	evt4 := makeToolCallPlanned(makeEnvelope(testBranchRunToolCls, 4, "c1", 1, now), "tool-1", "att-1", kernel.EffectClassPureRead)
	evt5 := makeToolResultRecorded(makeEnvelope(testBranchRunToolCls, 5, "c1", 1, now), "tool-1", "att-1", kernel.ToolResultOutcomeSucceeded)

	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-tool-cls", RunID: testBranchRunToolCls, CommandID: "c1", Kind: testKindAdvanceRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		AdmissionFence: &AdmissionFence{Token: 1, OwnerIncarnation: 1}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evt1, evt2, evt3, evt4, evt5},
		Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 5, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("append tool closed failed: %v", err)
	}

	var state string
	err = db.QueryRowContext(ctx, "SELECT state FROM exec_invocations WHERE run_id = '"+testBranchRunToolCls+"' AND invocation_id = 'tool-1'").Scan(&state)
	if err != nil {
		t.Fatalf("query tool state failed: %v", err)
	}
	if state != "closed" {
		t.Fatalf("expected tool state 'closed', got %q", state)
	}

	var disposition string
	err = db.QueryRowContext(ctx, "SELECT disposition FROM exec_transcript WHERE run_id = '"+testBranchRunToolCls+"' AND event_sequence = 5").Scan(&disposition)
	if err != nil {
		t.Fatalf("query transcript disposition failed: %v", err)
	}
	if disposition != "succeeded" {
		t.Fatalf("expected transcript disposition 'succeeded', got %q", disposition)
	}
}

func TestEncodeDecodeEdgeCases(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)

	// 1. Incomplete storage envelope in EncodeEvent
	evt := makeRunAccepted(makeEnvelope("r", 1, "c", 0, now), "sess", "")
	if _, err := EncodeEvent(evt, &StorageEnvelope{
		EventID: "", SessionID: "", SchemaVersion: 0, ProtocolVersion: 0, CorrelationID: "", WriterIncarnation: 0, QueueRevision: 0, RecordedAt: now, PayloadID: "",
	}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for empty EventID, got %v", err)
	}
	if _, err := EncodeEvent(evt, &StorageEnvelope{
		EventID: "e", SessionID: "", SchemaVersion: 0, ProtocolVersion: 0, CorrelationID: "", WriterIncarnation: 0, QueueRevision: 0, RecordedAt: now, PayloadID: "",
	}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for empty SessionID, got %v", err)
	}

	// 2. Incomplete event envelope in EncodeEvent
	incompleteEvt := makeRunAccepted(&kernel.Envelope{
		RunID: "", Sequence: 1, CommandID: "", CausationID: "", Origin: "", Token: 0, Timestamp: now,
	}, "sess", "")
	if _, err := EncodeEvent(incompleteEvt, &StorageEnvelope{
		EventID: "e", SessionID: "s", SchemaVersion: 0, ProtocolVersion: 0, CorrelationID: "", WriterIncarnation: 0, QueueRevision: 0, RecordedAt: now, PayloadID: "",
	}); !errors.Is(err, ErrInvalidBatch) {
		t.Fatalf("expected ErrInvalidBatch for incomplete envelope, got %v", err)
	}

	// 3. DecodeEvent with corrupt JSON
	corruptDurable := &DurableEvent{
		RunID: "", Sequence: 0, EventID: "", SessionID: "", EventType: testEventRunAccepted,
		SchemaVersion: CurrentSchemaVersion, ProtocolVersion: CurrentProtocolVersion,
		CommandID: "", CausationID: "", CorrelationID: "", Origin: "", WriterIncarnation: 0,
		AdmissionToken: 0, QueueRevision: 0, RecordedAt: now, Data: "{corrupted-json", PayloadID: "",
	}
	if _, err := DecodeEvent(corruptDurable); !errors.Is(err, ErrCorruptHistory) {
		t.Fatalf("expected ErrCorruptHistory on unmarshal failure, got %v", err)
	}

	// 4. Safe metadata value edge cases
	if err := validateSafeMetadataValue("SourcePath", reflect.ValueOf(strings.Repeat("a", 1025))); err == nil {
		t.Fatal("expected error for SourcePath > 1024")
	}
	if err := validateSafeMetadataValue("OtherField", reflect.ValueOf(strings.Repeat("a", 257))); err == nil {
		t.Fatal("expected error for OtherField > 256")
	}
	if err := validateSafeMetadataValue("SliceField", reflect.ValueOf([]string{strings.Repeat("x", 300)})); err == nil {
		t.Fatal("expected error for nested slice string > 256")
	}
	if err := validateSafeMetadataValue("MapField", reflect.ValueOf(map[string]string{"k": strings.Repeat("x", 300)})); err == nil {
		t.Fatal("expected error for nested map string > 256")
	}
}

func TestResolveCommitAndAppendClosedDB(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{LockGeneration: 1, IncarnationID: "host-closed", AcquiredAt: clk.Now()}
	idGen := ids.NewSequence("closed")

	db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatal(migErr)
	}

	s, err := New(ctx, db, host, clk, idGen, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Close database
	if closeErr := db.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}

	// ResolveCommit on closed DB
	status, _, err := s.ResolveCommit(ctx, "c-any")
	if status != CommitStatusUnavailable || !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("expected unavailable on closed DB ResolveCommit, got status=%v err=%v", status, err)
	}

	// Append on closed DB
	_, errAppend := s.Append(ctx, &AppendRequest{
		SessionID: testSess, RunID: "r", CommandID: "c", Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 0}, ExpectedAdmissionRevision: 1,
	})
	if !errors.Is(errAppend, ErrStoreUnavailable) {
		t.Fatalf("expected ErrStoreUnavailable on closed DB Append, got %v", errAppend)
	}
}

func TestAdmissionCASFailurePath(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	// Create initial admission row
	evt1 := makeRunAccepted(makeEnvelope(testBranchRunCAS, 1, "c-cas-1", 0, now), "sess-cas", "")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID: "sess-cas", RunID: testBranchRunCAS, CommandID: "c-cas-1", Kind: testKindAcceptRun, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 0}, ExpectedAdmissionRevision: 1,
		Events: []kernel.Event{evt1}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
	})
	if err != nil {
		t.Fatalf("initial append failed: %v", err)
	}

	// Trigger admission update CAS failure by modifying admission row revision in trigger before UPDATE
	_, err = db.ExecContext(ctx, `
		CREATE TRIGGER trigger_break_cas
		BEFORE UPDATE ON exec_admission
		FOR EACH ROW
		WHEN OLD.session_id = 'sess-cas' AND OLD.revision = 1
		BEGIN
			SELECT RAISE(IGNORE);
		END;
	`)
	if err != nil {
		t.Fatalf("create trigger failed: %v", err)
	}
	evt2 := makeRunFinished(makeEnvelope(testBranchRunCAS, 2, "c-cas-2", 0, now), kernel.RunOutcomeCancelled)
	_, errCAS := s.Append(ctx, &AppendRequest{
		SessionID: "sess-cas", RunID: testBranchRunCAS, CommandID: "c-cas-2", Kind: cmdRequestStop, Principal: "p", Target: "t",
		CodecVersion: codec.CodecV1, MetadataInput: []byte("{}"), CanonicalInput: []byte("{}"),
		QueueFence: &QueueFence{QueueRevision: 1}, ExpectedAdmissionRevision: 2, ExpectedRunRevision: 1,
		Events: []kernel.Event{evt2}, Result: codec.ReceiptResult{FirstSequence: 2, LastSequence: 2, RejectionCode: "", Message: ""},
	})
	if !errors.Is(errCAS, ErrStaleRevision) {
		t.Fatalf("expected ErrStaleRevision on admission CAS failure, got %v", errCAS)
	}
}
