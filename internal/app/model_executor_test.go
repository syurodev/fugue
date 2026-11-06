package app

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/codec"
	"github.com/syurodev/fugue/internal/coordinator"
	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/modeladapter"
	"github.com/syurodev/fugue/internal/modeladapter/faux"
	"github.com/syurodev/fugue/internal/platform/clock"
	"github.com/syurodev/fugue/internal/platform/ids"
)

var errUnknownPayload = errors.New("unknown payload")

func execClock() clock.Clock {
	return clock.NewFake(time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC))
}

func execGen(seed string) ids.Generator {
	return ids.NewUUIDv7(execClock(), ids.NewDeterministic(seed))
}

// Literal values the executor tests repeat; goconst wants them named. The
// adapterOriginSystem and adapterUsageKnown constants live with the adapter
// evidence tests.
const (
	execModelRef    = "faux-model"
	execAuthRef     = "auth-1"
	execCallID      = "call-1"
	execArgsRef     = "args-1"
	execInputRef    = "input-1"
	contextRoleUser = "user"
	testContextRef  = "ctx-1"
)

// fakeSource answers the executors' reads from memory: the folded state, the
// run's events (the model plan lives only in the stream) and payload bytes.
type fakeSource struct {
	mu       sync.Mutex
	events   map[string][]kernel.Event
	payloads map[string][]byte
}

func newFakeSource(events []kernel.Event, payloads map[string][]byte) *fakeSource {
	return &fakeSource{events: map[string][]kernel.Event{"r1": events}, payloads: payloads}
}

func (s *fakeSource) LoadRun(_ context.Context, runID string) (*kernel.State, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := kernel.NewState()
	for _, ev := range s.events[runID] {
		state = kernel.Evolve(state, ev)
	}
	return state, uint64(len(s.events[runID])), nil
}

