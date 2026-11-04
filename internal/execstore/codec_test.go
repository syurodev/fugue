package execstore

import (
	"strings"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/kernel"
)

const (
	testRunMeta   = "run-meta"
	testCmdMeta   = "cmd-meta"
	testCauseMeta = "cause-meta"
	testSessMeta  = "sess-meta"
)

func testEventCodecRoundtrip(t *testing.T, name string, env *kernel.Envelope, now time.Time) {
	t.Helper()
	evt, err := instantiateEvent(name)
	if err != nil {
		t.Fatalf("instantiateEvent(%s) failed: %v", name, err)
	}
	if evt.EventName() != name {
		t.Fatalf("EventName() = %s, want %s", evt.EventName(), name)
	}

	setEventEnvelope(evt, env)

	durable, err := EncodeEvent(evt, &StorageEnvelope{
		EventID:           "evt-id-" + name,
		SessionID:         "sess-cov",
		SchemaVersion:     CurrentSchemaVersion,
		ProtocolVersion:   CurrentProtocolVersion,
		CorrelationID:     "corr-cov",
		WriterIncarnation: 1,
		QueueRevision:     0,
		RecordedAt:        now,
		PayloadID:         "",
	})
	if err != nil {
		t.Fatalf("EncodeEvent(%s) failed: %v", name, err)
	}

	decoded, err := DecodeEvent(durable)
	if err != nil {
		t.Fatalf("DecodeEvent(%s) failed: %v", name, err)
	}

	if decoded.EventName() != name {
		t.Fatalf("decoded EventName = %s, want %s", decoded.EventName(), name)
	}
	decEnv := decoded.EventEnvelope()
	if decEnv.RunID != env.RunID || decEnv.Sequence != env.Sequence || decEnv.CommandID != env.CommandID {
		t.Fatalf("envelope mismatch on decoded %s: %+v", name, decEnv)
	}
}

func TestExecutionEventCodecCoversKernelRegistry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := kernel.Envelope{
		RunID:       "run-cov",
		Sequence:    1,
		CommandID:   "cmd-cov",
		CausationID: "cause-cov",
		Origin:      testOriginUser,
		Token:       1,
		Timestamp:   now,
	}

	if len(AllEventNames()) != 54 {
		t.Fatalf("expected 54 event names, got %d", len(AllEventNames()))
	}

	for _, name := range AllEventNames() {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testEventCodecRoundtrip(t, name, &env, now)
		})
	}
}

func TestExecutionRejectsUnknownVersions(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

	t.Run("unsupported schema version fails closed", func(t *testing.T) {
		t.Parallel()
		durable := &DurableEvent{
			RunID:             "run-ver",
			Sequence:          1,
			EventID:           "evt-ver-1",
			SessionID:         "sess-ver",
			EventType:         testEventRunAccepted,
			SchemaVersion:     999,
			ProtocolVersion:   CurrentProtocolVersion,
			CommandID:         "cmd-ver",
			CausationID:       "cause-ver",
			CorrelationID:     "",
			Origin:            testOriginUser,
			WriterIncarnation: 1,
			AdmissionToken:    0,
			QueueRevision:     0,
			RecordedAt:        now,
			Data:              "{}",
			PayloadID:         "",
		}
		_, err := DecodeEvent(durable)
		if err == nil || !strings.Contains(err.Error(), "schema=999") {
			t.Fatalf("expected ErrUnknownVersion for schema version 999, got %v", err)
		}
	})

	t.Run("unsupported protocol version fails closed", func(t *testing.T) {
		t.Parallel()
		durable := &DurableEvent{
			RunID:             "run-ver",
			Sequence:          1,
			EventID:           "evt-ver-2",
			SessionID:         "sess-ver",
			EventType:         testEventRunAccepted,
			SchemaVersion:     CurrentSchemaVersion,
			ProtocolVersion:   999,
			CommandID:         "cmd-ver",
			CausationID:       "cause-ver",
			CorrelationID:     "",
			Origin:            testOriginUser,
			WriterIncarnation: 1,
			AdmissionToken:    0,
			QueueRevision:     0,
			RecordedAt:        now,
			Data:              "{}",
			PayloadID:         "",
		}
		_, err := DecodeEvent(durable)
		if err == nil || !strings.Contains(err.Error(), "proto=999") {
			t.Fatalf("expected ErrUnknownVersion for protocol version 999, got %v", err)
		}
	})
}

