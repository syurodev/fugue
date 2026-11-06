package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/errcode"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

const (
	adapterRun           = "run"
	adapterSession       = "session"
	adapterAcceptCommand = "accept"
	adapterOriginUser    = "user"
	adapterModelID       = "model"
	realFirstRun         = "first"
	realSecondRun        = "second"
	realThirdRun         = "third"
)

var errAdapterFault = errors.New("adapter fault")

func newAdapterStore(t *testing.T, unlocked bool) *execstore.Store {
	t.Helper()
	ctx := t.Context()
	db, err := storage.Open(ctx, storage.DBPath(t.TempDir()))
	if err != nil {
		t.Fatal("open database")
	}
	t.Cleanup(func() {
		if closeErr := db.Close(); closeErr != nil {
			t.Error("close database")
		}
	})
	clk := clock.NewFake(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	if err = storage.Migrate(ctx, db, clk); err != nil {
		t.Fatal("migrate database")
	}
	v, err := vault.NewService(ctx, vault.NewSQLKeyringStore(db), ids.NewSequence("vault"))
	if err != nil {
		t.Fatal("create vault")
	}
	if unlocked {
		if _, err = v.Setup(ctx, redact.NewSecret("adapter-test-master")); err != nil {
			t.Fatal("set up vault")
		}
	}
	s, err := execstore.New(ctx, db, v, storage.HostIncarnation{LockGeneration: 1, IncarnationID: "adapter-host", AcquiredAt: clk.Now()}, clk, ids.NewSequence("adapter-id"), ids.NewDeterministic("adapter-seal"), nil)
	if err != nil {
		t.Fatal("create execution store")
	}
	t.Cleanup(func() {
		if closeErr := s.Close(); closeErr != nil {
			t.Error("close execution store")
		}
	})
	return s
}

func adapterAccept() *coordinator.Transition {
	accepted := new(kernel.RunAcceptedEvent)
	accepted.Envelope = kernel.Envelope{RunID: adapterRun, Sequence: 1, CommandID: adapterAcceptCommand, CausationID: adapterAcceptCommand, Origin: adapterOriginUser, Timestamp: time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)}
	accepted.SessionID = adapterSession
	accepted.Budgets = kernel.DefaultRunBudgets()
	return &coordinator.Transition{SessionID: adapterSession, RunID: adapterRun, CommandID: adapterAcceptCommand, Kind: "AcceptRun", Principal: adapterOriginUser, Target: adapterRun, Fence: coordinator.Fence{OwnerIncarnation: 1}, MetadataInput: []byte(`{}`), Events: []kernel.Event{accepted}, Result: codec.ReceiptResult{FirstSequence: 1, LastSequence: 1}}
}

func TestAdapterClassifiesDefiniteRejections(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		err  error
	}{
		{"stale fence", execstore.ErrStaleFence},
		{"stale revision", execstore.ErrStaleRevision},
		{"receipt conflict", execstore.ErrReceiptConflict},
		{"invalid batch", execstore.ErrInvalidBatch},
		{"codec mismatch", execstore.ErrCodecVersionMismatch},
		{"unknown version", execstore.ErrUnknownVersion},
		{"purging", execstore.ErrSessionPurging},
		{"payload limit", execstore.ErrPayloadTooLarge},
		{"run limit", execstore.ErrRunPayloadLimitExceeded},
		{"session limit", execstore.ErrSessionPayloadLimitExceeded},
		{"protected input", execstore.ErrProtectedInputUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := newAdapterStore(t, false)
			s.SetFault(execstore.FaultBeforeCommit, fmt.Errorf("injected rejection: %w", tc.err))
			_, err := NewCoordinatorStore(s).Append(t.Context(), adapterAccept())
			if !errors.Is(err, tc.err) || errors.Is(err, coordinator.ErrCommitUnknown) {
				t.Fatal("typed rejection must remain definite")
			}
		})
	}
}

func TestAdapterReportsLostAcknowledgementAsUnknown(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	s.SetFault(execstore.FaultAfterCommit, errAdapterFault)
	adapter := NewCoordinatorStore(s)
	_, err := adapter.Append(t.Context(), adapterAccept())
	if !errors.Is(err, coordinator.ErrCommitUnknown) {
		t.Fatal("lost acknowledgement must be unknown")
	}
	status, receipt, err := adapter.ResolveCommit(t.Context(), adapterAcceptCommand)
	if err != nil || status != coordinator.CommitResolutionCommitted || receipt.CommandID != adapterAcceptCommand {
		t.Fatal("read-back must confirm the committed receipt")
	}
	status, _, err = adapter.ResolveCommit(t.Context(), "absent")
	if err != nil || status != coordinator.CommitResolutionNotCommitted {
		t.Fatal("missing receipt must resolve definitely not committed")
	}
}