func (s *fakeSource) ReadEvents(_ context.Context, runID string, fromSequence, _ uint64) ([]kernel.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []kernel.Event
	for _, ev := range s.events[runID] {
		if ev.EventEnvelope().Sequence >= fromSequence {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *fakeSource) OpenPayload(_ context.Context, _, payloadID string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	plaintext, ok := s.payloads[payloadID]
	if !ok {
		return nil, errUnknownPayload
	}
	return slices.Clone(plaintext), nil
}

// reportCapture records what an executor reports through the worker channel.
type reportCapture struct {
	mu  sync.Mutex
	obs []*coordinator.Observation
}

func (c *reportCapture) report(obs *coordinator.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.obs = append(c.obs, obs)
}

func (c *reportCapture) all() []*coordinator.Observation {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.obs)
}

func (c *reportCapture) single(t *testing.T) *coordinator.Observation {
	t.Helper()
	obs := c.all()
	if len(obs) != 1 {
		t.Fatalf("executor reported %d observations, want 1", len(obs))
	}
	return obs[0]
}

// seededContext seals a step context the way production would and returns the
// payload map a fakeSource reads from.
func seededContext(t *testing.T, items []contextItem) map[string][]byte {
	t.Helper()
	payload, err := newContext(ids.NewSequence("seed"), items)
	if err != nil {
		t.Fatalf("newContext: %v", err)
	}
	return map[string][]byte{testContextRef: payload.Plaintext}
}

// runEventEnvelope is the envelope of a host-command event in the test run.
func runEventEnvelope(sequence uint64, command string) kernel.Envelope {
	return kernel.Envelope{RunID: "r1", Sequence: sequence, CommandID: command, CausationID: command, Origin: "session"}
}

// modelRunEvents folds the honest prefix of a Trace A run up to the dispatch:
// the step's plan names the context payload, the dispatch fences the attempt.
// testModelFence is the executor fence the folded dispatch carries.
const testModelFence = 2

func modelRunEvents(fence uint64) []kernel.Event {
	plan := &kernel.ModelRequestPlannedEvent{
		Envelope:              runEventEnvelope(1, "cmd-1"),
		InvocationID:          "m1",
		Purpose:               purposeTurn,
		FirstAttemptID:        "a1",
		ModelRef:              execModelRef,
		ContextRef:            "ctx-1",
		ToolDeclarationRefs:   nil,
		EstimatedBudgetTokens: 0,
	}
	dispatch := &kernel.ModelDispatchStartedEvent{
		Envelope:            runEventEnvelope(2, "cmd-2"),
		AttemptID:           "a1",
		InvocationID:        "m1",
		Token:               1,
		ExecutorFence:       fence,
		ReservationTokens:   0,
		ProviderSettingsRef: execAuthRef,
		AccountID:           execAuthRef,
		CatalogVersion:      "v1",
	}
	return []kernel.Event{plan, dispatch}
}

func modelWork() *coordinator.Work {
	dispatch := &kernel.ModelDispatchStartedEvent{
		Envelope:            kernel.Envelope{},
		AttemptID:           "a1",
		InvocationID:        "m1",
		Token:               1,
		ExecutorFence:       testModelFence,
		ReservationTokens:   0,
		ProviderSettingsRef: execAuthRef,
		AccountID:           execAuthRef,
		CatalogVersion:      "v1",
	}
	lease := coordinator.Lease{
		SessionID: "s1", RunID: "r1", InvocationID: "m1", AttemptID: "a1",
		AdmissionToken: 1, OwnerIncarnation: 1, ExecutorFence: testModelFence,
	}
	return &coordinator.Work{Lease: lease, Dispatch: dispatch}
}

func payloadKinds(payloads []coordinator.Payload) map[string]string {
	kinds := make(map[string]string, len(payloads))
	for _, p := range payloads {
		kinds[p.Kind] = p.ID
	}
	return kinds
}

func streamScript(events []modeladapter.Event) *faux.Provider {
	return faux.New(faux.Script{Events: events})
}

func TestModelExecutorReportsStreamThenResponse(t *testing.T) {
	t.Parallel()
	gen := execGen("model-stream")

	script := streamScript([]modeladapter.Event{
		{Kind: modeladapter.EventStart},
		{Kind: modeladapter.EventTextDelta, Delta: "hello"},
		{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonStop, Usage: modeladapter.Usage{Input: 10, Output: 5, CacheRead: 2, Reasoning: 1}},
	})
	capture := &reportCapture{}
	executor := &modelExecutor{provider: script, ids: gen, source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, []contextItem{{Role: "user", Ref: "input-1", Content: []byte("the input")}}))}

	executor.Execute(t.Context(), modelWork(), capture.report)

	obs := capture.all()
	if len(obs) != 2 {
		t.Fatalf("executor reported %d observations, want 2", len(obs))
	}
	requireStreamObservation(t, obs[0])
	requireStopResponse(t, obs[1])
}

func requireStreamObservation(t *testing.T, obs *coordinator.Observation) {
	t.Helper()
	stream, ok := obs.Command.(*kernel.ObserveModelStreamCommand)
	if !ok {
		t.Fatalf("first observation is %T, want ObserveModelStreamCommand", obs.Command)
	}
	if stream.FirstChunkRef == "" || stream.InvocationID != "m1" || stream.AttemptID != "a1" {
		t.Fatalf("stream observation incomplete: %+v", stream)
	}
	if obs.Lease.ExecutorFence != testModelFence {
		t.Fatalf("stream observation lost the lease fence")
	}
}

