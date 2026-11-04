package execstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/crypto"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/storage"
)

const (
	testOriginUser       = "user"
	testTargetSession    = "session"
	testKindAcceptRun    = "AcceptRun"
	testKindAdvanceRun   = "AdvanceRun"
	testKindAdmitRun     = "AdmitRun"
	testCause1           = "cause-1"
	testEventRunAccepted = "RunAccepted"
	testUser1            = "user-1"
	testSess             = "sess"
	testRequirementNone  = "none"
)

var (
	errTestInjected = errors.New("injected-write-fault")
	errTestLostAck  = errors.New("lost-ack")
)

type fakeEquality struct {
	payloads map[string][]byte
	err      error
}

func (f *fakeEquality) OpenPayload(_ context.Context, sessionID, payloadID string) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	if sessionID == "" || payloadID == "" {
		return nil, fmt.Errorf("%w: sessionID and payloadID required", ErrProtectedInputUnavailable)
	}
	key := sessionID + "/" + payloadID
	if p, ok := f.payloads[key]; ok {
		return p, nil
	}
	return nil, fmt.Errorf("%w: payload not found for session %s, payload %s", ErrProtectedInputUnavailable, sessionID, payloadID)
}

func closeTestDB(t *testing.T, closer io.Closer) {
	t.Helper()
	t.Cleanup(func() {
		if closer != nil {
			if err := closer.Close(); err != nil {
				return
			}
		}
	})
}

func newTestStore(ctx context.Context, t *testing.T, equality ProtectedEquality) (*Store, *sql.DB, string) {
	t.Helper()
	dataDir := t.TempDir()
	db, err := storage.Open(ctx, storage.DBPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	closeTestDB(t, db)
	clk := clock.NewFake(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC))
	if migErr := storage.Migrate(ctx, db, clk); migErr != nil {
		t.Fatal(migErr)
	}
	host := storage.HostIncarnation{
		LockGeneration: 1,
		IncarnationID:  "host-inc-1",
		AcquiredAt:     clk.Now(),
	}
	idGen := ids.NewSequence("test-id")
	s, err := New(ctx, db, nil, host, clk, idGen, nil, equality)
	if err != nil {
		t.Fatal(err)
	}
	return s, db, dataDir
}

func sealedTestPayload(t *testing.T, payloadID string, plaintext []byte) (SealedPayload, SealedSessionKey) {
	t.Helper()
	key := []byte("01234567890123456789012345678901") // 32 bytes
	ad := crypto.AssociatedData{
		RecordType: "exec.payload",
		RecordID:   payloadID,
		KeyID:      "v1",
	}
	env, err := crypto.SealV2(ad, key, plaintext)
	if err != nil {
		t.Fatal(err)
	}
	rawEnv, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	return SealedPayload{
		PayloadID: payloadID,
		Kind:      "command_input",
		KeyID:     "v1",
		Size:      int64(len(plaintext)),
		Envelope:  rawEnv,
	}, SealedSessionKey{
		KeyID:      "v1",
		WrappedKey: []byte("wrapped-session-key-32-bytes-long!"),
	}
}

func makeEnvelope(runID string, seq uint64, cmdID string, token uint64, ts time.Time) *kernel.Envelope {
	return &kernel.Envelope{
		RunID:       runID,
		Sequence:    seq,
		CommandID:   cmdID,
		CausationID: testCause1,
		Origin:      testOriginUser,
		Token:       token,
		Timestamp:   ts,
	}
}

func makeRunAccepted(env *kernel.Envelope, sessionID, inputRef string) *kernel.RunAcceptedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.RunAcceptedEvent{
		Envelope:        e,
		InputRef:        inputRef,
		SessionID:       sessionID,
		QueueItemRef:    "",
		ProtocolBinding: "",
		InitialBudgets:  "",
		PriorityClass:   "",
		PeerOutboxRef:   "",
		GoalID:          "",
		GoalRound:       0,
	}
}

func makeRunStopRequested(env *kernel.Envelope, reason kernel.StopReason, mode kernel.StopMode, actor, target string, reqTime time.Time) *kernel.RunStopRequestedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.RunStopRequestedEvent{
		Envelope:    e,
		Reason:      reason,
		Mode:        mode,
		Actor:       actor,
		Target:      target,
		RequestTime: reqTime,
	}
}