func TestAdapterSealsEventPayloadsInTheSameTransaction(t *testing.T) {
	t.Parallel()
	for _, rollback := range []bool{false, true} {
		t.Run(map[bool]string{false: "committed", true: "rolled-back"}[rollback], func(t *testing.T) {
			t.Parallel()
			verifyPayloadTransaction(t, rollback)
		})
	}
}

func verifyPayloadTransaction(t *testing.T, rollback bool) {
	t.Helper()
	s := newAdapterStore(t, true)
	tr := adapterResponseBatch()
	if rollback {
		s.SetFault(execstore.FaultBeforeCommit, errAdapterFault)
	}
	res, err := NewCoordinatorStore(s).Append(t.Context(), tr)
	if rollback {
		if !errors.Is(err, coordinator.ErrCommitUnknown) {
			t.Fatal("untyped append failure must be unknown")
		}
		assertResponseRolledBack(t, s)
		return
	}
	if err != nil {
		t.Fatal("append response")
	}
	assertResponseCommitted(t, s, &res)
	if _, err = NewCoordinatorStore(s).Append(t.Context(), tr); err != nil {
		t.Fatal("protected canonical input must support duplicate equality")
	}
}

func assertResponseRolledBack(t *testing.T, s *execstore.Store) {
	t.Helper()
	events, err := s.ReadEvents(t.Context(), adapterRun, 1, 5)
	if err != nil || len(events) != 0 {
		t.Fatal("rollback must persist no events")
	}
	_, err = s.OpenPayload(t.Context(), adapterSession, "response")
	if errcode.CodeOf(err) != errcode.NotFound {
		t.Fatal("rollback must persist no response payload")
	}
}

func assertResponseCommitted(t *testing.T, s *execstore.Store, res *coordinator.AppendResult) {
	t.Helper()
	if res.RunRevision != 1 || res.AdmissionRevision != 1 {
		t.Fatal("append must return actual revisions")
	}
	events, err := s.ReadEvents(t.Context(), adapterRun, 1, 5)
	if err != nil || len(events) != 5 {
		t.Fatal("all response events must commit")
	}
	opened, err := s.OpenPayload(t.Context(), adapterSession, "response")
	if err != nil || !bytes.Equal(opened, []byte("sample response")) {
		t.Fatal("response payload must commit")
	}
	canonical, err := s.OpenPayload(t.Context(), adapterSession, res.Receipt.InputPayloadID)
	if err != nil || !bytes.Equal(canonical, []byte(`{"command":"response"}`)) {
		t.Fatal("canonical input must also be sealed")
	}
}

func adapterResponseBatch() *coordinator.Transition {
	tr := adapterAccept()
	env := func(seq uint64) kernel.Envelope {
		e := tr.Events[0].EventEnvelope()
		e.Sequence = seq
		e.Token = 1
		return e
	}
	admitted := new(kernel.RunAdmittedEvent)
	admitted.Envelope = env(2)
	admitted.Token = 1
	admitted.OwnerIncarnation = 1
	admitted.ContextEpoch = 1
	admitted.InitialContextRevision = 1
	admitted.StartRunID = adapterRun
	step := new(kernel.StepStartedEvent)
	step.Envelope = env(3)
	step.StepIndex = 1
	plan := new(kernel.ModelRequestPlannedEvent)
	plan.Envelope = env(4)
	plan.InvocationID = adapterModelID
	plan.FirstAttemptID = "attempt"
	response := new(kernel.ModelResponseRecordedEvent)
	response.Envelope = env(5)
	response.InvocationID = adapterModelID
	response.AttemptID = "attempt"
	response.ResponseRef = "response"
	response.UsageStatus = "known"
	response.FinishReason = kernel.FinishReasonStop
	tr.Events = append(tr.Events, admitted, step, plan, response)
	tr.Fence.AdmissionToken = 1
	tr.MetadataInput = nil
	tr.ProtectedInput = []byte(`{"command":"response"}`)
	tr.Payloads = []coordinator.Payload{{ID: "response", Kind: "model_response", Plaintext: []byte("sample response")}}
	tr.Result.LastSequence = 5
	return tr
}

func TestAdapterSealFailureIsDefinite(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	tr := adapterAccept()
	tr.Payloads = []coordinator.Payload{{ID: "input", Kind: "user_input", Plaintext: []byte("sample input")}}
	_, err := NewCoordinatorStore(s).Append(t.Context(), tr)
	if !errors.Is(err, vault.ErrLocked) || errors.Is(err, coordinator.ErrCommitUnknown) {
		t.Fatal("seal failure must remain definite")
	}
	if _, found, readErr := s.LookupReceipt(t.Context(), adapterAcceptCommand); readErr != nil || found {
		t.Fatal("seal failure must not append")
	}
}