func requireStopResponse(t *testing.T, obs *coordinator.Observation) {
	t.Helper()
	response, ok := obs.Command.(*kernel.ObserveModelResponseCommand)
	if !ok {
		t.Fatalf("second observation is %T, want ObserveModelResponseCommand", obs.Command)
	}
	if response.ResponseRef == "" || response.UsageStatus != adapterUsageKnown || response.FinishReason != kernel.FinishReasonStop {
		t.Fatalf("response observation incomplete: %+v", response)
	}
	usage := obs.Facts.Usage
	if usage == nil || usage.TokensUsed != 14 || usage.Basis != "provider_reported" || usage.AttemptID != "a1" {
		t.Fatalf("usage observation wrong: %+v", usage)
	}
	if len(obs.Facts.ToolPlans) != 0 {
		t.Fatalf("a stop answer must not plan tools")
	}
	if payloadKinds(obs.Payloads)["model_response"] != response.ResponseRef {
		t.Fatalf("the response payload is not sealed with the command: %v", payloadKinds(obs.Payloads))
	}
}

func TestStopReasonsMapToFinishReasons(t *testing.T) {
	t.Parallel()
	gen := execGen("stop-reasons")

	cases := []struct {
		name       string
		events     []modeladapter.Event
		wantFinish kernel.FinishReason
		wantPlans  int
	}{
		{"stop", []modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonStop, Usage: modeladapter.Usage{Output: 1}}}, kernel.FinishReasonStop, 0},
		{"length", []modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonLength, Usage: modeladapter.Usage{Output: 1}}}, kernel.FinishReasonLength, 0},
		{"refusal", []modeladapter.Event{{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonRefusal, Usage: modeladapter.Usage{Output: 1}}}, kernel.FinishReasonRefusal, 0},
		{"tool_use", []modeladapter.Event{
			{Kind: modeladapter.EventStart},
			{Kind: modeladapter.EventToolCallStart, CallID: execCallID, Name: "pure_read"},
			{Kind: modeladapter.EventToolCallDelta, Delta: `{"Key":"k"}`},
			{Kind: modeladapter.EventToolCallEnd},
			{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonToolUse, Usage: modeladapter.Usage{Output: 3}},
		}, kernel.FinishReasonToolUse, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			capture := &reportCapture{}
			executor := &modelExecutor{provider: streamScript(tc.events), ids: gen, source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil))}
			executor.Execute(t.Context(), modelWork(), capture.report)
			requireFinishReason(t, capture.all(), tc.wantFinish, tc.wantPlans)
		})
	}
}

func requireFinishReason(t *testing.T, obs []*coordinator.Observation, wantFinish kernel.FinishReason, wantPlans int) {
	t.Helper()
	var reported *coordinator.Observation
	for _, o := range obs {
		if _, ok := o.Command.(*kernel.ObserveModelResponseCommand); ok {
			reported = o
		}
	}
	if reported == nil {
		t.Fatalf("no response reported for %s: %d observations", wantFinish, len(obs))
	}
	cmd, ok := reported.Command.(*kernel.ObserveModelResponseCommand)
	if !ok {
		t.Fatalf("reported %T", reported.Command)
	}
	if cmd.FinishReason != wantFinish {
		t.Fatalf("finish reason %q, want %q", cmd.FinishReason, wantFinish)
	}
	if len(reported.Facts.ToolPlans) != wantPlans {
		t.Fatalf("%d tool plans, want %d", len(reported.Facts.ToolPlans), wantPlans)
	}
	if wantPlans == 1 {
		requireToolPlan(t, reported)
	}
}

func requireToolPlan(t *testing.T, reported *coordinator.Observation) {
	t.Helper()
	plan := reported.Facts.ToolPlans[0]
	if plan.ToolCallID != execCallID || plan.SourcePosition != 0 ||
		plan.EffectClass != kernel.EffectClassPureRead || plan.InputRef == "" ||
		plan.FirstAttemptID == "" || plan.DescriptorVersion == "" {
		t.Fatalf("tool plan incomplete: %+v", plan)
	}
	if payloadKinds(reported.Payloads)["tool_args"] != plan.InputRef {
		t.Fatalf("the args payload is not sealed with the response")
	}
}