func makeRunAdmitted(env *kernel.Envelope, token uint64) *kernel.RunAdmittedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.RunAdmittedEvent{
		Envelope:               e,
		Token:                  token,
		OwnerIncarnation:       1,
		QueueDecisionRef:       "",
		ContextEpoch:           0,
		StartRunID:             "",
		InitialContextRevision: 0,
	}
}

func makeStepStarted(env *kernel.Envelope, stepIndex int) *kernel.StepStartedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.StepStartedEvent{
		Envelope:    e,
		StepIndex:   stepIndex,
		ContextRefs: nil,
	}
}

func makeRunFinished(env *kernel.Envelope, outcome kernel.RunOutcome) *kernel.RunFinishedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.RunFinishedEvent{
		Envelope:               e,
		Outcome:                outcome,
		Truncated:              false,
		PrimaryStopRef:         "",
		SecondaryStopRefs:      nil,
		UnresolvedExposureRefs: nil,
	}
}

func makeToolCallPlanned(env *kernel.Envelope, toolCallID, firstAttemptID string, effect kernel.EffectClass) *kernel.ToolCallPlannedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.ToolCallPlannedEvent{
		Envelope:          e,
		ToolCallID:        toolCallID,
		SourcePosition:    0,
		DescriptorVersion: "1.0",
		EffectClass:       effect,
		FirstAttemptID:    firstAttemptID,
		InputRef:          "",
	}
}

func makeToolResultRecorded(env *kernel.Envelope, toolCallID, attemptID string, outcome kernel.ToolResultOutcome) *kernel.ToolResultRecordedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.ToolResultRecordedEvent{
		Envelope:       e,
		ToolCallID:     toolCallID,
		AttemptID:      attemptID,
		Outcome:        outcome,
		ResultRef:      "",
		Code:           "",
		EffectEvidence: "",
		SourcePosition: 0,
	}
}

func makeModelRequestPlanned(env *kernel.Envelope, invID, attID string) *kernel.ModelRequestPlannedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.ModelRequestPlannedEvent{
		Envelope:              e,
		InvocationID:          invID,
		Purpose:               "",
		FirstAttemptID:        attID,
		ModelRef:              "",
		ContextRef:            "",
		ToolDeclarationRefs:   nil,
		EstimatedBudgetTokens: 0,
	}
}

func makeModelResponseRecorded(env *kernel.Envelope, invID, attID, respRef string) *kernel.ModelResponseRecordedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.ModelResponseRecordedEvent{
		Envelope:     e,
		AttemptID:    attID,
		InvocationID: invID,
		ResponseRef:  respRef,
		UsageStatus:  "known",
	}
}

func makeModelAttemptAbandoned(env *kernel.Envelope, invID, attID string, next kernel.NextAction) *kernel.ModelAttemptAbandonedEvent {
	var e kernel.Envelope
	if env != nil {
		e = *env
	}
	return &kernel.ModelAttemptAbandonedEvent{
		Envelope:     e,
		AttemptID:    attID,
		InvocationID: invID,
		Uncertainty:  "",
		Receipt:      "",
		NextAction:   next,
		UnknownCost:  0,
	}
}

func makeAcceptedReq(
	s *Store,
	sessionID, runID, cmdID, payloadID string,
	canonicalInput []byte,
	sealed []SealedPayload,
	sealedKey *SealedSessionKey,
) *AppendRequest {
	evt := makeRunAccepted(makeEnvelope(runID, 1, cmdID, 0, s.clk.Now()), sessionID, payloadID)
	return &AppendRequest{
		SessionID:                 sessionID,
		RunID:                     runID,
		CommandID:                 cmdID,
		Kind:                      cmdAcceptRun,
		Principal:                 testOriginUser,
		Target:                    testTargetSession,
		CodecVersion:              codec.CodecV1,
		ProtectedInputID:          payloadID,
		CanonicalInput:            canonicalInput,
		QueueFence:                &QueueFence{QueueRevision: 0},
		ExpectedRunRevision:       0,
		ExpectedAdmissionRevision: 1,
		Events:                    []kernel.Event{evt},
		Result:                    codec.ReceiptResult{FirstSequence: 1, LastSequence: 1},
		SealedPayloads:            sealed,
		SealedSessionKey:          sealedKey,
	}
}