func TestAdapterLoadsStateAdmissionAndUnavailableCommit(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	adapter := NewCoordinatorStore(s)
	state, rev, err := adapter.LoadRun(t.Context(), adapterRun)
	if err != nil || rev != 0 || state == nil || state.Accepted {
		t.Fatal("new run must have empty state")
	}
	if _, err = adapter.Append(t.Context(), adapterAccept()); err != nil {
		t.Fatal("append acceptance")
	}
	state, rev, err = adapter.LoadRun(t.Context(), adapterRun)
	if err != nil || rev != 1 || !state.Accepted {
		t.Fatal("load run must rebuild stored events and return actual revision")
	}
	adm, err := adapter.LoadAdmission(t.Context(), adapterSession)
	if err != nil || adm.Revision != 1 || adm.SessionID != adapterSession {
		t.Fatal("load admission must return the committed projection")
	}
}

func TestAdapterUnavailableCommit(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	adapter := NewCoordinatorStore(s)
	if err := s.DB().Close(); err != nil {
		t.Fatal("close database")
	}
	status, _, err := adapter.ResolveCommit(t.Context(), adapterAcceptCommand)
	if status != coordinator.CommitResolutionUnavailable || !errors.Is(err, execstore.ErrStoreUnavailable) {
		t.Fatal("unavailable store must not resolve commit")
	}
}

func TestAdapterMarksRevisionsUnavailableAfterConfirmedCommit(t *testing.T) {
	t.Parallel()
	s := newAdapterStore(t, false)
	adapter := &coordinatorStore{store: s}
	tr := adapterAccept()
	committed, err := adapter.Append(t.Context(), tr)
	if err != nil {
		t.Fatal("append acceptance")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result := adapter.confirmedResult(ctx, tr, &execstore.AppendOutcome{Committed: true, Receipt: committed.Receipt})
	if !result.RevisionsUnavailable || result.RunRevision != 0 || result.AdmissionRevision != 0 || result.Receipt.CommandID != adapterAcceptCommand {
		t.Fatal("confirmed commit must retain receipt and mark failed revision reads without guessing")
	}
}

// This catches a coordinator admission that never reaches the durable row.
func TestAutomaticAdmissionPersistsCallerPlanThroughRealStore(t *testing.T) {
	t.Parallel()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	barrier := &realAdmissionBarrier{Store: NewCoordinatorStore(s), reached: make(chan string, 8), release: make(chan struct{}), results: make(chan error, 8)}
	close(barrier.release)
	sess := startRealSession(t, barrier, clk, "auto")
	req := realAccept(realFirstRun)
	if _, err := sess.Submit(t.Context(), req); err != nil {
		t.Fatal("accept run")
	}
	select {
	case appendErr := <-barrier.results:
		if appendErr != nil {
			t.Fatal("automatic admission append must commit")
		}
	default:
		t.Fatal("automatic admission did not append")
	}
	assertInitialAdmission(t, s)
}

func TestConcurrentAdmissionOnlyOneCommits(t *testing.T) {
	t.Parallel()
	s, clk, closeHost := openCoordinatorHost(t, t.TempDir())
	t.Cleanup(closeHost)
	barrier := &realAdmissionBarrier{
		Store: NewCoordinatorStore(s), reached: make(chan string, 8),
		release: make(chan struct{}), results: make(chan error, 8),
	}
	first := startRealSession(t, barrier, clk, "first-owner")
	second := startRealSession(t, barrier, clk, "second-owner")
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	one := supervise.Go(ctx, "test.first_accept", func(taskCtx context.Context) error {
		return submitRealRequest(taskCtx, first, realAccept(realFirstRun))
	})
	defer stopSubmission(one)
	waitAdmissionBarrier(t, ctx, barrier.reached)
	two := supervise.Go(ctx, "test.second_accept", func(taskCtx context.Context) error {
		return submitRealRequest(taskCtx, second, realAccept(realSecondRun))
	})
	defer stopSubmission(two)
	waitAdmissionBarrier(t, ctx, barrier.reached)
	close(barrier.release)
	if one.Wait() != nil || two.Wait() != nil {
		t.Fatal("both accepts must commit before competing admissions")
	}
	assertSingleAdmission(t, s)
	left, right := <-barrier.results, <-barrier.results
	if (left == nil) == (right == nil) {
		t.Fatal("exactly one admission append must commit")
	}
	loser := left
	if loser == nil {
		loser = right
	}
	if !errors.Is(loser, execstore.ErrStaleRevision) || errors.Is(loser, coordinator.ErrCommitUnknown) {
		t.Fatal("losing admission must be a definite ErrStaleRevision")
	}
	assertRejectedAdmissionHasOnlyAcceptance(t, s, realFirstRun)
}