func TestUsageCountsNewTokensOnly(t *testing.T) {
	t.Parallel()
	gen := execGen("usage")

	capture := &reportCapture{}
	executor := &modelExecutor{
		provider: streamScript([]modeladapter.Event{
			{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonStop, Usage: modeladapter.Usage{Input: 100, CacheRead: 40, Output: 30, Reasoning: 10}},
		}),
		ids: gen, source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil)),
	}
	executor.Execute(t.Context(), modelWork(), capture.report)

	usage := capture.single(t).Facts.Usage
	if usage.TokensUsed != 100-40+30+10 {
		t.Fatalf("usage counted %d tokens, want %d", usage.TokensUsed, 100-40+30+10)
	}
}

func TestUnparsableToolArgsFailTheAttempt(t *testing.T) {
	t.Parallel()
	gen := execGen("bad-args")

	capture := &reportCapture{}
	executor := &modelExecutor{
		provider: streamScript([]modeladapter.Event{
			{Kind: modeladapter.EventStart},
			{Kind: modeladapter.EventToolCallStart, CallID: execCallID, Name: "pure_read"},
			{Kind: modeladapter.EventToolCallDelta, Delta: `not json`},
			{Kind: modeladapter.EventToolCallEnd},
			{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonToolUse, Usage: modeladapter.Usage{Output: 3}},
		}),
		ids: gen, source: newFakeSource(modelRunEvents(testModelFence), seededContext(t, nil)),
	}
	executor.Execute(t.Context(), modelWork(), capture.report)

	for _, obs := range capture.all() {
		if _, ok := obs.Command.(*kernel.ObserveModelResponseCommand); ok {
			t.Fatalf("a response was reported despite unparsable arguments")
		}
	}
}

// appStore is a minimal coordinator.Store for executor-level session tests:
// it folds committed events so the coordinator's cache and the store agree.
type appStore struct {
	mu        sync.Mutex
	runs      map[string]*kernel.State
	revs      map[string]uint64
	admission coordinator.Admission
	appends   int
}

func newAppStore(admitted *kernel.State) *appStore {
	return &appStore{
		runs:      map[string]*kernel.State{"r1": admitted},
		revs:      map[string]uint64{"r1": 1},
		admission: coordinator.Admission{SessionID: "s1", ActiveRunID: "r1", OwnerIncarnation: 1, AdmissionToken: 1, Revision: 1},
	}
}

func (s *appStore) Append(_ context.Context, t *coordinator.Transition) (coordinator.AppendResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[t.RunID]
	if state == nil {
		state = kernel.NewState()
	}
	for _, ev := range t.Events {
		state = kernel.Evolve(state, ev)
	}
	s.runs[t.RunID] = state
	s.revs[t.RunID]++
	s.admission.Revision++
	s.appends++
	if state.Admitted && !state.Finished {
		s.admission.ActiveRunID = t.RunID
		s.admission.AdmissionToken = state.AdmissionToken
	}
	return coordinator.AppendResult{RunRevision: s.revs[t.RunID], AdmissionRevision: s.admission.Revision}, nil
}

func (s *appStore) ResolveCommit(_ context.Context, _ string) (coordinator.CommitResolution, codec.Receipt, error) {
	return coordinator.CommitResolutionUnavailable, codec.Receipt{}, nil
}

func (s *appStore) LoadRun(_ context.Context, runID string) (*kernel.State, uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.runs[runID]
	if state == nil {
		return nil, 0, nil
	}
	return state, s.revs[runID], nil
}

func (s *appStore) LoadAdmission(_ context.Context, _ string) (coordinator.Admission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.admission, nil
}

func (s *appStore) ListArmedTimers(_ context.Context) ([]coordinator.ArmedTimer, error) {
	return nil, nil
}

func (s *appStore) appendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appends
}

