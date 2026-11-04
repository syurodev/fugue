package pureread

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/syurodev/fugue/internal/kernel"
	"github.com/syurodev/fugue/internal/platform/errcode"
)

const (
	fixtureKey     = "guide"
	readCallID     = "read-call"
	readAttemptID  = "read-attempt"
	modelAttemptID = "model-attempt"
	nextCallID     = "next-call"
	nextAttemptID  = "next-attempt"
	fixtureOrigin  = "headless-test"
	fixtureRunID   = "run"
	fixtureToken   = uint64(7)
	fixtureBody    = "read me"
)

func TestPureReadPrepareExecuteFollowsKernelLifecycle(t *testing.T) {
	t.Parallel()
	tool := New(map[string][]byte{fixtureKey: []byte(fixtureBody)})
	state := stateWithToolFrontier()
	state = startCheck(t, state)
	plan, err := tool.Prepare(t.Context(), Args{Key: fixtureKey})
	if err != nil {
		t.Fatal(err)
	}
	if evidence := tool.Snapshot(); evidence != (Evidence{PrepareCalls: 1, ReadyPlans: 1}) {
		t.Fatalf("Prepare read fixture content: %+v", evidence)
	}
	state = recordReady(t, state)
	state = startExecute(t, state)
	result, err := tool.Execute(t.Context(), plan, nil)
	if err != nil || !bytes.Equal(result.Content, []byte(fixtureBody)) {
		t.Fatalf("Execute() = %q, %v", result.Content, err)
	}
	state = recordResult(t, state)
	if got := state.ToolInvocations[readCallID].Attempts[readAttemptID].Status; got != kernel.ToolAttemptStatusSucceeded {
		t.Fatalf("tool status = %q, want succeeded", got)
	}
	if evidence := tool.Snapshot(); evidence != (Evidence{PrepareCalls: 1, ReadyPlans: 1, ExecuteCalls: 1, Reads: 1}) {
		t.Fatalf("tool evidence = %+v", evidence)
	}
}

func startCheck(t *testing.T, state *kernel.State) *kernel.State {
	t.Helper()
	events, state := decideAndApply(t, state, &kernel.PrepareDispatchCommand{
		CommandID: "check", RunID: fixtureRunID, Origin: fixtureOrigin,
		Token: fixtureToken, InvocationID: readCallID, AttemptID: readAttemptID,
	}, "check")
	singleEvent[*kernel.ToolCheckStartedEvent](t, events)
	return state
}

func recordReady(t *testing.T, state *kernel.State) *kernel.State {
	t.Helper()
	events, state := decideAndApply(t, state, &kernel.ObserveToolCheckCommand{
		CommandID: "ready", RunID: fixtureRunID, Origin: fixtureOrigin, Token: fixtureToken,
		ToolCallID: readCallID, AttemptID: readAttemptID, CheckGeneration: 1,
		ValidatedArgsRef: "validated-args", GateObservation: "allowed", ApprovalReceiptRef: "",
	}, "ready")
	ready := singleEvent[*kernel.ToolReadyRecordedEvent](t, events)
	if ready.OneUseApprovalReceiptRef != "" {
		t.Fatal("ready event unexpectedly contains an approval receipt")
	}
	return state
}

func startExecute(t *testing.T, state *kernel.State) *kernel.State {
	t.Helper()
	events, state := decideAndApply(t, state, &kernel.PrepareDispatchCommand{
		CommandID: "dispatch", RunID: fixtureRunID, Origin: fixtureOrigin,
		Token: fixtureToken, InvocationID: readCallID, AttemptID: readAttemptID,
	}, "dispatch")
	dispatch := singleEvent[*kernel.ToolDispatchStartedEvent](t, events)
	if dispatch.ReceiptRef != "current-dispatch-authorization" {
		t.Fatal("dispatch event does not contain current dispatch authorization")
	}
	return state
}

func recordResult(t *testing.T, state *kernel.State) *kernel.State {
	t.Helper()
	events, state := decideAndApply(t, state, &kernel.ObserveToolResultCommand{
		CommandID: "result", RunID: fixtureRunID, Origin: fixtureOrigin, Token: fixtureToken,
		ToolCallID: readCallID, AttemptID: readAttemptID,
		Outcome: string(kernel.ToolResultOutcomeSucceeded), ResultRef: "result-ref",
		Code: "ok", EffectEvidence: "read-complete", SourcePosition: 0,
	}, "result")
	singleEvent[*kernel.ToolResultRecordedEvent](t, events)
	return state
}

func singleEvent[T kernel.Event](t *testing.T, events []kernel.Event) T {
	t.Helper()
	if len(events) != 1 {
		t.Fatalf("events = %d, want one", len(events))
	}
	event, ok := events[0].(T)
	if !ok {
		var zero T
		t.Fatalf("event = %T, want %T", events[0], zero)
	}
	return event
}

func TestInvalidArgsNeverReachExecute(t *testing.T) {
	t.Parallel()
	tool := New(map[string][]byte{fixtureKey: []byte(fixtureBody)})
	_, err := tool.Prepare(t.Context(), Args{Key: " \t "})
	if err == nil || errcode.CodeOf(err) != errcode.InvalidArgument {
		t.Fatalf("Prepare invalid args error = %v", err)
	}
	if evidence := tool.Snapshot(); evidence != (Evidence{PrepareCalls: 1}) {
		t.Fatalf("invalid args reached later phase: %+v", evidence)
	}
}

