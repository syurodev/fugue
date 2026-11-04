package execstore

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

func TestStoreConstructorValidations(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{LockGeneration: 1, IncarnationID: "host-val", AcquiredAt: clk.Now()}
	idGen := ids.NewSequence("val")

	t.Run("nil db in New is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := New(ctx, nil, host, clk, idGen, nil)
		if err == nil {
			t.Fatal("expected error for nil db")
		}
	})

	t.Run("nil clock in New is rejected", func(t *testing.T) {
		t.Parallel()
		db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		closeTestDB(t, db)
		_, err = New(ctx, db, host, nil, idGen, nil)
		if err == nil {
			t.Fatal("expected error for nil clock")
		}
	})

	t.Run("nil id generator in New is rejected", func(t *testing.T) {
		t.Parallel()
		db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		closeTestDB(t, db)
		_, err = New(ctx, db, host, clk, nil, nil)
		if err == nil {
			t.Fatal("expected error for nil idGen")
		}
	})

	t.Run("empty host in New is rejected", func(t *testing.T) {
		t.Parallel()
		db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
		if err != nil {
			t.Fatal(err)
		}
		closeTestDB(t, db)
		_, err = New(ctx, db, storage.HostIncarnation{}, clk, idGen, nil)
		if err == nil {
			t.Fatal("expected error for empty host")
		}
	})
}

func TestStoreAccessors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, db, _ := newTestStore(ctx, t, nil)
	if s.DB() != db {
		t.Fatalf("s.DB() = %v, want %v", s.DB(), db)
	}
	if s.Host().IncarnationID != "host-inc-1" {
		t.Fatalf("s.Host().IncarnationID = %s, want host-inc-1", s.Host().IncarnationID)
	}

	var nilStore *Store
	if nilStore.DB() != nil {
		t.Fatal("nilStore.DB() should be nil")
	}
	if nilStore.Host().IncarnationID != "" {
		t.Fatal("nilStore.Host() should be empty")
	}
}

func TestAppendValidationEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _, _ := newTestStore(ctx, t, nil)

	t.Run("nil append request", func(t *testing.T) {
		t.Parallel()
		_, err := s.Append(ctx, nil)
		if !errors.Is(err, ErrInvalidBatch) {
			t.Fatalf("expected ErrInvalidBatch, got %v", err)
		}
	})

	t.Run("missing required request fields", func(t *testing.T) {
		t.Parallel()
		badRequests := []*AppendRequest{
			{SessionID: "", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "r", CommandID: "", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			// Neither protected nor metadata input
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", QueueFence: &QueueFence{}},
			// Both protected and metadata input
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", ProtectedInputID: "p1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}},
			// Neither admission nor queue fence
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}")},
			// Both admission and queue fence
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), AdmissionFence: &AdmissionFence{}, QueueFence: &QueueFence{}},
			// Rejection without rejection code
			{SessionID: "s", RunID: "r", CommandID: "c", Kind: "k", Principal: "p", Target: "t", CodecVersion: "v1", MetadataInput: []byte("{}"), QueueFence: &QueueFence{}, Result: codec.ReceiptResult{FirstSequence: 0, LastSequence: 0, RejectionCode: ""}},
		}

		for i, req := range badRequests {
			_, err := s.Append(ctx, req)
			if err == nil {
				t.Fatalf("expected error for bad request index %d, got nil", i)
			}
		}
	})
}

func TestTargetedMutantKillersMetadata(t *testing.T) {
	t.Parallel()

	// Kill registry.go:278:12 CONDITIONALS_BOUNDARY len(s) > maxLen
	exact256 := strings.Repeat("a", 256)
	if err := validateStringMetadata("Reason", exact256); err != nil {
		t.Fatalf("expected 256-char string to be valid, got: %v", err)
	}
	exact1024 := strings.Repeat("b", 1024)
	if err := validateStringMetadata("SourcePath", exact1024); err != nil {
		t.Fatalf("expected 1024-char SourcePath to be valid, got: %v", err)
	}
}

func initTestRun(ctx context.Context, t *testing.T, s *Store, runID, sessID string, now time.Time) {
	t.Helper()
	env := makeEnvelope(runID, 1, "cmd-"+runID, 0, now)
	evt := makeRunAccepted(env, sessID, "")
	req := &AppendRequest{
		CommandID:                 "cmd-" + runID,
		SessionID:                 sessID,
		RunID:                     runID,
		Kind:                      testKindAcceptRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              "v1",
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		QueueFence:                &QueueFence{QueueRevision: 0},
		MetadataInput:             []byte("{}"),
		CorrelationID:             "corr-" + runID,
	}
	if _, err := s.Append(ctx, req); err != nil {
		t.Fatalf("append req: %v", err)
	}
}