// admittedModelState is the run state a session starts a Trace A dispatch from:
// admitted, step open, model attempt planned.
func admittedModelState() *kernel.State {
	state := kernel.NewState()
	state.RunID = "r1"
	state.Admitted = true
	state.AdmissionToken = 1
	state.OwnerIncarnation = 1
	state.LastSequence = 2
	state.StepStarted = true
	state.CurrentStepIndex = 1
	state.CurrentModelInvocationID = "m1"
	state.ModelInvocations["m1"] = &kernel.ModelInvocation{
		InvocationID:      "m1",
		Planned:           true,
		Completed:         false,
		Failed:            false,
		Closed:            false,
		SelectedAttemptID: "",
		Attempts:          map[string]*kernel.ModelAttempt{"a1": {AttemptID: "a1", Status: kernel.ModelAttemptStatusPlanned}},
	}
	return state
}

func dispatchRequest(cmdID string) *coordinator.Request {
	cmd := &kernel.PrepareDispatchCommand{CommandID: cmdID, RunID: "r1", Origin: "", Token: 1, InvocationID: "m1", AttemptID: "a1"}
	dispatch := &kernel.ModelDispatchStartedEvent{
		Envelope:            kernel.Envelope{},
		AttemptID:           "a1",
		InvocationID:        "m1",
		Token:               1,
		ExecutorFence:       3,
		ReservationTokens:   0,
		ProviderSettingsRef: execAuthRef,
		AccountID:           execAuthRef,
		CatalogVersion:      "v1",
	}
	return &coordinator.Request{
		Command:        cmd,
		Origin:         adapterOriginSystem,
		ProtectedInput: []byte(`{"CommandID":"` + cmdID + `"}`),
		Facts:          kernel.DecisionFacts{ModelDispatch: dispatch, DispatchAuthorizationRef: execAuthRef},
		Kind:           "PrepareDispatch", Principal: adapterOriginSystem, Target: "r1",
	}
}

// The failure path drives a real session so the slot mechanics are the
// coordinator's own.
func TestModelFailureLeavesAttemptRequestingAndFreesSlot(t *testing.T) {
	t.Parallel()
	gen := execGen("failure")

	store := newAppStore(admittedModelState())
	executor := &modelExecutor{
		provider: streamScript([]modeladapter.Event{
			{Kind: modeladapter.EventDone, StopReason: modeladapter.StopReasonError, Usage: modeladapter.Usage{Output: 1}},
		}),
		ids: gen, source: newFakeSource(modelRunEvents(3), seededContext(t, nil)),
	}

	sess, err := coordinator.Start(t.Context(), &coordinator.Config{
		SessionID: "s1", OwnerIncarnation: 1, WorkerCapacity: 1,
		Store: store, Executor: executor, Clock: execClock(),
		IDs: execGen("failure-session"),
	})
	if err != nil {
		t.Fatalf("start session: %v", err)
	}
	t.Cleanup(sess.Stop)

	if _, submitErr := sess.Submit(t.Context(), dispatchRequest("cmd-disp-failure")); submitErr != nil {
		t.Fatalf("submit dispatch: %v", submitErr)
	}

	// Nothing was reported: the run's only committed batch is the dispatch, so
	// the attempt stays exactly where the dispatch left it.
	state, _, err := store.LoadRun(t.Context(), "r1")
	if err != nil {
		t.Fatalf("load run: %v", err)
	}
	if got := state.ModelInvocations["m1"].Attempts["a1"].Status; got != kernel.ModelAttemptStatusRequesting {
		t.Fatalf("attempt status %q, want requesting", got)
	}
	if store.appendCount() != 1 {
		t.Fatalf("%d transitions committed, want 1", store.appendCount())
	}

	// The slot was reclaimed: a second dispatch reserves successfully and is
	// rejected by the kernel instead of parking until the context ends.
	if _, err := sess.Submit(t.Context(), dispatchRequest("cmd-disp-after-failure")); err == nil {
		t.Fatalf("a second dispatch of a requesting attempt must be rejected")
	}
}