func TestInputOutputAndReceiptMutationsDoNotChangePinnedRead(t *testing.T) {
	t.Parallel()
	input := []byte("original")
	items := map[string][]byte{fixtureKey: input}
	tool := New(items)
	input[0] = 'X'
	items[fixtureKey][1] = 'Y'
	plan, err := tool.Prepare(t.Context(), Args{Key: fixtureKey})
	if err != nil {
		t.Fatal(err)
	}
	first, err := tool.Execute(t.Context(), plan, []string{"unneeded-receipt"})
	if err != nil {
		t.Fatal(err)
	}
	first.Content[0] = 'Z'
	second, err := tool.Execute(t.Context(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(second.Content, []byte("original")) {
		t.Fatalf("second content = %q, want original fixture bytes", second.Content)
	}
	if evidence := tool.Snapshot(); evidence.Reads != 2 || evidence.ExecuteCalls != 2 {
		t.Fatalf("receipt mutation or result alias changed execution evidence: %+v", evidence)
	}
}

func TestExecuteRevalidatesPlanAndDataRevision(t *testing.T) {
	t.Parallel()
	tool := New(map[string][]byte{fixtureKey: []byte(fixtureBody)})
	plan, err := tool.Prepare(t.Context(), Args{Key: fixtureKey})
	if err != nil {
		t.Fatal(err)
	}
	plan.revision = "stale-revision"
	if _, err = tool.Execute(t.Context(), plan, nil); err == nil || errcode.CodeOf(err) != errcode.FailedPrecondition {
		t.Fatalf("Execute stale plan error = %v", err)
	}
	if evidence := tool.Snapshot(); evidence.Reads != 0 || evidence.ExecuteCalls != 1 {
		t.Fatalf("stale plan reached read: %+v", evidence)
	}
}

func TestPrepareAndExecuteHonorCancellation(t *testing.T) {
	t.Parallel()
	tool := New(map[string][]byte{fixtureKey: []byte(fixtureBody)})
	plan, err := tool.Prepare(t.Context(), Args{Key: fixtureKey})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = tool.Prepare(ctx, Args{Key: fixtureKey}); err == nil || errcode.CodeOf(err) != errcode.Canceled {
		t.Fatalf("Prepare canceled error = %v", err)
	}
	if _, err = tool.Execute(ctx, plan, nil); err == nil || errcode.CodeOf(err) != errcode.Canceled {
		t.Fatalf("Execute canceled error = %v", err)
	}
	if evidence := tool.Snapshot(); evidence != (Evidence{PrepareCalls: 2, ReadyPlans: 1, ExecuteCalls: 1}) {
		t.Fatalf("canceled operation reached data read: %+v", evidence)
	}
}

func TestMissingKeyIsAValidEmptyRead(t *testing.T) {
	t.Parallel()
	tool := New(nil)
	plan, err := tool.Prepare(t.Context(), Args{Key: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := tool.Execute(t.Context(), plan, nil)
	if err != nil || len(result.Content) != 0 {
		t.Fatalf("missing-key read = %q, %v; want valid empty result", result.Content, err)
	}
}

func stateWithToolFrontier() *kernel.State {
	state := kernel.NewState()
	state.RunID = "run"
	state.Admitted = true
	state.AdmissionToken = 7
	state.OwnerIncarnation = 3
	state.StepStarted = true
	state.CurrentModelInvocationID = "model-call"
	state.ModelInvocations["model-call"] = &kernel.ModelInvocation{
		InvocationID: "model-call", Completed: true, SelectedAttemptID: modelAttemptID,
		ToolIDs: []string{readCallID, nextCallID},
		Attempts: map[string]*kernel.ModelAttempt{
			modelAttemptID: {AttemptID: modelAttemptID, Status: kernel.ModelAttemptStatusCompleted},
		},
	}
	state.ToolOrder = []string{readCallID, nextCallID}
	state.ToolInvocations[readCallID] = &kernel.ToolInvocation{
		ToolCallID: readCallID, SourcePosition: 0, DescriptorVersion: "pure-read-v1",
		EffectClass: kernel.EffectClassPureRead, CheckGeneration: 1,
		Attempts: map[string]*kernel.ToolAttempt{
			readAttemptID: {AttemptID: readAttemptID, Status: kernel.ToolAttemptStatusPlanned},
		},
	}
	state.ToolInvocations[nextCallID] = &kernel.ToolInvocation{
		ToolCallID: nextCallID, SourcePosition: 1, DescriptorVersion: "pure-read-v1",
		EffectClass: kernel.EffectClassPureRead, CheckGeneration: 1,
		Attempts: map[string]*kernel.ToolAttempt{
			nextAttemptID: {AttemptID: nextAttemptID, Status: kernel.ToolAttemptStatusPlanned},
		},
	}
	return state
}

func decideAndApply(t *testing.T, state *kernel.State, command kernel.Command, commandID string) ([]kernel.Event, *kernel.State) {
	t.Helper()
	facts := &kernel.DecisionFacts{
		Envelope: kernel.Envelope{
			RunID: fixtureRunID, CommandID: commandID, Origin: fixtureOrigin, Token: fixtureToken,
			CausationID: commandID, Sequence: state.LastSequence + 1,
			Timestamp: time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC),
		},
		OwnerIncarnation: 3, ExecutorFence: 11, WorkerReservationRef: "worker-reservation",
		DispatchAuthorizationRef: "current-dispatch-authorization", BudgetReservationRef: "tool-budget",
	}
	events, err := kernel.Decide(state, command, facts)
	if err != nil {
		t.Fatalf("Decide(%T): %v", command, err)
	}
	for _, event := range events {
		state = kernel.Evolve(state, event)
	}
	return events, state
}