func TestTargetedMutantKillersRead(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, _, _ := newTestStore(ctx, t, nil)

	initTestRun(ctx, t, s, "run-mut", "sess-mut", now)

	evts, err := s.ReadEvents(ctx, "run-mut", 1, 1)
	if err != nil || len(evts) != 1 {
		t.Fatalf("ReadEvents(1, 1) expected 1 event, got %d, err: %v", len(evts), err)
	}
}

func TestTargetedMutantKillersReceipt(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	_, db, _ := newTestStore(ctx, t, nil)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close conn: %v", closeErr)
		}
	}()

	reqNoop := &AppendRequest{
		CommandID:                 "cmd-noop",
		SessionID:                 "sess-mut",
		RunID:                     "run-mut",
		Kind:                      testKindAcceptRun,
		Principal:                 testUser1,
		Target:                    testTargetSession,
		CodecVersion:              "v1",
		Events:                    nil,
		Result:                    codec.ReceiptResult{FirstSequence: 0, LastSequence: 0},
		ExpectedRunRevision:       1,
		ExpectedAdmissionRevision: 1,
		QueueFence:                &QueueFence{QueueRevision: 0},
		MetadataInput:             []byte("{}"),
		CorrelationID:             "corr-noop",
	}
	if err := insertReceipt(ctx, conn, reqNoop, now); err != nil {
		t.Fatalf("insert noop receipt: %v", err)
	}
	var noopResult string
	if err := conn.QueryRowContext(ctx, "SELECT result FROM exec_receipts WHERE command_id = 'cmd-noop'").Scan(&noopResult); err != nil {
		t.Fatalf("scan noop result: %v", err)
	}
	if noopResult != "noop" {
		t.Fatalf("expected result 'noop', got %q", noopResult)
	}
}

func TestTargetedMutantKillersProjections(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	initTestRun(ctx, t, s, "run-proj-mut", "sess-proj", now)

	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if closeErr := conn.Close(); closeErr != nil {
			t.Errorf("close conn: %v", closeErr)
		}
	}()

	state := kernel.NewState()
	state.ModelInvocations = map[string]*kernel.ModelInvocation{
		"inv-m1": {Attempts: map[string]*kernel.ModelAttempt{"att-m1": {Status: kernel.ModelAttemptStatusStreaming, OwnerIncarnation: 1}}},
		"inv-m2": {Attempts: map[string]*kernel.ModelAttempt{"att-m2": {Status: kernel.ModelAttemptStatusStreaming, OwnerIncarnation: 1}}},
	}
	state.ToolInvocations = map[string]*kernel.ToolInvocation{
		"tool-t1": {Attempts: map[string]*kernel.ToolAttempt{"att-t1": {Status: kernel.ToolAttemptStatusReady, OwnerIncarnation: 1}}},
		"tool-t2": {Attempts: map[string]*kernel.ToolAttempt{"att-t2": {Status: kernel.ToolAttemptStatusReady, OwnerIncarnation: 1}}},
	}
	if err := writeInvocationProjections(ctx, conn, "run-proj-mut", state); err != nil {
		t.Fatalf("writeInvocationProjections: %v", err)
	}
	var count int
	if err := conn.QueryRowContext(ctx, "SELECT COUNT(*) FROM exec_invocations WHERE run_id = 'run-proj-mut'").Scan(&count); err != nil {
		t.Fatalf("query count: %v", err)
	}
	if count != 4 {
		t.Fatalf("expected 4 invocations, got %d", count)
	}
	var mStatus, tStatus string
	if err := conn.QueryRowContext(ctx, "SELECT status FROM exec_attempts WHERE attempt_id = 'att-m1'").Scan(&mStatus); err != nil {
		t.Fatalf("scan model attempt status: %v", err)
	}
	if mStatus != "streaming" {
		t.Fatalf("expected model attempt status 'streaming', got %q", mStatus)
	}
	if err := conn.QueryRowContext(ctx, "SELECT status FROM exec_attempts WHERE attempt_id = 'att-t1'").Scan(&tStatus); err != nil {
		t.Fatalf("scan tool attempt status: %v", err)
	}
	if tStatus != "ready" {
		t.Fatalf("expected tool attempt status 'ready', got %q", tStatus)
	}
}
