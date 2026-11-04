package execstore

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

func TestExecutionStoreRejectsUnsafePragmas(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	host := storage.HostIncarnation{
		LockGeneration: 1,
		IncarnationID:  "host-1",
		AcquiredAt:     clk.Now(),
	}
	idGen := ids.NewSequence("test")

	pragmaCases := []struct {
		name string
		sql  string
	}{
		{name: "wrong synchronous pragma", sql: "PRAGMA synchronous=NORMAL"},
		{name: "wrong journal mode pragma", sql: "PRAGMA journal_mode=MEMORY"},
		{name: "foreign keys disabled", sql: "PRAGMA foreign_keys=OFF"},
	}

	for _, tc := range pragmaCases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			closeTestDB(t, db)
			if _, execErr := db.ExecContext(ctx, tc.sql); execErr != nil {
				t.Fatal(execErr)
			}
			_, err = New(ctx, db, nil, host, clk, idGen, nil, nil)
			if err == nil || !strings.Contains(err.Error(), "durability requirements") {
				t.Fatalf("expected ErrUnsafePragmas, got %v", err)
			}
		})
	}
}

func TestExecutionMigrationEventConstraints(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)

	t.Run("event sequence must be positive", func(t *testing.T) {
		t.Parallel()
		_, execErr := db.ExecContext(
			ctx,
			`INSERT INTO exec_events (
				run_id, sequence, event_id, session_id, event_type,
				schema_version, protocol_version, command_id, causation_id,
				correlation_id, origin, writer_incarnation, admission_token,
				queue_revision, recorded_at, data, payload_id
			) VALUES ('run-1', 0, 'evt-0', 'sess-1', 'RunAccepted', 1, 1, 'cmd-1', 'cause-1', 'corr-1', 'user', 1, 0, 0, 'now', '{}', NULL)`,
		)
		if execErr == nil {
			t.Fatal("expected CHECK constraint failure for sequence=0, got nil")
		}
	})

	t.Run("event origin must be in allowlist", func(t *testing.T) {
		t.Parallel()
		// Insert parent rows first so foreign keys are satisfied
		if _, err := db.ExecContext(ctx, `INSERT INTO exec_admission (session_id, owner_incarnation, admission_token, revision, queue_revision, context_epoch, context_revision, context_requirement) VALUES ('sess-1', 1, 0, 1, 0, 0, 0, 'none')`); err != nil {
			t.Fatalf("insert parent admission: %v", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO exec_runs (run_id, session_id, last_sequence, revision, protocol_version, lifecycle, phase, wait_reasons, outcome, admission_token, step_index, context_epoch) VALUES ('run-1', 'sess-1', 1, 1, 1, 'queued', 'preparing', '[]', '', 0, 0, 0)`); err != nil {
			t.Fatalf("insert parent run: %v", err)
		}

		_, execErr := db.ExecContext(
			ctx,
			`INSERT INTO exec_events (
				run_id, sequence, event_id, session_id, event_type,
				schema_version, protocol_version, command_id, causation_id,
				correlation_id, origin, writer_incarnation, admission_token,
				queue_revision, recorded_at, data, payload_id
			) VALUES ('run-1', 1, 'evt-1', 'sess-1', 'RunAccepted', 1, 1, 'cmd-1', 'cause-1', 'corr-1', 'malicious_client', 1, 0, 0, 'now', '{}', NULL)`,
		)
		if execErr == nil {
			t.Fatal("expected CHECK constraint failure for invalid origin, got nil")
		}
		if !strings.Contains(strings.ToLower(execErr.Error()), "check") {
			t.Fatalf("expected CHECK constraint failure, got %v", execErr)
		}
	})
}

func TestExecutionMigrationReceiptAndPayloadConstraints(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)

	t.Run("payload size must be non-negative", func(t *testing.T) {
		t.Parallel()
		_, execErr := db.ExecContext(
			ctx,
			`INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope)
			 VALUES ('p-1', 'sess-1', 'command_input', 'k-1', -1, x'01')`,
		)
		if execErr == nil {
			t.Fatal("expected CHECK constraint failure for negative size, got nil")
		}
	})

	t.Run("receipt must have exactly one input representation", func(t *testing.T) {
		t.Parallel()
		_, err1 := db.ExecContext(
			ctx,
			`INSERT INTO exec_receipts (
				command_id, session_id, run_id, kind, principal, target,
				codec_version, input_payload_id, metadata_input, result,
				first_sequence, last_sequence, rejection_code, recorded_at
			) VALUES ('cmd-both-null', 'sess-1', 'run-1', 'kind', 'p', 't', 'v1', NULL, NULL, 'noop', 0, 0, '', 'now')`,
		)
		if err1 == nil {
			t.Fatal("expected CHECK failure for both null input fields")
		}

		_, err2 := db.ExecContext(
			ctx,
			`INSERT INTO exec_receipts (
				command_id, session_id, run_id, kind, principal, target,
				codec_version, input_payload_id, metadata_input, result,
				first_sequence, last_sequence, rejection_code, recorded_at
			) VALUES ('cmd-both-set', 'sess-1', 'run-1', 'kind', 'p', 't', 'v1', 'p-1', x'01', 'noop', 0, 0, '', 'now')`,
		)
		if err2 == nil {
			t.Fatal("expected CHECK failure for both non-null input fields")
		}
	})
}

func TestExecutionMigrationForeignKeys(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db, _ := newTestStore(ctx, t, nil)

	_, execErr := db.ExecContext(
		ctx,
		`INSERT INTO exec_payloads (payload_id, session_id, kind, key_id, size, envelope)
		 VALUES ('p-missing-key', 'sess-no-key', 'command_input', 'k-nonexistent', 10, x'0102')`,
	)
	if execErr == nil {
		t.Fatal("expected foreign key constraint violation, got nil")
	}
}

func setupAppendOnlyTestRun(ctx context.Context, t *testing.T) (*Store, *sql.DB) {
	t.Helper()
	s, db, _ := newTestStore(ctx, t, nil)
	payload, key := sealedTestPayload(t, "pay-1", []byte("canonical-bytes"))
	evt := makeRunAccepted(makeEnvelope("run-append", 1, "cmd-append", 0, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)), "sess-append", "pay-1")

	req := &AppendRequest{
		SessionID:                 "sess-append",
		RunID:                     "run-append",
		CommandID:                 "cmd-append",
		Kind:                      testKindAcceptRun,
		Principal:                 "test-user",
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-1",
		CanonicalInput:            []byte("canonical-bytes"),
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
		t.Fatalf("Append failed: %v", err)
	}
	return s, db
}

func TestExecutionPayloadAppendOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db := setupAppendOnlyTestRun(ctx, t)

	_, execErr := db.ExecContext(ctx, "UPDATE exec_payloads SET size = 999 WHERE payload_id = 'pay-1'")
	if execErr == nil || !strings.Contains(execErr.Error(), "append-only") {
		t.Fatalf("expected append-only trigger rejection, got %v", execErr)
	}

	_, delErr := db.ExecContext(ctx, "DELETE FROM exec_payloads WHERE payload_id = 'pay-1'")
	if delErr == nil || !strings.Contains(delErr.Error(), "append-only") {
		t.Fatalf("expected append-only trigger rejection, got %v", delErr)
	}
}

func TestExecutionReceiptImmutable(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db := setupAppendOnlyTestRun(ctx, t)

	_, execErr := db.ExecContext(ctx, "UPDATE exec_receipts SET result = 'noop' WHERE command_id = 'cmd-append'")
	if execErr == nil || !strings.Contains(execErr.Error(), "immutable") {
		t.Fatalf("expected immutable trigger rejection, got %v", execErr)
	}

	_, delErr := db.ExecContext(ctx, "DELETE FROM exec_receipts WHERE command_id = 'cmd-append'")
	if delErr == nil || !strings.Contains(delErr.Error(), "immutable") {
		t.Fatalf("expected immutable trigger rejection, got %v", delErr)
	}
}

func TestExecutionPurgePermanenceAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, db := setupAppendOnlyTestRun(ctx, t)

	if _, insertErr := db.ExecContext(ctx, "INSERT INTO exec_purges (session_id, requested_at) VALUES ('sess-other', 'now')"); insertErr != nil {
		t.Fatal(insertErr)
	}
	_, delErr := db.ExecContext(ctx, "DELETE FROM exec_payloads WHERE session_id = 'sess-append'")
	if delErr == nil || !strings.Contains(delErr.Error(), "append-only") {
		t.Fatalf("expected purge exception isolation failure, got %v", delErr)
	}

	if _, insertErr := db.ExecContext(ctx, "INSERT INTO exec_purges (session_id, requested_at) VALUES ('sess-perm', 'now')"); insertErr != nil {
		t.Fatal(insertErr)
	}
	_, errUpdate := db.ExecContext(ctx, "UPDATE exec_purges SET requested_at = 'later' WHERE session_id = 'sess-perm'")
	if errUpdate == nil || !strings.Contains(errUpdate.Error(), "permanent") {
		t.Fatalf("expected update exec_purges rejection, got %v", errUpdate)
	}
	_, errDelete := db.ExecContext(ctx, "DELETE FROM exec_purges WHERE session_id = 'sess-perm'")
	if errDelete == nil || !strings.Contains(errDelete.Error(), "permanent") {
		t.Fatalf("expected delete exec_purges rejection, got %v", errDelete)
	}
}