func TestExecutionRejectsSequenceGap(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	payload, key := sealedTestPayload(t, "pay-gap", []byte("in"))
	evt1 := makeRunAccepted(makeEnvelope("run-gap", 1, "cmd-gap-1", 0, now), "sess-gap", "pay-gap")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 "sess-gap",
		RunID:                     "run-gap",
		CommandID:                 "cmd-gap-1",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-gap",
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
		t.Fatalf("Append 1 failed: %v", err)
	}

	_, err = db.ExecContext(
		ctx,
		`INSERT INTO exec_events (
			run_id, sequence, event_id, session_id, event_type,
			schema_version, protocol_version, command_id, causation_id,
			correlation_id, origin, writer_incarnation, admission_token,
			queue_revision, recorded_at, data, payload_id
		) VALUES ('run-gap', 3, 'evt-gap-3', 'sess-gap', 'StepStarted', 1, 1, 'cmd-3', 'cause-3', 'corr-3', 'user', 1, 0, 0, 'now', '{"ContextRefs":[],"StepIndex":1}', NULL)`,
	)
	if err == nil || !strings.Contains(err.Error(), "contiguous") {
		t.Fatalf("expected trigger to reject sequence gap, got %v", err)
	}
}

func TestExecutionRejectsMalformedData(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	durable := &DurableEvent{
		RunID:             "run-malformed",
		Sequence:          1,
		EventID:           "evt-mal-1",
		SessionID:         "sess-mal",
		EventType:         testEventRunAccepted,
		SchemaVersion:     CurrentSchemaVersion,
		ProtocolVersion:   CurrentProtocolVersion,
		CommandID:         "cmd-mal",
		CausationID:       "cause-mal",
		CorrelationID:     "",
		Origin:            testOriginUser,
		WriterIncarnation: 1,
		AdmissionToken:    0,
		QueueRevision:     0,
		RecordedAt:        now,
		Data:              "not-valid-json",
		PayloadID:         "",
	}
	if _, err := DecodeEvent(durable); err == nil {
		t.Fatal("expected failure on malformed event data")
	}
}

func TestExecutionMetadataControlCharacters(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := &kernel.Envelope{
		RunID:       testRunMeta,
		Sequence:    1,
		CommandID:   testCmdMeta,
		CausationID: testCauseMeta,
		Origin:      testOriginUser,
		Token:       0,
		Timestamp:   now,
	}
	evt := makeRunAccepted(env, testSessMeta, "ref\nwith\nnewlines")
	_, err := EncodeEvent(evt, &StorageEnvelope{
		EventID:           "evt-meta",
		SessionID:         testSessMeta,
		SchemaVersion:     1,
		ProtocolVersion:   1,
		CorrelationID:     "corr",
		WriterIncarnation: 1,
		QueueRevision:     0,
		RecordedAt:        now,
		PayloadID:         "",
	})
	if err == nil || !strings.Contains(err.Error(), "control character") {
		t.Fatalf("expected control character rejection, got %v", err)
	}
}

func TestExecutionMetadataLengthLimit(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	env := &kernel.Envelope{
		RunID:       testRunMeta,
		Sequence:    1,
		CommandID:   testCmdMeta,
		CausationID: testCauseMeta,
		Origin:      testOriginUser,
		Token:       0,
		Timestamp:   now,
	}
	evt := makeRunAccepted(env, testSessMeta, strings.Repeat("a", 300))
	_, err := EncodeEvent(evt, &StorageEnvelope{
		EventID:           "evt-meta",
		SessionID:         testSessMeta,
		SchemaVersion:     1,
		ProtocolVersion:   1,
		CorrelationID:     "corr",
		WriterIncarnation: 1,
		QueueRevision:     0,
		RecordedAt:        now,
		PayloadID:         "",
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("expected field length rejection, got %v", err)
	}
}

func TestExecutionControlColumnsNoSensitiveWords(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	s, db, _ := newTestStore(ctx, t, nil)

	payload, key := sealedTestPayload(t, "pay-safe", []byte("secret-user-prompt-content"))
	evt := makeRunAccepted(makeEnvelope("run-safe", 1, "cmd-safe", 0, now), "sess-safe", "pay-safe")
	_, err := s.Append(ctx, &AppendRequest{
		SessionID:                 "sess-safe",
		RunID:                     "run-safe",
		CommandID:                 "cmd-safe",
		Kind:                      testKindAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          "pay-safe",
		CanonicalInput:            []byte("secret-user-prompt-content"),
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1, RejectionCode: "", Message: ""},
		SealedPayloads:            []SealedPayload{payload},
		SealedSessionKey:          &key,
	})
	if err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	var dataStr, orig, cmdID string
	err = db.QueryRowContext(ctx, "SELECT data, origin, command_id FROM exec_events WHERE run_id = 'run-safe'").Scan(&dataStr, &orig, &cmdID)
	if err != nil {
		t.Fatalf("query exec_events: %v", err)
	}
	if strings.Contains(dataStr, "secret-user-prompt-content") {
		t.Fatal("exec_events.data contains plaintext protected content!")
	}
}
