package app

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/config"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/execstore"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
	"github.com/syurodev/fugue/internal/platform/redact"
	"github.com/syurodev/fugue/internal/platform/supervise"
	"github.com/syurodev/fugue/internal/storage"
	"github.com/syurodev/fugue/internal/vault"
)

func openCoordinatorHost(t *testing.T, dir string) (*execstore.Store, *clock.Fake, func()) {
	t.Helper()
	clk := clock.NewFake(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
	host, err := storage.OpenStore(t.Context(), config.Config{DataDir: dir}, clk, ids.NewSequence("host"))
	if err != nil {
		t.Fatal("open host store")
	}
	v, err := vault.NewService(t.Context(), vault.NewSQLKeyringStore(host.DB()), ids.NewSequence("vault"))
	if err != nil {
		t.Fatal("create host vault")
	}
	if v.Snapshot().State == vault.StateNotConfigured {
		if _, err = v.Setup(t.Context(), redact.NewSecret("real-store-master")); err != nil {
			t.Fatal("set up host vault")
		}
	} else if err = v.Unlock(t.Context(), redact.NewSecret("real-store-master")); err != nil {
		t.Fatal("unlock host vault")
	}
	s, err := execstore.New(t.Context(), host.DB(), v, host.Incarnation(), clk, ids.NewSequence("exec"), ids.NewDeterministic("seal"), nil)
	if err != nil {
		t.Fatal("create host execution store")
	}
	var once sync.Once
	return s, clk, func() {
		once.Do(func() {
			if err := s.Close(); err != nil {
				t.Error("close execution store")
			}
			if err := host.Close(); err != nil {
				t.Error("close host store")
			}
		})
	}
}

type realExecutor struct {
	work         chan coordinator.Work
	observations chan *coordinator.Observation
	calls        atomic.Int32
	scripted     bool
}

func newRealExecutor(scripted bool) *realExecutor {
	return &realExecutor{work: make(chan coordinator.Work, 8), observations: make(chan *coordinator.Observation, 8), scripted: scripted}
}

func (e *realExecutor) Execute(ctx context.Context, w *coordinator.Work, report func(*coordinator.Observation)) {
	e.calls.Add(1)
	select {
	case e.work <- *w:
	case <-ctx.Done():
		return
	}
	if e.scripted {
		obs := realResponse(w)
		report(obs)
		select {
		case e.observations <- obs:
		case <-ctx.Done():
		}
	}
}

func startRealSession(t *testing.T, store coordinator.Store, clk clock.Clock, prefix string) *coordinator.Session {
	t.Helper()
	return startRealSessionWith(t, store, clk, prefix, 1, newRealExecutor(false))
}

func startRealSessionWith(t *testing.T, store coordinator.Store, clk clock.Clock, prefix string, incarnation uint64, executor coordinator.Executor) *coordinator.Session {
	t.Helper()
	sess, err := coordinator.Start(t.Context(), &coordinator.Config{
		SessionID: adapterSession, OwnerIncarnation: incarnation, WorkerCapacity: 1,
		Store: store, Clock: clk, IDs: ids.NewSequence(prefix), Executor: executor,
	})
	if err != nil {
		t.Fatal("start real-store session")
	}
	t.Cleanup(sess.Stop)
	return sess
}

func realAccept(runID string) *coordinator.Request {
	cmd := new(kernel.AcceptRunCommand)
	cmd.CommandID, cmd.RunID, cmd.SessionID = "accept-"+runID, runID, adapterSession
	cmd.InputRef, cmd.QueueItemRef, cmd.ProtocolBinding = "input-"+runID, "queue-"+runID, "binding"
	cmd.Budgets = kernel.DefaultRunBudgets()
	plan := new(kernel.ModelRequestPlannedEvent)
	plan.InvocationID, plan.FirstAttemptID = "model-"+runID, "attempt-"+runID
	plan.Purpose, plan.ModelRef, plan.ContextRef = "turn", adapterModelID, "context-"+runID
	return &coordinator.Request{
		Command: cmd, Origin: adapterOriginUser, QueueDecisionRef: "decision-" + runID,
		Facts: kernel.DecisionFacts{ModelPlan: plan}, MetadataInput: []byte(`{}`),
		Payloads: []coordinator.Payload{{ID: cmd.InputRef, Kind: "user_input", Plaintext: []byte("sample input")}},
	}
}

// The barrier delays only admission, leaving both accepts and admission appends real.
type realAdmissionBarrier struct {
	coordinator.Store
	reached chan string
	release chan struct{}
	results chan error
}

func (b *realAdmissionBarrier) Append(ctx context.Context, tr *coordinator.Transition) (coordinator.AppendResult, error) {
	if tr.Kind != "AdmitRun" {
		return appendRealStore(ctx, b.Store, tr)
	}
	select {
	case b.reached <- tr.RunID:
	case <-ctx.Done():
		return coordinator.AppendResult{}, fmt.Errorf("admission barrier: %w", ctx.Err())
	}
	select {
	case <-b.release:
	case <-ctx.Done():
		return coordinator.AppendResult{}, fmt.Errorf("admission barrier: %w", ctx.Err())
	}
	res, err := appendRealStore(ctx, b.Store, tr)
	select {
	case b.results <- err:
	case <-ctx.Done():
	}
	return res, err
}

func waitAdmissionBarrier(t *testing.T, ctx context.Context, reached <-chan string) {
	t.Helper()
	select {
	case <-reached:
	case <-ctx.Done():
		t.Fatalf("admission did not reach real append barrier: %v", ctx.Err())
	}
}

func assertSingleAdmission(t *testing.T, s *execstore.Store) {
	t.Helper()
	count := 0
	for _, runID := range []string{realFirstRun, realSecondRun} {
		events, err := s.ReadEvents(t.Context(), runID, 1, 20)
		if err != nil {
			t.Fatal("read competing run events")
		}
		for _, event := range events {
			if _, ok := event.(*kernel.RunAdmittedEvent); ok {
				count++
			}
		}
	}
	if count != 1 {
		t.Fatalf("committed RunAdmitted count = %d, want 1", count)
	}
}

func realDispatch(runID string) *coordinator.Request {
	cmd := new(kernel.PrepareDispatchCommand)
	cmd.CommandID, cmd.RunID, cmd.Token = "dispatch-"+runID, runID, 1
	cmd.InvocationID, cmd.AttemptID = "model-"+runID, "attempt-"+runID
	dispatch := new(kernel.ModelDispatchStartedEvent)
	dispatch.InvocationID, dispatch.AttemptID = cmd.InvocationID, cmd.AttemptID
	dispatch.ProviderSettingsRef, dispatch.AccountID, dispatch.CatalogVersion = "settings", "account", "catalog"
	return &coordinator.Request{
		Command: cmd, Origin: "system",
		ProtectedInput: []byte(`{"kind":"PrepareDispatch"}`),
		Facts:          kernel.DecisionFacts{ModelDispatch: dispatch, DispatchAuthorizationRef: "authorization-" + runID},
	}
}

func realResponse(w *coordinator.Work) *coordinator.Observation {
	cmd := new(kernel.ObserveModelResponseCommand)
	cmd.CommandID, cmd.RunID = "response-"+w.Lease.RunID, w.Lease.RunID
	cmd.InvocationID, cmd.AttemptID = w.Lease.InvocationID, w.Lease.AttemptID
	cmd.ResponseRef, cmd.UsageStatus, cmd.FinishReason = "response-payload-"+w.Lease.RunID, "known", kernel.FinishReasonStop
	usage := new(kernel.BudgetObservedEvent)
	usage.AttemptID, usage.TokensUsed, usage.Basis = w.Lease.AttemptID, 3, "provider"
	return &coordinator.Observation{
		Lease: w.Lease, Command: cmd, Facts: kernel.DecisionFacts{Usage: usage},
		ProtectedInput: []byte(`{"kind":"ObserveModelResponse","finish_reason":"stop"}`),
		Payloads:       []coordinator.Payload{{ID: cmd.ResponseRef, Kind: "model_response", Plaintext: []byte("sample response")}},
	}
}

func submitRealAccept(t *testing.T, sess *coordinator.Session, runID string) {
	t.Helper()
	if _, err := sess.Submit(t.Context(), realAccept(runID)); err != nil {
		t.Fatalf("accept %s must commit; stale fence=%t", runID, errors.Is(err, execstore.ErrStaleFence))
	}
}

func stopSubmission(task *supervise.Task) {
	task.Stop()
	if err := task.Wait(); err != nil {
		return
	}
}

func waitRealWork(t *testing.T, ctx context.Context, executor *realExecutor) coordinator.Work {
	t.Helper()
	select {
	case w := <-executor.work:
		return w
	case <-ctx.Done():
		t.Fatalf("executor did not receive committed work: %v", ctx.Err())
		return coordinator.Work{}
	}
}

func waitRealObservation(t *testing.T, ctx context.Context, executor *realExecutor) *coordinator.Observation {
	t.Helper()
	select {
	case obs := <-executor.observations:
		return obs
	case <-ctx.Done():
		t.Fatalf("executor did not report scripted observation: %v", ctx.Err())
		return nil
	}
}

func assertRejectedAdmissionHasOnlyAcceptance(t *testing.T, s *execstore.Store, runID string) {
	t.Helper()
	events, err := s.ReadEvents(t.Context(), runID, 1, 20)
	if err != nil || len(events) != 1 {
		t.Fatal("losing admission must append no events after its earlier acceptance")
	}
	if _, ok := events[0].(*kernel.RunAcceptedEvent); !ok {
		t.Fatal("only the earlier RunAccepted may remain")
	}
}

type realLostAckStore struct {
	coordinator.Store
	store         *execstore.Store
	committed     chan struct{}
	resolved      chan string
	stopBeforeAck bool
}

func (s *realLostAckStore) Append(ctx context.Context, tr *coordinator.Transition) (coordinator.AppendResult, error) {
	if tr.Kind != "PrepareDispatch" {
		return appendRealStore(ctx, s.Store, tr)
	}
	s.store.SetFault(execstore.FaultAfterCommit, errAdapterFault)
	res, err := appendRealStore(ctx, s.Store, tr)
	s.store.SetFault(execstore.FaultAfterCommit, nil)
	if s.stopBeforeAck && errors.Is(err, coordinator.ErrCommitUnknown) {
		close(s.committed)
		<-ctx.Done()
	}
	return res, err
}

func (s *realLostAckStore) ResolveCommit(ctx context.Context, commandID string) (coordinator.CommitResolution, codec.Receipt, error) {
	status, receipt, err := s.Store.ResolveCommit(ctx, commandID)
	if status == coordinator.CommitResolutionCommitted {
		select {
		case s.resolved <- receipt.CommandID:
		case <-ctx.Done():
		}
	}
	if err != nil {
		return status, receipt, fmt.Errorf("real resolve: %w", err)
	}
	return status, receipt, nil
}

func appendRealStore(ctx context.Context, store coordinator.Store, tr *coordinator.Transition) (coordinator.AppendResult, error) {
	res, err := store.Append(ctx, tr)
	if err != nil {
		return res, fmt.Errorf("real append: %w", err)
	}
	return res, nil
}

func submitRealRequest(ctx context.Context, sess *coordinator.Session, req *coordinator.Request) error {
	if _, err := sess.Submit(ctx, req); err != nil {
		return fmt.Errorf("real submit: %w", err)
	}
	return nil
}

func assertInitialAdmission(t *testing.T, s *execstore.Store) {
	t.Helper()
	assertInitialAdmissionRow(t, s)
	events, err := s.ReadEvents(t.Context(), realFirstRun, 1, 4)
	if err != nil || len(events) != 4 {
		t.Fatal("automatic admission must persist the caller model plan")
	}
	planned, ok := events[3].(*kernel.ModelRequestPlannedEvent)
	if !ok || planned.InvocationID != "model-first" {
		t.Fatal("automatic admission must retain the caller model identity")
	}
	receipt, found, err := s.LookupReceipt(t.Context(), events[1].EventEnvelope().CommandID)
	if err != nil || !found || !bytes.Equal(receipt.CanonicalInput, []byte(`{"ContextEpoch":1,"token":1}`)) {
		t.Fatal("automatic admission canonical input must contain the actual token and epoch")
	}
}

func assertDispatchReadBack(t *testing.T, store *realLostAckStore) {
	t.Helper()
	select {
	case commandID := <-store.resolved:
		if commandID != "dispatch-first" {
			t.Fatal("coordinator must read back the dispatch command identity")
		}
	default:
		t.Fatal("coordinator did not resolve its unknown append")
	}
}

func assertInitialAdmissionRow(t *testing.T, s *execstore.Store) {
	t.Helper()
	adm, err := s.ReadAdmission(t.Context(), adapterSession)
	if err != nil || adm.ActiveRunID != realFirstRun || adm.ContextStartRunID != realFirstRun || adm.ContextEpoch != 1 || adm.ContextRevision != 1 {
		t.Fatal("automatic admission must persist the first context boundary")
	}
}
